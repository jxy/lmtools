package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	"lmtools/internal/session"
	"lmtools/internal/workingcontext"
	"strconv"
	"strings"
)

type quietCheckpointOutput struct{}

func (quietCheckpointOutput) HandleStreamEvent(core.ResponseStreamEvent) {}
func (quietCheckpointOutput) HandleResponseComplete(core.Response)       {}

type checkpointNotifier struct{ core.Notifier }

func (n checkpointNotifier) ReportUsage(u *core.Usage, _ bool) {
	if reporter, ok := n.Notifier.(interface{ ReportCheckpointUsage(*core.Usage) }); ok {
		reporter.ReportCheckpointUsage(u)
		return
	}
	if u != nil {
		n.Infof("Checkpoint tokens: %s", u.DisplaySummary())
	}
}

func turnContextWindow(env *turnEnv, opts core.RequestOptions) *workingcontext.Engine {
	if !opts.Context.Enabled() && opts.Context.ManualCheckpoint == nil {
		return nil
	}
	return newContextWindow(env, opts)
}

func newContextWindow(env *turnEnv, opts core.RequestOptions) *workingcontext.Engine {
	history := &workingcontext.History{}
	canRead := false
	if host, ok := opts.MCP.(*workingcontext.Tools); ok {
		history = &host.History
		history.SetCompactionAvailable(opts.Context.Compacts() && opts.Context.Window > 0)
		for _, tool := range host.Tools() {
			if tool.Server == "history" && tool.Tool.Name == "read" {
				canRead = opts.ToolEnabled
				break
			}
		}
	}
	definitions := toolDefinitionsForOptions(opts)
	schemas, _ := json.Marshal(definitions)
	e := &workingcontext.Engine{
		Binding: opts.Context, History: history, CanReadHistory: canRead,
		WriterProfile: fmt.Sprintf("%s/%s/%s/%t", opts.Provider, core.EffectiveChatModel(opts), opts.Effort, opts.OpenAIResponses),
		ExtraTokens:   (len(schemas)+len(opts.JSONSchema))/2 + 128, SchemaTokens: len(opts.JSONSchema) / 2, Notice: env.notifier.Infof,
		Sizing: sizingIdentity(opts, schemas),
	}
	send := func(ctx context.Context, cfg core.RequestOptions, messages []core.TypedMessage, tools []core.ToolDefinition, op string) (core.Response, error) {
		req, body, err := core.BuildChatRequest(cfg, messages, core.ChatBuildOptions{ToolDefs: tools})
		if err != nil {
			return core.Response{}, err
		}
		logBuiltHTTPRequest(ctx, env.cfg, env.logDir, env.notifier, req, body, op)
		resp, err := sendWithRetry(ctx, newRetryClient(ctx, env.cfg), req)
		if err != nil {
			return core.Response{}, err
		}
		return core.HandleResponseWithOptions(ctx, cfg, resp, logger.From(ctx), checkpointNotifier{env.notifier}, core.ResponseParseOptions{ArgoLegacy: cfg.ArgoLegacy, Output: quietCheckpointOutput{}})
	}
	ask := func(ctx context.Context, instructions, source string, measure bool) (core.Response, error) {
		writer := opts
		writer.ToolEnabled = false
		writer.MCP = nil
		writer.StreamChat = false
		writer.JSONMode = false
		writer.JSONSchema = nil
		writer.Developer = ""
		writer.Images = nil
		writer.System = instructions
		writer.EffectiveSystem = instructions
		writer.SystemExplicitlySet = true
		// The input-window setting can deliberately force early cuts on a larger
		// model. It must not starve a max-effort writer of reasoning tokens.
		writer.MaxTokens = 0
		op := "context_checkpoint_input"
		if measure {
			writer.MaxTokens = 64
			op = "context_measure_input"
		}
		messages := []core.TypedMessage{core.NewTextMessage("system", instructions), core.NewTextMessage("user", source)}
		answer, err := send(ctx, writer, messages, nil, op)
		if err == nil && !measure {
			err = core.ValidateResponse(writer, answer)
		}
		return answer, err
	}
	e.Write = func(ctx context.Context, instructions, source string) (string, error) {
		answer, err := ask(ctx, instructions, source, false)
		if err != nil {
			return "", err
		}
		if len(answer.ToolCalls) > 0 {
			return "", fmt.Errorf("checkpoint writer returned a tool call")
		}
		return strings.TrimSpace(answer.Text), nil
	}
	inputCount := func(answer core.Response, err error) (int, error) {
		if err != nil {
			return 0, err
		}
		count, ok := answer.Usage.MeasuredInput(opts)
		if !ok {
			return 0, fmt.Errorf("provider omitted input usage")
		}
		return count, nil
	}
	e.CountText = func(ctx context.Context, text string) (int, error) {
		return inputCount(ask(ctx, "Measure this input for a context-window check. Reply only OK.", text, true))
	}
	e.CountMessages = func(ctx context.Context, messages []core.TypedMessage) (int, error) {
		counter := opts
		counter.StreamChat = false
		counter.MaxTokens = 64
		counter.JSONMode = false
		counter.JSONSchema = nil
		// Preserve the ordinary tools and native signed blocks. The parsing
		// path reads usage and never enters the tool executor.
		return inputCount(send(ctx, counter, messages, definitions, "context_measure_native_input"))
	}
	return e
}

// sizingIdentity names what a provider's count of a request depends on
// besides its messages: the endpoint and model the request goes to, its
// wire, the reasoning settings that decide which earlier reasoning the
// provider charges, and the tool and answer schemas it carries.
func sizingIdentity(opts core.RequestOptions, schemas []byte) string {
	h := sha256.New()
	for _, part := range []string{
		opts.Provider, opts.ProviderURL, opts.Env, strconv.FormatBool(opts.ArgoLegacy),
		core.EffectiveChatModel(opts), strconv.FormatBool(opts.OpenAIResponses),
		opts.Effort, opts.ReasoningMode, opts.ReasoningContext,
		strconv.FormatBool(opts.JSONMode), string(opts.JSONSchema), string(schemas),
	} {
		fmt.Fprintf(h, "%d:%s\x00", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// buildTurnRequest builds the request of a turn, through the context window
// when the run has one. The transcript comes from where the history came: a
// plan read from a saved session keeps its checkpoints there, although
// nothing of this run is written yet.
func buildTurnRequest(ctx context.Context, env *turnEnv, opts core.RequestOptions, plan *session.Plan, input string) (core.RequestBuild, error) {
	var raw []core.TypedMessage
	if plan != nil {
		raw = plan.Messages
	}
	if env.window == nil {
		return buildHTTPRequest(ctx, env.cfg, opts, raw, input)
	}
	var refs []contextstate.Ref
	var transcript workingcontext.Transcript
	if plan == nil {
		var err error
		raw, err = core.NewMemorySessionStoreForRequest(opts, input).Messages("")
		if err != nil {
			return core.RequestBuild{}, err
		}
		transcript = workingcontext.MemoryTranscript(nil)
	} else {
		refs, transcript = plan.Refs, plan.Transcript
	}
	view, err := env.window.Project(ctx, raw, refs, transcript, env.cfg.PrintCurl)
	if err != nil {
		return core.RequestBuild{}, err
	}
	return buildHTTPRequest(ctx, env.cfg, opts, view, input)
}

// contextToolStore returns where the tool loop saves its rounds and how it
// builds each follow-up request, through the context window when the run
// has one.
func contextToolStore(ctx context.Context, env *turnEnv, opts core.RequestOptions, sess *session.Session, input string) (core.SessionStore, func(string) ([]core.TypedMessage, error), error) {
	if env.window == nil {
		store, build := createToolStoreAndMessageBuilder(opts, sess, input)
		return store, build, nil
	}
	if sess == nil {
		store := core.NewMemorySessionStoreForRequest(opts, input)
		initial, err := store.Messages("")
		if err != nil {
			return nil, nil, err
		}
		refs := workingcontext.MemoryRefs(initial)
		env.window.History.Bind(initial, refs, 0, workingcontext.MemoryTranscript(refs))
		return store, func(path string) ([]core.TypedMessage, error) {
			msgs, err := store.Messages(path)
			if err != nil {
				return nil, err
			}
			refs := workingcontext.MemoryRefs(msgs)
			return env.window.Project(ctx, msgs, refs, workingcontext.MemoryTranscript(refs), false)
		}, nil
	}
	view, err := sess.View(opts)
	if err != nil {
		return nil, nil, err
	}
	if err = env.window.BindExisting(ctx, view.Messages, view.Refs, view.Transcript); err != nil {
		return nil, nil, err
	}
	return sess.ToolStore(), func(string) ([]core.TypedMessage, error) {
		view, err := sess.View(opts)
		if err != nil {
			return nil, err
		}
		return env.window.Project(ctx, view.Messages, view.Refs, view.Transcript, false)
	}, nil
}
