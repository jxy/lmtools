package workingcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/contextcfg"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	session "lmtools/internal/oldsession"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// storedConversation is a saved session that a test extends one exchange
// at a time.
type storedConversation struct {
	t    *testing.T
	ctx  context.Context
	s    *session.Session
	raw  []core.TypedMessage
	refs []session.ContextRef
}

func newStoredConversation(t *testing.T) *storedConversation {
	t.Helper()
	old := session.GetSessionsDir()
	session.SetSessionsDir(t.TempDir())
	t.Cleanup(func() { session.SetSessionsDir(old) })
	s, err := session.CreateSession("", core.NewTestLogger(false))
	if err != nil {
		t.Fatal(err)
	}
	return &storedConversation{t: t, ctx: context.Background(), s: s}
}

// exchange saves a question and an answer produced under receipt.
func (c *storedConversation) exchange(question, answer string, receipt *session.WindowReceipt) {
	c.t.Helper()
	c.ask(question)
	c.answer(answer, receipt)
}

func (c *storedConversation) ask(question string) {
	c.t.Helper()
	if _, err := session.AppendMessageWithToolInteraction(c.ctx, c.s, session.Message{Role: core.RoleUser, Content: question}, nil, nil); err != nil {
		c.t.Fatal(err)
	}
}

// answer saves a final answer produced under receipt.
func (c *storedConversation) answer(text string, receipt *session.WindowReceipt) {
	c.t.Helper()
	c.s.WindowUsed = receipt
	if _, err := session.SaveAssistantResponse(c.ctx, c.s, core.Response{Text: text}, "model"); err != nil {
		c.t.Fatal(err)
	}
	c.reload()
}

// toolTurn saves a question, a call to tool, and the call's output.
func (c *storedConversation) toolTurn(question, tool, output string) {
	c.t.Helper()
	c.ask(question)
	if _, err := session.SaveAssistantResponseWithTools(c.ctx, c.s, "", []core.ToolCall{{ID: "call", Name: tool, Args: json.RawMessage(`{}`)}}, "model"); err != nil {
		c.t.Fatal(err)
	}
	if _, err := session.SaveToolResults(c.ctx, c.s, []core.ToolResult{{ID: "call", Output: output}}, ""); err != nil {
		c.t.Fatal(err)
	}
	c.reload()
}

func (c *storedConversation) reload() {
	c.t.Helper()
	var err error
	if c.s, err = session.OpenSession(c.ctx, c.s.Path); err != nil {
		c.t.Fatal(err)
	}
	read, err := session.CreateContextBuilder(c.ctx, c.s)
	if err != nil {
		c.t.Fatal(err)
	}
	if c.raw, c.refs, err = read(); err != nil {
		c.t.Fatal(err)
	}
}

// next projects the request that a further question would send.
func (c *storedConversation) next(e *Engine) ([]core.TypedMessage, error) {
	return c.request(e, core.NewTextMessage("user", "continue"))
}

// request projects the saved conversation followed by pending input.
func (c *storedConversation) request(e *Engine, pending ...core.TypedMessage) ([]core.TypedMessage, error) {
	refs, transcript := saved(c.refs, c.s)
	return e.Project(c.ctx, append(slices.Clone(c.raw), pending...), refs, transcript, false)
}

func recordNotices(e *Engine) *[]string {
	var notices []string
	e.Notice = func(format string, args ...any) { notices = append(notices, fmt.Sprintf(format, args...)) }
	return &notices
}

// A window that began with the notebook catalog keeps it in its prefix.
// When the request outgrows the ceiling, a checkpoint makes room.
func TestFirstWindowKeepsItsCatalogUntilACheckpoint(t *testing.T) {
	ctx := context.Background()
	e := testEngine(contextcfg.Handoff)
	e.Binding.Window = 4096
	e.Binding.Initial = "Project notebook at window start.\n" + strings.Repeat("note @rev — Title. A summary of the note.\n", 120)
	writes := 0
	e.Write = func(context.Context, string, string) (string, error) { writes++; return "HANDOFF", nil }
	raw := []core.TypedMessage{core.NewTextMessage("system", "sys"), core.NewTextMessage("user", "first question")}
	view, err := e.Project(ctx, raw, MemoryRefs(raw), MemoryTranscript(MemoryRefs(raw)), false)
	b, _ := json.Marshal(view)
	if err != nil || !strings.Contains(string(b), "window start") || e.Receipt().CatalogOmitted {
		t.Fatal("first request", err)
	}
	raw = append(raw, core.NewTextMessage("assistant", strings.Repeat("answer ", 300)), core.NewTextMessage("user", "second question"))
	if _, err := e.Project(ctx, raw, MemoryRefs(raw), MemoryTranscript(MemoryRefs(raw)), false); err != nil || writes == 0 || e.current.Count == 0 {
		t.Fatalf("expected a checkpoint: writes %d, count %d: %v", writes, e.current.Count, err)
	}
}

// A receipt that records a window without the catalog keeps the window's
// later requests without it, and one with the catalog keeps it.
func TestReceiptKeepsTheFirstWindowsCatalogChoice(t *testing.T) {
	for _, omitted := range []bool{false, true} {
		e := testEngine(contextcfg.Handoff)
		e.Binding.Initial = "Project notebook at window start: one note."
		raw := []core.TypedMessage{core.NewTextMessage("system", "sys"), core.NewTextMessage("user", "q1"), core.NewTextMessage("assistant", "a1"), core.NewTextMessage("user", "q2")}
		refs := MemoryRefs(raw)
		refs[2].Role, refs[2].Receipt = core.RoleAssistant, &contextstate.Receipt{CatalogOmitted: omitted, View: "catalog"}
		view, err := e.Project(context.Background(), raw, refs, savedInMemory{MemoryTranscript(refs)}, false)
		b, _ := json.Marshal(view)
		if err != nil || strings.Contains(string(b), "one note") == omitted || e.Receipt().CatalogOmitted != omitted || e.Receipt().CatalogSent == omitted {
			t.Fatal(omitted, err)
		}
	}
}

// A full replay never reaches a checkpoint. Once a request has carried the
// notebook catalog, later requests keep it, and one that no longer fits
// beside it reports its size instead of changing the prefix.
func TestFullReplayKeepsASentCatalog(t *testing.T) {
	c := newStoredConversation(t)
	engine := func() *Engine {
		e := testEngine(contextcfg.Full)
		e.Binding.Initial = "Project notebook at window start.\n" + strings.Repeat("note @rev — Title. A summary of the note.\n", 60)
		return e
	}
	first := engine()
	if _, err := c.next(first); err != nil || !first.Receipt().CatalogSent {
		t.Fatal("first request", err)
	}
	c.exchange("continue", strings.Repeat("answer ", 1900), first.Receipt())
	later := engine()
	notices := recordNotices(later)
	if _, err := c.next(later); !exceedsBudget(err) || len(*notices) != 0 {
		t.Fatalf("notices %q: %v", *notices, err)
	}
}

// A provider that refuses a sizing request stops the turn at its first
// probe. The catalog decision does not ask again.
func TestFirstWindowStopsAtACredentialRefusal(t *testing.T) {
	e := testEngine(contextcfg.Handoff)
	e.Binding.Window = 4096
	e.Binding.Initial = "Project notebook at window start.\n" + strings.Repeat("note @rev — Title. A summary of the note.\n", 200)
	probes := 0
	e.CountText = func(context.Context, string) (int, error) {
		probes++
		return 0, &lmerrors.HTTPError{StatusCode: http.StatusForbidden}
	}
	raw := []core.TypedMessage{core.NewTextMessage("system", "sys"), core.NewTextMessage("user", "short question")}
	if _, err := e.Project(context.Background(), raw, MemoryRefs(raw), MemoryTranscript(MemoryRefs(raw)), false); !credentialRefusal(err) || probes != 1 {
		t.Fatal(probes, err)
	}
}

// Retrying a turn finds the checkpoint its failed attempt saved and sends it
// without measuring the old transcript.
func TestRetryUsesASavedCheckpointWithoutMeasuring(t *testing.T) {
	c := newStoredConversation(t)
	for i := 0; i < 3; i++ {
		c.exchange(fmt.Sprintf("question %d %s", i, strings.Repeat("q", 3000)), fmt.Sprintf("answer %d %s", i, strings.Repeat("a", 3000)), nil)
	}
	engine := func() *Engine {
		e := testEngine(contextcfg.Handoff)
		e.Binding.Initial = "Project notebook at window start: one note."
		return e
	}
	first := engine()
	first.Write = func(context.Context, string, string) (string, error) { return "SAVED-HANDOFF", nil }
	if _, err := c.next(first); err != nil || first.current.Count == 0 {
		t.Fatal("no checkpoint to retry with", err)
	}
	retry := engine()
	probes := 0
	retry.CountMessages = func(context.Context, []core.TypedMessage) (int, error) { probes++; return 1, nil }
	retry.CountText = func(context.Context, string) (int, error) { probes++; return 1, nil }
	retry.Write = func(context.Context, string, string) (string, error) {
		t.Fatal("rewrote a saved checkpoint")
		return "", nil
	}
	if _, err := c.next(retry); err != nil || probes != 0 || retry.current.Handoff != "SAVED-HANDOFF" {
		t.Fatalf("probes %d, handoff %q: %v", probes, retry.current.Handoff, err)
	}
}

// fallbackAfter saves a conversation whose second window is an archive
// fallback, chosen with history reads on, and one more exchange under that
// fallback that ends with reply.
func fallbackAfter(t *testing.T, reply string) *storedConversation {
	c := newStoredConversation(t)
	e := testEngine(contextcfg.Handoff)
	e.CanReadHistory = true
	c.exchange("FIRST-RAW "+strings.Repeat("a", 3000), "finished", nil)
	e.Write = func(context.Context, string, string) (string, error) { return "FIRST-HANDOFF", nil }
	if _, err := c.next(e); err != nil || e.current.Count != 2 {
		t.Fatal(e.current, err)
	}
	c.exchange("continue", "MISSED-EVIDENCE "+strings.Repeat("b", 18000), e.Receipt())
	e.Write = func(context.Context, string, string) (string, error) {
		return "", errors.New("temporary writer failure")
	}
	if _, err := c.next(e); err != nil || !e.current.Fallback {
		t.Fatal(e.current, err)
	}
	c.exchange("continue", reply, e.Receipt())
	return c
}

// An archive fallback leaves the exchanges it covers to history reads. A
// run without them rewrites the handoff. If the writer fails again, the
// operator and the model learn what the window is missing.
func TestArchiveFallbackWithoutHistoryReads(t *testing.T) {
	c := fallbackAfter(t, "short reply")
	for _, works := range []bool{false, true} {
		off := testEngine(contextcfg.Handoff)
		notices := recordNotices(off)
		var sources strings.Builder
		off.Write = func(_ context.Context, _, source string) (string, error) {
			sources.WriteString(source)
			if !works {
				return "", errors.New("writer still failing")
			}
			return "REBUILT-HANDOFF", nil
		}
		view, err := c.next(off)
		b, _ := json.Marshal(view)
		said := strings.Join(*notices, "\n")
		if err != nil || !strings.Contains(sources.String(), "FIRST-HANDOFF") || !strings.Contains(sources.String(), "MISSED-EVIDENCE") || !strings.Contains(said, "Rebuilding the handoff") {
			t.Fatalf("writer %t: %v %q", works, err, said)
		}
		if works && (off.current.Fallback || !strings.Contains(string(b), "REBUILT-HANDOFF")) {
			t.Fatal("kept the fallback after a successful rewrite")
		}
		if !works && (!off.current.Fallback || !strings.Contains(string(b), "history tools are unavailable now") || !strings.Contains(said, "archive note")) {
			t.Fatalf("missing access went unreported: %q", said)
		}
	}
}

// A rewritten handoff also covers the exchanges after the fallback when the
// window could not hold them beside a full handoff.
func TestArchiveRecoveryCoversTheLatestExchange(t *testing.T) {
	c := fallbackAfter(t, "LATER-ANSWER "+strings.Repeat("c", 21000))
	off := testEngine(contextcfg.Handoff)
	var sources strings.Builder
	off.Write = func(_ context.Context, _, source string) (string, error) {
		sources.WriteString(source)
		return "REBUILT-HANDOFF", nil
	}
	view, err := c.next(off)
	b, _ := json.Marshal(view)
	if err != nil || off.current.Fallback || off.current.Count != len(c.refs) || !strings.Contains(string(b), "REBUILT-HANDOFF") {
		t.Fatalf("checkpoint through %d of %d, fallback %t: %v", off.current.Count, len(c.refs), off.current.Fallback, err)
	}
	if !strings.Contains(sources.String(), "MISSED-EVIDENCE") || !strings.Contains(sources.String(), "LATER-ANSWER") {
		t.Fatal("the writer did not read the exchanges the handoff covers")
	}
}

// A fallback written during a tool turn can page a large result while
// history reads are on. A rewritten handoff cannot page it, so once the
// turn has finished the handoff covers the whole turn.
func TestArchiveRecoveryReplacesAPagedResult(t *testing.T) {
	c := newStoredConversation(t)
	c.toolTurn("Find the row.", "events", "head "+strings.Repeat("r", 15000)+" ROW-IN-THE-MIDDLE "+strings.Repeat("r", 15000))
	on := testEngine(contextcfg.Handoff)
	on.CanReadHistory = true
	on.Write = func(context.Context, string, string) (string, error) {
		return "", errors.New("temporary writer failure")
	}
	if _, err := c.request(on); err != nil || !on.current.Fallback || on.current.PagedResult == "" {
		t.Fatal(on.current, err)
	}
	c.answer("The row is in the middle.", on.Receipt())
	off := testEngine(contextcfg.Handoff)
	writes := 0
	off.Write = func(context.Context, string, string) (string, error) { writes++; return "REBUILT-HANDOFF", nil }
	if _, err := c.next(off); err != nil || writes == 0 || off.current.Fallback || off.current.PagedResult != "" || off.current.Count != len(c.refs) {
		t.Fatalf("%d writes, checkpoint through %d of %d, fallback %t, paged %q: %v", writes, off.current.Count, len(c.refs), off.current.Fallback, off.current.PagedResult, err)
	}
}

// countQuestions counts a token per byte of a question and a token per four
// bytes of other text.
func countQuestions(text string) int {
	if strings.Contains(text, "QQQQ") {
		return len(text)
	}
	return len(text) / 4
}

// A request that did not fit keeps a record of the checkpoint it used, so a
// retry of that request measures nothing the handoff covers. A different
// request at the same point is measured and gets what it would have had
// without the checkpoint.
func TestSavedCheckpointSkipsSizingOnlyForItsOwnRequest(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange("q0", "a0 "+strings.Repeat("a", 8000), nil)
	c.toolTurn("Find the row.", "events", strings.Repeat("r", 7000)+" ROW-IN-THE-MIDDLE "+strings.Repeat("r", 7500))
	probes, earlier := 0, 0
	attempt := func(question string) (*Engine, string, error) {
		e := testEngine(contextcfg.Handoff)
		e.CanReadHistory = true
		e.Write = func(context.Context, string, string) (string, error) { return "HANDOFF", nil }
		e.CountText = func(_ context.Context, text string) (int, error) {
			probes++
			if strings.Contains(text, "aaaa") {
				earlier++
			}
			return countQuestions(text), nil
		}
		view, err := c.request(e, core.NewTextMessage("user", question))
		b, _ := json.Marshal(view)
		return e, string(b), err
	}
	long := "LONG " + strings.Repeat("Q", 5000)
	if e, _, err := attempt(long); err != nil || e.current.PagedResult == "" {
		t.Fatal("the long question should need its result paged", err)
	}
	if e, view, err := attempt("short question"); err != nil || e.current.Count != 0 || !strings.Contains(view, "ROW-IN-THE-MIDDLE") {
		t.Fatalf("short question used checkpoint through %d: %v", e.current.Count, err)
	}
	medium := "MEDIUM " + strings.Repeat("Q", 2500)
	for i := 0; i < 2; i++ {
		earlier = 0
		e, view, err := attempt(medium)
		if err != nil || e.current.Count == 0 || e.current.PagedResult != "" || !strings.Contains(view, "ROW-IN-THE-MIDDLE") {
			t.Fatalf("medium question: checkpoint through %d, paged %q: %v", e.current.Count, e.current.PagedResult, err)
		}
		if i == 1 && earlier != 0 {
			t.Fatalf("retrying the medium question measured the answer its handoff covers %d times", earlier)
		}
	}
	probes = 0
	if e, _, err := attempt(long); err != nil || probes != 0 || e.current.PagedResult == "" {
		t.Fatalf("retrying the long question measured %d times: %v", probes, err)
	}
}

// A sizing decision made while the meter fails compacts the request, but
// it is not recorded: a retry measures again once the meter works and sends
// the result whole when it fits. This holds whether the meter failed at the
// first measurement or after the request had measured too large.
func TestMeterOutageLeavesNoOverflowRecord(t *testing.T) {
	for _, tc := range []struct{ name, question, fails string }{
		{"before the cut", "short question", ""},
		{"while fitting the handoff", "MEDIUM " + strings.Repeat("Q", 2500), "Result call"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newStoredConversation(t)
			c.exchange("q0", "a0 "+strings.Repeat("a", 8000), nil)
			c.toolTurn("Find the row.", "events", strings.Repeat("r", 7000)+" ROW-IN-THE-MIDDLE "+strings.Repeat("r", 7500))
			probes := 0
			attempt := func(outage bool) (*Engine, string, error) {
				e := testEngine(contextcfg.Handoff)
				e.CanReadHistory = true
				e.Write = func(context.Context, string, string) (string, error) { return "HANDOFF", nil }
				e.CountText = func(_ context.Context, text string) (int, error) {
					probes++
					if outage && strings.Contains(text, tc.fails) {
						return 0, errors.New("meter unavailable")
					}
					return countQuestions(text), nil
				}
				view, err := c.request(e, core.NewTextMessage("user", tc.question))
				b, _ := json.Marshal(view)
				return e, string(b), err
			}
			if e, _, err := attempt(true); err != nil || e.current.PagedResult == "" {
				t.Fatal("the outage should page the result", err)
			}
			probes = 0
			e, view, err := attempt(false)
			if err != nil || probes == 0 || e.current.PagedResult != "" || !strings.Contains(view, "ROW-IN-THE-MIDDLE") {
				t.Fatalf("after the outage: %d probes, paged %q: %v", probes, e.current.PagedResult, err)
			}
		})
	}
}

// Public text sizes stand in for a native count the provider could not
// give, and their refusal is not recorded either. Once native counts work,
// a retry measures the request and sends it without a checkpoint when it
// fits.
func TestNativeMeterOutageLeavesNoOverflowRecord(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange("q0", "a0 "+strings.Repeat("a", 9000), nil)
	c.toolTurn("Find the row.", "events", strings.Repeat("r", 6000))
	attempt := func(outage bool) (*Engine, int, error) {
		e := testEngine(contextcfg.Handoff)
		e.Write = func(context.Context, string, string) (string, error) { return "HANDOFF", nil }
		native := 0
		e.CountMessages = func(context.Context, []core.TypedMessage) (int, error) {
			native++
			if outage {
				return 0, errors.New("native count unavailable")
			}
			return 1000, nil
		}
		e.CountText = func(_ context.Context, text string) (int, error) { return len(text), nil }
		_, err := c.request(e, core.NewTextMessage("user", "short question"))
		return e, native, err
	}
	if e, _, err := attempt(true); err != nil || e.current.Count == 0 {
		t.Fatal("public text sizes should need a checkpoint", err)
	}
	if e, native, err := attempt(false); err != nil || native == 0 || e.current.Count != 0 {
		t.Fatalf("after the outage: %d native counts, checkpoint through %d: %v", native, e.current.Count, err)
	}
}

// Two runs of one request can write different handoffs. When the first to
// record its checkpoint paged the tool result and the second kept it whole,
// a retry of the request sends the result whole.
func TestRetryPrefersTheRecordThatKeptTheResultWhole(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange("q0", "a0 "+strings.Repeat("a", 8000), nil)
	c.toolTurn("Find the row.", "events", strings.Repeat("r", 7000)+" ROW-IN-THE-MIDDLE "+strings.Repeat("r", 7500))
	question := "MEDIUM " + strings.Repeat("Q", 3150)
	engine := func(handoff string, write func()) *Engine {
		e := testEngine(contextcfg.Handoff)
		e.CanReadHistory = true
		e.Write = func(context.Context, string, string) (string, error) { write(); return handoff, nil }
		e.CountText = func(_ context.Context, text string) (int, error) { return countQuestions(text), nil }
		return e
	}
	long := engine("LONG HANDOFF "+strings.Repeat("Q", 740), func() {})
	short := engine("HANDOFF", func() {
		if long.current.Count > 0 {
			return
		}
		if _, err := c.request(long, core.NewTextMessage("user", question)); err != nil || long.current.PagedResult == "" {
			t.Fatal("the long handoff should need the result paged", err)
		}
	})
	if _, err := c.request(short, core.NewTextMessage("user", question)); err != nil || short.current.PagedResult != "" {
		t.Fatalf("the short handoff paged %q: %v", short.current.PagedResult, err)
	}
	retry := engine("", func() { t.Error("the retry wrote a handoff") })
	view, err := c.request(retry, core.NewTextMessage("user", question))
	b, _ := json.Marshal(view)
	if err != nil || retry.current.PagedResult != "" || !strings.Contains(string(b), "ROW-IN-THE-MIDDLE") {
		t.Fatalf("the retry paged %q: %v", retry.current.PagedResult, err)
	}
}

// A request measured too large under one configuration says nothing about
// the same messages sent to another endpoint or with other tools. The other
// configuration measures them and sends them whole when they fit.
func TestOverflowRecordBelongsToItsConfiguration(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange("q0", "a0 "+strings.Repeat("a", 8000), nil)
	c.toolTurn("Find the row.", "events", strings.Repeat("r", 6000))
	attempt := func(sizing string, bytesPerToken int) (*Engine, int, error) {
		e := testEngine(contextcfg.Handoff)
		e.Sizing = sizing
		e.Write = func(context.Context, string, string) (string, error) { return "HANDOFF", nil }
		probes := 0
		e.CountText = func(_ context.Context, text string) (int, error) {
			probes++
			return len(text) / bytesPerToken, nil
		}
		_, err := c.request(e, core.NewTextMessage("user", "QUESTION "+strings.Repeat("Q", 3000)))
		return e, probes, err
	}
	if e, _, err := attempt("endpoint A", 1); err != nil || e.current.Count == 0 {
		t.Fatal("the first endpoint should need a checkpoint", err)
	}
	if e, probes, err := attempt("endpoint B", 4); err != nil || probes == 0 || e.current.Count != 0 {
		t.Fatalf("the second endpoint measured %d times and used checkpoint through %d: %v", probes, e.current.Count, err)
	}
}

// A request that loses the race to publish a checkpoint takes the winner's
// result whole when it fits, although the winner paged it for a longer
// request.
func TestConcurrentPagedCheckpointIsOfferedWhole(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange("q0", "a0 "+strings.Repeat("a", 8000), nil)
	c.toolTurn("Find the row.", "events", strings.Repeat("r", 7000)+" ROW-IN-THE-MIDDLE "+strings.Repeat("r", 7500))
	engine := func(write func()) *Engine {
		e := testEngine(contextcfg.Handoff)
		e.CanReadHistory = true
		e.Write = func(context.Context, string, string) (string, error) { write(); return "HANDOFF", nil }
		e.CountText = func(_ context.Context, text string) (int, error) { return countQuestions(text), nil }
		return e
	}
	long := engine(func() {})
	medium := engine(func() {
		if long.current.Count > 0 {
			return
		}
		if _, err := c.request(long, core.NewTextMessage("user", "LONG "+strings.Repeat("Q", 5000))); err != nil || long.current.PagedResult == "" {
			t.Fatal("the long question should need its result paged", err)
		}
	})
	view, err := c.request(medium, core.NewTextMessage("user", "MEDIUM "+strings.Repeat("Q", 2500)))
	b, _ := json.Marshal(view)
	if err != nil || medium.current.Count == 0 || medium.current.PagedResult != "" || !strings.Contains(string(b), "ROW-IN-THE-MIDDLE") {
		t.Fatalf("checkpoint through %d, paged %q: %v", medium.current.Count, medium.current.PagedResult, err)
	}
}

// A request whose own handoff kept a tool result whole does not trade it
// for another request's excerpts when that request's whole result does not
// fit.
func TestReuseKeepsAWholeResultOverExcerpts(t *testing.T) {
	e := testEngine(contextcfg.Handoff)
	e.CanReadHistory = true
	raw := []core.TypedMessage{
		core.NewTextMessage("user", "Find the row."),
		{Role: "assistant", Blocks: []core.Block{core.ToolUseBlock{ID: "call", Name: "events", Input: json.RawMessage(`{}`)}}},
		{Role: "user", Blocks: []core.Block{core.ToolResultBlock{ToolUseID: "call", Name: "events", Content: strings.Repeat("r", 10000)}}},
		core.NewTextMessage("user", "continue"),
	}
	e.t = MemoryTranscript(MemoryRefs(raw[:3]))
	saved := contextstate.Checkpoint{Count: 3, Continue: true, Handoff: strings.Repeat("h", 6000), PagedResult: "source"}
	for _, paged := range []bool{false, true} {
		if _, fits, err := e.reuse(context.Background(), raw, 3, e.tag(), saved, paged, false); err != nil || fits != paged {
			t.Fatalf("paged checkpoint allowed %t: fits %t: %v", paged, fits, err)
		}
	}
}

// A preview makes the sizing decision the run would make. When only a
// measurement can tell whether the transcript fits, the preview says so.
func TestPreviewNeedsTheMeasurementTheRunWouldMake(t *testing.T) {
	c := newStoredConversation(t)
	c.exchange("q0", "a0 "+strings.Repeat("a", 9000), nil)
	c.toolTurn("Find the row.", "events", strings.Repeat("r", 6000))
	attempt := func(question string, preview bool) (*Engine, string, error) {
		e := testEngine(contextcfg.Handoff)
		e.Write = func(context.Context, string, string) (string, error) { return "HANDOFF", nil }
		e.CountText = func(_ context.Context, text string) (int, error) { return countQuestions(text), nil }
		refs, transcript := saved(c.refs, c.s)
		view, err := e.Project(c.ctx, append(slices.Clone(c.raw), core.NewTextMessage("user", question)), refs, transcript, preview)
		b, _ := json.Marshal(view)
		return e, string(b), err
	}
	if e, _, err := attempt("LONG "+strings.Repeat("Q", 5000), false); err != nil || e.current.Count == 0 {
		t.Fatal("the long question should need a checkpoint", err)
	}
	if e, view, err := attempt("short question", false); err != nil || e.current.Count != 0 || !strings.Contains(view, "a0 aaa") {
		t.Fatalf("the short question used checkpoint through %d: %v", e.current.Count, err)
	}
	if e, _, err := attempt("short question", true); err == nil || !strings.Contains(err.Error(), "measurement is required") {
		t.Fatalf("the preview used checkpoint through %d: %v", e.current.Count, err)
	}
}

// A recovery cannot start from a handoff that the writer's window cannot
// hold beside the records it adds. It writes the handoff from the original
// records.
func TestRecoveryRewritesAHandoffTooLargeForTheWriter(t *testing.T) {
	c := newStoredConversation(t)
	on := testEngine(contextcfg.Handoff)
	on.Binding.Window = 30000
	on.CanReadHistory = true
	c.exchange("FIRST-RAW "+strings.Repeat("a", 3000), "finished", nil)
	on.Write = func(context.Context, string, string) (string, error) {
		return "BIG-HANDOFF " + strings.Repeat("h", 18000), nil
	}
	if _, err := c.next(on); err != nil || on.current.Count != 2 {
		t.Fatal(on.current.Count, err)
	}
	c.exchange("continue", "MISSED-EVIDENCE "+strings.Repeat("b", 40000), on.Receipt())
	on.Write = func(context.Context, string, string) (string, error) {
		return "", errors.New("temporary writer failure")
	}
	if _, err := c.next(on); err != nil || !on.current.Fallback {
		t.Fatal(on.current, err)
	}
	c.exchange("continue", "short reply", on.Receipt())
	small := testEngine(contextcfg.Handoff)
	small.Binding.Window, small.Binding.Trigger = 4096, 0
	var sources strings.Builder
	small.Write = func(_ context.Context, _, source string) (string, error) {
		if strings.Contains(source, "BIG-HANDOFF") {
			return "", errors.New("input exceeds the context window")
		}
		sources.WriteString(source)
		return "REBUILT-HANDOFF", nil
	}
	view, err := c.next(small)
	b, _ := json.Marshal(view)
	if err != nil || small.current.Fallback || !strings.Contains(string(b), "REBUILT-HANDOFF") {
		t.Fatalf("fallback %t: %v", small.current.Fallback, err)
	}
	if !strings.Contains(sources.String(), "FIRST-RAW") || !strings.Contains(sources.String(), "MISSED-EVIDENCE") {
		t.Fatal("the writer did not read the original records")
	}
}

// A recovery that failed is not retried while the archive note still fits.
// A handoff that another run published for the same boundary and settings
// replaces the note without another writer call, and a preview shows what
// the run would send.
func TestDeferredRecoveryUsesAHandoffAnotherRunPublished(t *testing.T) {
	c := fallbackAfter(t, "short reply")
	refuse := func(context.Context, string, string) (string, error) {
		t.Fatal("called the writer")
		return "", nil
	}
	failing := testEngine(contextcfg.Handoff)
	failing.Write = func(context.Context, string, string) (string, error) {
		return "", errors.New("writer still failing")
	}
	if _, err := c.next(failing); err != nil || !failing.current.Fallback || failing.deferred == "" {
		t.Fatal(failing.current, err)
	}
	raw, refs := c.raw, c.refs
	c.exchange("continue", "answer under the archive note", failing.Receipt())
	preview := testEngine(contextcfg.Handoff)
	preview.Write = refuse
	previewRefs, previewTranscript := saved(c.refs, c.s)
	view, err := preview.Project(c.ctx, append(slices.Clone(c.raw), core.NewTextMessage("user", "continue")), previewRefs, previewTranscript, true)
	b, _ := json.Marshal(view)
	if err != nil || !strings.Contains(string(b), "history tools are unavailable now") {
		t.Fatal("preview of the deferred recovery:", err)
	}
	other := testEngine(contextcfg.Handoff)
	other.Write = func(context.Context, string, string) (string, error) { return "REBUILT-HANDOFF", nil }
	otherRefs, otherTranscript := saved(refs, nil)
	if _, err := other.Project(c.ctx, append(slices.Clone(raw), core.NewTextMessage("user", "continue")), otherRefs, otherTranscript, false); err != nil || other.current.Fallback {
		t.Fatal(other.current, err)
	}
	later := testEngine(contextcfg.Handoff)
	later.Write = refuse
	view, err = c.next(later)
	b, _ = json.Marshal(view)
	if err != nil || later.current.Fallback || !strings.Contains(string(b), "REBUILT-HANDOFF") {
		t.Fatalf("fallback %t: %v", later.current.Fallback, err)
	}
}

// saved returns the engine's refs and the transcript of a history read from
// a saved session.
func saved(refs []session.ContextRef, s *session.Session) ([]contextstate.Ref, Transcript) {
	return session.EngineRefs(refs), session.NewTranscript(refs, s)
}

// savedInMemory is a memory transcript that reports its history saved, for
// tests of what the engine reads from receipts.
type savedInMemory struct{ Transcript }

func (savedInMemory) Stored(int) bool { return true }

// A checkpoint's catalog goes with it while the notebook and view it came
// from are in force. After a switch to another notebook a request carries
// that notebook's catalog, and after a detach none. A receipt's choice to
// leave the first window's catalog out is made again for another notebook.
func TestCheckpointCatalogFollowsTheNotebookInForce(t *testing.T) {
	raw := []core.TypedMessage{core.NewTextMessage("user", "first"), core.NewTextMessage("assistant", "done"), core.NewTextMessage("user", "next")}
	refs := MemoryRefs(raw)
	for _, tc := range []struct {
		notebook, initial, want string
	}{
		{"notes-a", "CATALOG-B", "CATALOG-A"},
		{"notes-b", "CATALOG-B", "CATALOG-B"},
		{"", "", ""},
	} {
		e := testEngine(contextcfg.Handoff)
		e.Binding.Notebook, e.Binding.Initial = tc.notebook, tc.initial
		e.current = contextstate.Checkpoint{Count: 2, Prefix: contextstate.PrefixKeys(refs)[1], Handoff: "PRIOR-HANDOFF", Catalog: "CATALOG-A", Notebook: "notes-a", View: "catalog"}
		e.loaded = true
		visible, err := e.Project(context.Background(), raw, refs, MemoryTranscript(refs), false)
		b, _ := json.Marshal(visible)
		sent := string(b)
		if err != nil || !strings.Contains(sent, "PRIOR-HANDOFF") {
			t.Fatalf("notebook %q: %v", tc.notebook, err)
		}
		for _, catalog := range []string{"CATALOG-A", "CATALOG-B"} {
			if strings.Contains(sent, catalog) != (catalog == tc.want) {
				t.Errorf("notebook %q sent %s: %t", tc.notebook, catalog, strings.Contains(sent, catalog))
			}
		}
	}
	first := []core.TypedMessage{core.NewTextMessage("system", "sys"), core.NewTextMessage("user", "q1"), core.NewTextMessage("assistant", "a1"), core.NewTextMessage("user", "q2")}
	firstRefs := MemoryRefs(first)
	firstRefs[2].Role, firstRefs[2].Receipt = core.RoleAssistant, &contextstate.Receipt{CatalogOmitted: true, Notebook: "notes-a", View: "catalog"}
	e := testEngine(contextcfg.Handoff)
	e.Binding.Notebook, e.Binding.Initial = "notes-b", "CATALOG-B"
	visible, err := e.Project(context.Background(), first, firstRefs, savedInMemory{MemoryTranscript(firstRefs)}, false)
	b, _ := json.Marshal(visible)
	if err != nil || !strings.Contains(string(b), "CATALOG-B") || e.Receipt().CatalogOmitted {
		t.Fatalf("the choice made for notes-a held for notes-b: %v", err)
	}
}
