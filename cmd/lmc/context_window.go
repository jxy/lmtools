package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"lmtools/internal/core"
	"lmtools/internal/logger"
	session "lmtools/internal/oldsession"
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

func newContextWindow(env *turnEnv, opts core.RequestOptions) *workingcontext.Engine {
	if !opts.Context.Enabled() {
		return nil
	}
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

func buildTurnRequest(ctx context.Context, env *turnEnv, opts core.RequestOptions, plan *session.RequestPlan, input string) (core.RequestBuild, error) {
	if env.window == nil {
		return buildHTTPRequest(ctx, env.cfg, opts, plan, input)
	}
	var raw []core.TypedMessage
	var refs []session.ContextRef
	if plan == nil {
		var err error
		raw, err = core.NewMemorySessionStoreForRequest(opts, input).Messages("")
		if err != nil {
			return core.RequestBuild{}, err
		}
	} else {
		raw, refs = plan.Messages, plan.Refs
	}
	view, err := env.window.Project(ctx, raw, refs, nil, env.cfg.PrintCurl)
	if err != nil {
		return core.RequestBuild{}, err
	}
	return buildHTTPRequest(ctx, env.cfg, opts, &session.RequestPlan{Messages: view}, input)
}

func contextToolStore(ctx context.Context, env *turnEnv, opts core.RequestOptions, sess *session.Session, input string) (core.SessionStore, func(string) ([]core.TypedMessage, error), error) {
	if env.window == nil {
		store, build := createToolStoreAndMessageBuilder(ctx, opts, sess, input)
		return store, build, nil
	}
	if sess == nil {
		store := core.NewMemorySessionStoreForRequest(opts, input)
		initial, err := store.Messages("")
		if err != nil {
			return nil, nil, err
		}
		env.window.History.Bind(initial, workingcontext.MemoryRefs(initial), 0, nil)
		return store, func(path string) ([]core.TypedMessage, error) {
			msgs, err := store.Messages(path)
			if err != nil {
				return nil, err
			}
			return env.window.Project(ctx, msgs, workingcontext.MemoryRefs(msgs), nil, false)
		}, nil
	}
	reader, err := session.CreateContextBuilder(ctx, sess)
	if err != nil {
		return nil, nil, err
	}
	msgs, refs, err := reader()
	if err != nil {
		return nil, nil, err
	}
	if err = env.window.BindExisting(ctx, msgs, refs, sess); err != nil {
		return nil, nil, err
	}
	return session.NewStore(sess, logger.From(ctx)), func(string) ([]core.TypedMessage, error) {
		msgs, refs, err := reader()
		if err != nil {
			return nil, err
		}
		return env.window.Project(ctx, msgs, refs, sess, false)
	}, nil
}
