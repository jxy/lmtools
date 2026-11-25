package workingcontext

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/contextcfg"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"lmtools/internal/mcp"
	"lmtools/internal/notebook"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

//go:embed handoff.txt
var handoffPrompt string

//go:embed archive.txt
var archivePrompt string

type Writer func(context.Context, string, string) (string, error)

type Engine struct {
	Binding        contextcfg.Binding
	History        *History
	Write          Writer
	CountText      func(context.Context, string) (int, error)
	CountMessages  func(context.Context, []core.TypedMessage) (int, error)
	CanReadHistory bool
	counts         map[sizingKey]int
	Notice         func(string, ...any)
	ExtraTokens    int
	SchemaTokens   int // answer schema omitted from native sizing requests
	// Sizing identifies what a measured size depends on besides the
	// messages: the endpoint, the model and wire, the reasoning settings,
	// and the tool and answer schemas a request carries.
	Sizing        string
	WriterProfile string
	current       contextstate.Checkpoint
	lastWritten   contextstate.Checkpoint // writer base for a memory-only fallback
	loaded        bool
	deferred      string
	// sentInitial and omitInitial hold the notebook catalog choice of the
	// window that starts at the beginning of the branch, as its receipts
	// record it: a request of the window carried the catalog, or the window
	// leaves it out.
	sentInitial bool
	omitInitial bool
	noticed     map[string]bool
	// t is the transcript of the request being built.
	t Transcript
	// meterFailed is set when an admission refused a request because the
	// provider could not measure it. Such a decision is not recorded for
	// later attempts.
	meterFailed bool
}

func (e *Engine) tag() string {
	hash := sha256.Sum256([]byte(strings.Join([]string{
		"v3", e.WriterProfile, fmt.Sprint(e.CanReadHistory), fmt.Sprint(e.Binding.PhaseHints()),
		e.Binding.Policy, e.Binding.Notebook, e.Binding.NotebookView(),
		fmt.Sprint(e.Binding.Window), fmt.Sprint(e.Binding.Threshold()), e.writerKey(), archivePrompt,
	}, "\x00")))
	return hex.EncodeToString(hash[:12])
}

// BindExisting binds the history tools to a saved history before the first
// request of a turn is built, through the transcript t it is kept in.
func (e *Engine) BindExisting(ctx context.Context, messages []core.TypedMessage, refs []contextstate.Ref, t Transcript) error {
	if t == nil {
		return errors.New("context engine bound without a transcript")
	}
	e.t = t
	if !e.Binding.Compacts() && e.Binding.ManualCheckpoint == nil {
		e.History.Bind(messages, refs, 0, t)
		return nil
	}
	cp, err := e.checkpoint(ctx, messages, refs, transcriptKeys(t, len(refs)))
	if err != nil {
		return err
	}
	e.History.Bind(messages, refs, cp.Count, t)
	return nil
}

func (e *Engine) checkpoint(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, keys []string) (contextstate.Checkpoint, error) {
	cp := e.current
	if e.loaded && cp.Count <= len(refs) && (cp.Count == 0 || cp.Prefix == keys[cp.Count-1]) {
		return cp, nil
	}
	cp = contextstate.Checkpoint{}
	if len(refs) > 0 && e.t.Stored(len(refs)) {
		latest := e.readReceipt(refs)
		var err error
		cp, err = e.storedCheckpoint(ctx, raw, refs, keys, latest)
		if err != nil {
			return cp, err
		}
	}
	e.current = cp
	e.loaded = true
	return cp, nil
}

// Use the manual cut until a reply made under that selection records the
// actual window. With automatic cuts off, the manual selection stays in use.
func (e *Engine) storedCheckpoint(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, keys []string, latest *contextstate.Receipt) (contextstate.Checkpoint, error) {
	var cp contextstate.Checkpoint
	manual := e.Binding.ManualCheckpoint
	var used *contextcfg.CheckpointRef
	if latest != nil {
		used = latest.ManualCheckpoint
	}
	if manual != nil && (!e.Binding.Compacts() || used == nil || *used != *manual) {
		end := slices.Index(keys, manual.Prefix) + 1
		if end == 0 {
			return cp, errors.New("manual checkpoint belongs to a different history")
		}
		saved, ok, err := e.savedCheckpoint(ctx, raw, refs, keys, end, manual.Tag)
		if err == nil && !ok {
			err = errors.New("saved manual checkpoint is missing")
		}
		return saved, err
	}
	if !e.Binding.Compacts() {
		return cp, nil
	}
	cp, err := activeCheckpoint(ctx, refs, e.t, len(refs))
	if err != nil || cp.Count == 0 {
		return cp, err
	}
	pins, err := contextstate.ActivePins(raw[:cp.Count], refs[:cp.Count])
	if err != nil {
		return cp, err
	}
	if contextstate.PinDigest(pins) != cp.PinDigest {
		return cp, errors.New("saved checkpoint does not match its user pin events")
	}
	return cp, nil
}

// readReceipt restores what the latest reply's window recorded: a deferred
// checkpoint, and whether a window starting at the beginning of the branch
// carried the notebook catalog or left it out.
func (e *Engine) readReceipt(refs []contextstate.Ref) *contextstate.Receipt {
	for i := len(refs) - 1; i >= 0; i-- {
		if refs[i].Role == core.RoleAssistant {
			w := refs[i].Receipt
			if w != nil {
				e.deferred = w.Deferred
				sameCatalog := w.Notebook == e.Binding.Notebook && w.View == e.Binding.NotebookView()
				e.sentInitial = w.Count == 0 && w.CatalogSent && sameCatalog
				e.omitInitial = w.Count == 0 && w.CatalogOmitted && sameCatalog
			}
			return w
		}
	}
	return nil
}

// Image inputs consume visual tokens. Their compressed or base64 byte length
// can vary independently of the pixels a model sees. The provider determines
// the actual count; this allowance is used for planning on all supported wires.
const estimatedImageTokens = 2048

// Duration is optional in a stored audio block. These portable estimates
// apply equally to encoded data and references to the same recording.
const (
	estimatedAudioTokensPerSecond = 32
	estimatedUnknownAudioTokens   = 2048
)

func estimateAudio(duration int) int {
	if duration <= 0 {
		return estimatedUnknownAudioTokens
	}
	if duration > math.MaxInt/estimatedAudioTokensPerSecond {
		return math.MaxInt
	}
	return duration * estimatedAudioTokensPerSecond
}

func addEstimatedTokens(total, n int) int {
	if n > math.MaxInt-total {
		return math.MaxInt
	}
	return total + n
}

func size(msg core.TypedMessage) int {
	n := 0
	images := 0
	audioTokens := 0
	for _, b := range msg.Blocks {
		n += 12
		switch v := b.(type) {
		case core.TextBlock:
			n += len(v.Text)
		case core.ToolResultBlock:
			n += len(v.Content) + len(v.Name) + len(v.ToolUseID)
			images += len(v.Images)
		case core.ToolUseBlock:
			n += len(v.Name) + len(v.ID) + len(v.Input) + len(v.InputString)
		case core.ReasoningBlock:
			n += max(len(v.Raw), len(v.Text)+len(v.Summary)+len(v.Content)+len(v.Signature)+len(v.EncryptedContent))
		case core.ImageBlock:
			images++
		case core.AudioBlock:
			audioTokens = addEstimatedTokens(audioTokens, estimateAudio(v.Duration))
		default:
			raw, _ := json.Marshal(v)
			n += len(raw)
		}
	}
	return addEstimatedTokens(8+(n+1)/2+images*estimatedImageTokens, audioTokens)
}

func estimate(msgs []core.TypedMessage, extra int) int {
	for _, m := range msgs {
		extra = addEstimatedTokens(extra, size(m))
	}
	return extra
}

func hasCall(m core.TypedMessage) bool {
	for _, b := range m.Blocks {
		if _, ok := b.(core.ToolUseBlock); ok {
			return true
		}
	}
	return false
}

func resultMessage(m core.TypedMessage) bool {
	for _, b := range m.Blocks {
		if _, ok := b.(core.ToolResultBlock); ok {
			return true
		}
	}
	return false
}

func completedBatch(raw []core.TypedMessage, i int) bool {
	if i == 0 || raw[i].Role != "user" || raw[i-1].Role != "assistant" {
		return false
	}
	calls := map[string]bool{}
	for _, b := range raw[i-1].Blocks {
		if c, ok := b.(core.ToolUseBlock); ok {
			calls[c.ID] = true
		}
	}
	if len(calls) == 0 {
		return false
	}
	for _, b := range raw[i].Blocks {
		if r, ok := b.(core.ToolResultBlock); ok {
			if !calls[r.ToolUseID] {
				return false
			}
			delete(calls, r.ToolUseID)
		}
	}
	return len(calls) == 0
}

func cutPoint(raw []core.TypedMessage, from, end int) (int, bool) {
	for i := end - 1; i >= from; i-- {
		if raw[i].Role == "assistant" && !hasCall(raw[i]) {
			return i + 1, false
		}
		if completedBatch(raw, i) {
			return i + 1, true
		}
	}
	return from, false
}

// projection is this run's view of a checkpoint. The tools view leaves out a
// catalog that an earlier run saved with the checkpoint.
// projection is what a request sends under checkpoint cp. A checkpoint's
// catalog goes with it while the notebook and view it came from are in
// force; under another notebook or view the request carries that one's
// catalog, and none without a notebook.
func (e *Engine) projection(raw []core.TypedMessage, cp contextstate.Checkpoint) []core.TypedMessage {
	if cp.Count > 0 && (cp.Notebook != e.Binding.Notebook || cp.View != e.Binding.NotebookView()) {
		cp.Catalog = e.Binding.Initial
	}
	if e.Binding.NotebookView() == "tools" {
		cp.Catalog = ""
	}
	initial := e.Binding.Initial
	if e.omitInitial {
		initial = ""
	}
	return project(raw, cp, initial, e.CanReadHistory)
}

// noticeOnce gives each kind of notice at most once per turn.
func (e *Engine) noticeOnce(kind, format string, args ...any) {
	if e.Notice == nil || e.noticed[kind] {
		return
	}
	if e.noticed == nil {
		e.noticed = make(map[string]bool)
	}
	e.noticed[kind] = true
	e.Notice(format, args...)
}

// reportOmissions tells the operator what an admitted window cannot give the
// model while history reads are off.
func (e *Engine) reportOmissions(cp contextstate.Checkpoint) {
	if e.CanReadHistory {
		return
	}
	if cp.PagedResult != "" {
		e.noticeOnce("paged", "This window keeps only excerpts of a large tool result (%s); enable the history tools to let the model read all of it", cp.PagedResult)
	}
	if cp.Fallback {
		e.noticeOnce("fallback", "This window replaces messages 1–%d with an archive note, and the history tools are off, so the model cannot retrieve them", cp.Count)
	}
}

func project(raw []core.TypedMessage, cp contextstate.Checkpoint, initial string, canRead bool) []core.TypedMessage {
	if cp.Count == 0 && initial == "" {
		return raw
	}
	msgs := make([]core.TypedMessage, 0, len(raw)-cp.Count+4)
	offset := cp.Count
	if offset == 0 && len(raw) > 0 && raw[0].Role == "system" {
		offset = 1
	}
	user := -1
	for i, m := range raw[:offset] {
		switch m.Role {
		case "system", "developer":
			msgs = append(msgs, m)
		case "user":
			if !resultMessage(m) {
				user = i
			}
		}
	}
	background := initial
	if cp.Count > 0 {
		background = fmt.Sprintf("Handoff through message %d. Use the following historical evidence to continue the existing project.\n\n%s", cp.Count, cp.Handoff)
		if cp.Fallback && !canRead {
			background += "\n\nThe history tools are unavailable now, so the earlier exchanges cannot be retrieved."
		}
		if cp.Tail != "" {
			background += "\n\nRecent saved evidence:\n" + cp.Tail
		}
		if cp.Catalog != "" {
			background += "\n\n" + cp.Catalog
		}
		if cp.Interrupted != "" {
			background += "\n\n" + cp.Interrupted
		}
		if cp.Continue {
			background += "\n\nThe preceding tool batch already completed. Continue from its recorded outcomes."
		}
	}
	if background != "" {
		msgs = append(msgs, core.NewTextMessage("user", background))
	}
	if pins := contextstate.RenderPins(cp.Pins); pins != "" {
		msgs = append(msgs, core.NewTextMessage("user", pins))
	}
	if cp.Continue && user >= 0 {
		msgs = append(msgs, raw[user])
		// The model has not yet seen this batch when a cut happens just
		// after execution. Preserve the observed batch, with a source pointer
		// for individual results that were too large to deliver in this window.
		batch := core.TypedMessage{Role: "user", Blocks: []core.Block{core.TextBlock{Text: "Completed tool batch. Use these original results to continue the task."}}}
		for _, m := range raw[cp.Count-2 : cp.Count] {
			for index, block := range m.Blocks {
				switch b := block.(type) {
				case core.ToolUseBlock:
					input := string(b.Input)
					if b.InputString != "" {
						input = b.InputString
					}
					batch.Blocks = append(batch.Blocks, core.TextBlock{Text: fmt.Sprintf("Call %s (%s): %s", b.ID, b.Name, input)})
				case core.ToolResultBlock:
					text := b.Content
					if cp.PagedResult != "" && len(text) > 1024 && !IsHistoryTool(b.Name) {
						suffix := mcp.TextBoundary(text, len(text)-256)
						access := "Read by byte offset or use search when available."
						if !canRead {
							access = "History reads are unavailable now, so only these excerpts are in context."
						}
						text = fmt.Sprintf("Original history ref %s.%d, %d UTF-8 bytes. %s\nFirst bytes:\n%s\nLast bytes at offset %d:\n%s", cp.PagedResult, index, len(text), access, text[:mcp.TextBoundary(text, 512)], suffix, text[suffix:])
					}
					batch.Blocks = append(batch.Blocks, core.TextBlock{Text: fmt.Sprintf("Result %s (error=%t):\n%s", b.ToolUseID, b.IsError, text)})
					for _, im := range b.Images {
						batch.Blocks = append(batch.Blocks, im)
					}
				case core.TextBlock:
					batch.Blocks = append(batch.Blocks, b)
				}
			}
		}
		msgs = append(msgs, batch)
	}
	return append(msgs, raw[offset:]...)
}

// MemoryRefs identify the immutable copies in a no-session tool loop.
func MemoryRefs(raw []core.TypedMessage) []contextstate.Ref {
	r := make([]contextstate.Ref, len(raw))
	for i, m := range raw {
		h := sha256.New()
		fmt.Fprintf(h, "%d\x00%s\x00", i, m.Role)
		for _, b := range m.Blocks {
			fmt.Fprintf(h, "%T\x00", b)
			_ = json.NewEncoder(h).Encode(b)
		}
		r[i].Source = hex.EncodeToString(h.Sum(nil))
	}
	return r
}

// chunkSize is the most original record text one writer request carries.
// It leaves room for the previous handoff, output and dense tokenization.
func (e *Engine) chunkSize() int { return max(768, e.writerBudget().Window/2) }

// A manual cut can run without a known model capacity. Bound the writer's
// chunks and summary in that case without imposing a window on ordinary turns.
func (e *Engine) writerBudget() contextcfg.Settings {
	settings := e.Binding.Settings
	if settings.Window == 0 {
		settings.Window = 8192
	}
	return settings
}

func (e *Engine) lengthNote() string {
	return fmt.Sprintf("\nThe resulting handoff must fit in %d UTF-8 bytes.", e.handoffLimit())
}

func writerSource(previous, records string) string {
	return "Previous handoff:\n" + previous + "\n\nOriginal records:\n" + records
}

// priorHandoff is the text a handoff written after base starts from.
func priorHandoff(base contextstate.Checkpoint) string {
	if base.Interrupted != "" {
		return base.Handoff + "\n\n" + base.Interrupted
	}
	return base.Handoff
}

// writerTakes reports whether the input ceiling holds a writer request that
// starts from base: the writer's instructions, base's handoff, and a full
// chunk of original records.
func (e *Engine) writerTakes(base contextstate.Checkpoint) bool {
	request := []core.TypedMessage{
		core.NewTextMessage("system", handoffPrompt+e.lengthNote()),
		core.NewTextMessage("user", writerSource(priorHandoff(base), "")),
	}
	return estimate(request, (e.chunkSize()+1)/2) <= e.writerBudget().Ceiling()
}

func (e *Engine) summarize(ctx context.Context, previous string, raw []core.TypedMessage, refs []contextstate.Ref, begin, end int) (string, error) {
	limit := e.handoffLimit()
	chunkSize := e.chunkSize()
	var chunk strings.Builder
	flush := func(final bool) error {
		if chunk.Len() == 0 {
			return nil
		}
		draft, err := e.writeChunk(ctx, e.lengthNote(), writerSource(previous, chunk.String()))
		if err != nil {
			return err
		}
		if !final && len(draft) > limit {
			draft, err = e.shorten(ctx, draft, limit)
			if err != nil {
				return err
			}
		}
		if strings.TrimSpace(draft) == "" || (!final && len(draft) > limit) {
			return fmt.Errorf("checkpoint writer returned %d bytes (limit %d)", len(draft), limit)
		}
		previous = strings.TrimSpace(draft)
		chunk.Reset()
		return nil
	}
	for i := begin; i < end; i++ {
		if raw[i].Role == "system" || raw[i].Role == "developer" || refs[i].Pin != nil {
			continue
		}
		for _, r := range Records(raw[i], refs[i].Source, i) {
			hdr := fmt.Sprintf("\n[%s %s %s] ", r.Ref, r.Kind, r.Tool)
			content := r.Text
			if r.Capture != "" {
				content += "\n" + r.Capture
			}
			if len(hdr)+utf8.UTFMax > chunkSize {
				content = hdr + content
				hdr = "\n[continued record] "
			}
			for {
				if chunk.Len()+len(hdr)+4 >= chunkSize {
					if err := flush(false); err != nil {
						return "", err
					}
				}
				chunk.WriteString(hdr)
				n := mcp.TextBoundary(content, min(len(content), chunkSize-chunk.Len()))
				chunk.WriteString(content[:n])
				content = content[n:]
				if len(content) == 0 {
					break
				}
				if err := flush(false); err != nil {
					return "", err
				}
			}
		}
	}
	if err := flush(true); err != nil {
		return "", err
	}
	if previous == "" {
		previous = "Earlier exchanges contain no visible text."
	}
	return previous, nil
}

// shortTail retains literal, attributable evidence from the latest completed exchange.
func shortTail(raw []core.TypedMessage, refs []contextstate.Ref, end, limit int) string {
	var parts []string
	used := 0
	for i := end - 1; i >= 0 && used < limit; i-- {
		if refs[i].Pin != nil {
			continue
		}
		if raw[i].Role == "system" || raw[i].Role == "developer" {
			break
		}
		records := Records(raw[i], refs[i].Source, i)
		var part strings.Builder
		for _, r := range records {
			h := fmt.Sprintf("[%s %s %s] ", r.Ref, r.Kind, r.Tool)
			room := limit - used - part.Len() - len(h) - 5
			if room <= 0 {
				break
			}
			part.WriteString(h)
			n := mcp.TextBoundary(r.Text, min(room, len(r.Text)))
			part.WriteString(r.Text[:n])
			if n < len(r.Text) {
				part.WriteString("…")
			}
			part.WriteByte('\n')
		}
		parts = append(parts, part.String())
		used += part.Len()
		if raw[i].Role == "user" && !resultMessage(raw[i]) {
			break
		}
	}
	var b strings.Builder
	for i := len(parts) - 1; i >= 0; i-- {
		b.WriteString(parts[i])
	}
	return b.String()
}

// Receipt is committed with the assistant reply under this projection.
func (e *Engine) Receipt() *contextstate.Receipt {
	first := e.current.Count == 0
	r := &contextstate.Receipt{
		Count: e.current.Count, Prefix: e.current.Prefix, Tag: e.current.Tag, PinDigest: e.current.PinDigest, Deferred: e.deferred,
		ManualCheckpoint: e.Binding.ManualCheckpoint,
		CatalogSent:      first && e.sentInitial, CatalogOmitted: first && e.omitInitial,
		Notebook: e.Binding.Notebook, View: e.Binding.NotebookView(),
	}
	if c := e.Binding.Capacity; c.Tokens > 0 {
		r.Capacity = &c
	}
	return r
}

func (e *Engine) shorten(ctx context.Context, draft string, limit int) (string, error) {
	return e.Write(ctx, fmt.Sprintf("Shorten this session handoff. Keep distinct facts, current corrections, failures, source references and the next action. Remove repetition. Return only the replacement.\nThe resulting handoff must fit in %d UTF-8 bytes.", limit), draft)
}

func (e *Engine) handoffLimit() int { return min(32768, max(768, e.writerBudget().Threshold()/3)) }

func (e *Engine) newHandoff(ctx context.Context, previous contextstate.Checkpoint, raw []core.TypedMessage, refs []contextstate.Ref, end int, preview bool) (string, error) {
	if e.Binding.ArchiveOnly() {
		return strings.TrimSpace(archivePrompt), nil
	}
	if preview {
		return "", errors.New("an exact preview requires a new model-written checkpoint; resume normally to generate it")
	}
	return e.summarize(ctx, priorHandoff(previous), raw, refs, previous.Count, end)
}

func (e *Engine) writerBase(ctx context.Context, previous contextstate.Checkpoint, refs []contextstate.Ref, keys []string) (contextstate.Checkpoint, error) {
	for previous.Fallback {
		if !e.t.Stored(previous.Count) {
			if base := e.lastWritten; base.Count > 0 && base.Count <= previous.Count && base.Prefix == keys[base.Count-1] {
				return base, nil
			}
			return contextstate.Checkpoint{}, nil
		}
		// Walk actual ancestor receipts, including through successive fallbacks.
		// Each receipt covers strictly fewer messages than the current fallback.
		base, err := activeCheckpoint(ctx, refs, e.t, previous.Count)
		if err != nil {
			return contextstate.Checkpoint{}, err
		}
		previous = base
	}
	return previous, nil
}

func derivedCheckpointTag(settings string, cp contextstate.Checkpoint) string {
	data, _ := json.Marshal(cp)
	digest := sha256.Sum256(append([]byte(settings), data...))
	return hex.EncodeToString(digest[:12])
}

func exceedsBudget(err error) bool {
	var over budgetExceeded
	return errors.As(err, &over)
}

func pinBudgetError(err error, pins []contextstate.StandingPin) error {
	if !exceedsBudget(err) || len(pins) == 0 {
		return err
	}
	ids := make([]string, 0, len(pins))
	for _, p := range pins {
		ids = append(ids, p.ID)
	}
	return fmt.Errorf("protected request with standing pins %s: %w", strings.Join(ids, ", "), err)
}

func (e *Engine) fitCheckpoint(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, cp *contextstate.Checkpoint, preview bool) error {
	admit := func() error { return e.admit(ctx, e.projection(raw, *cp), preview) }
	err := admit()
	if exceedsBudget(err) && cp.Catalog != "" {
		cp.Catalog = ""
		err = admit()
	}
	if exceedsBudget(err) && cp.Tail != "" {
		cp.Tail = ""
		err = admit()
	}
	if exceedsBudget(err) && cp.Continue && e.CanReadHistory && cp.PagedResult == "" {
		cp.PagedResult = refs[cp.Count-1].Source
		err = admit()
		if err == nil && e.Notice != nil {
			e.Notice("Large tool result retained in history at %s; supplying its excerpt and offsets", cp.PagedResult)
		}
	}
	return err
}

// nextCheckpoint chooses the checkpoint for cut. A request that measured too
// large records the checkpoint it used, and a retry of that request takes
// the record before anything is measured or written. Runs of one request
// can record two checkpoints, one with its tool result whole and one with
// the result paged, and a retry prefers the whole one. A checkpoint chosen
// while the provider could not measure the request is not recorded.
func (e *Engine) nextCheckpoint(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, keys []string, previous contextstate.Checkpoint, cut checkpointCut, preview bool) (contextstate.Checkpoint, error) {
	if cut.overflow == "" || !e.t.Stored(cut.end) {
		return e.writeCheckpoint(ctx, raw, refs, keys, previous, cut, preview)
	}
	for _, paged := range []bool{false, true} {
		saved, ok, err := e.savedCheckpoint(ctx, raw, refs, keys, cut.end, e.overflowTag(cut.overflow, paged))
		if err != nil {
			return previous, err
		}
		if !ok {
			continue
		}
		if err = e.admit(ctx, e.projection(raw, saved), preview); err == nil {
			return saved, nil
		} else if !exceedsBudget(err) {
			return previous, err
		}
	}
	e.meterFailed = false
	cp, err := e.writeCheckpoint(ctx, raw, refs, keys, previous, cut, preview)
	if err != nil || preview || cp.Fallback || cp.Count != cut.end || e.meterFailed {
		return cp, err
	}
	if _, err = e.t.Publish(ctx, cut.end, e.overflowTag(cut.overflow, cp.PagedResult != ""), cp); err != nil {
		return previous, err
	}
	return cp, nil
}

// savedCheckpoint loads the checkpoint saved under tag at the boundary end
// and checks that it covers the instructions in force there.
func (e *Engine) savedCheckpoint(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, keys []string, end int, tag string) (contextstate.Checkpoint, bool, error) {
	saved, ok, err := e.t.Checkpoint(ctx, end, tag)
	if err != nil || !ok {
		return saved, false, err
	}
	pins, err := contextstate.ActivePins(raw[:end], refs[:end])
	if err != nil {
		return saved, false, err
	}
	if digest := contextstate.PinDigest(pins); saved.Count != end || saved.Prefix != keys[end-1] || saved.PinDigest != digest || contextstate.PinDigest(saved.Pins) != digest {
		return saved, false, errors.New("checkpoint candidate does not match the covered instructions")
	}
	return saved, true, nil
}

// reuse offers this request a checkpoint published for its boundary. A
// result that another request needed paged is offered whole first, and the
// paged checkpoint only when paged allows it.
func (e *Engine) reuse(ctx context.Context, raw []core.TypedMessage, end int, tag string, saved contextstate.Checkpoint, paged, preview bool) (contextstate.Checkpoint, bool, error) {
	if saved.PagedResult != "" {
		whole := saved
		whole.PagedResult = ""
		switch err := e.admit(ctx, e.projection(raw, whole), preview); {
		case err == nil && preview:
			return whole, true, nil
		case err == nil:
			whole, err = e.t.Publish(ctx, end, derivedCheckpointTag(tag, whole), whole)
			return whole, err == nil, err
		case !exceedsBudget(err):
			return saved, false, err
		case !paged:
			return saved, false, nil
		}
	}
	err := e.admit(ctx, e.projection(raw, saved), preview)
	if exceedsBudget(err) {
		return saved, false, nil
	}
	return saved, err == nil, err
}

func (e *Engine) writeCheckpoint(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, keys []string, previous contextstate.Checkpoint, cut checkpointCut, preview bool) (contextstate.Checkpoint, error) {
	end := cut.end
	stored := e.t.Stored(end)
	tag, prefix := e.tag(), keys[end-1]
	manual := cut.manualCatalog != nil
	before := 0
	if manual {
		// A handoff can revise notes while another branch compacts the
		// same exchanges. Reuse only a candidate with the same catalog.
		hash := sha256.Sum256([]byte("manual:" + tag + *cut.manualCatalog))
		tag = hex.EncodeToString(hash[:12])
		before = estimate(e.projection(raw, previous), e.ExtraTokens)
	}
	// Other runs can reach this boundary using a different starting
	// checkpoint. Every candidate for a manual cut must shorten this run.
	shortEnough := func(cp contextstate.Checkpoint) bool {
		return !manual || estimate(e.projection(raw, cp), e.ExtraTokens) < before
	}
	key := tag + ":" + prefix
	pins, err := contextstate.ActivePins(raw[:end], refs[:end])
	if err != nil {
		return previous, err
	}
	next := contextstate.Checkpoint{Tag: tag, Window: e.Binding.Window, Threshold: e.Binding.Threshold(), Prefix: prefix, Count: end, Continue: cut.mid, Pins: pins, PinDigest: contextstate.PinDigest(pins)}
	cached := false
	if stored {
		saved, ok, err := e.savedCheckpoint(ctx, raw, refs, keys, end, tag)
		if err != nil {
			return previous, err
		}
		if ok && shortEnough(saved) {
			cp, fits, err := e.reuse(ctx, raw, end, tag, saved, true, preview)
			if err != nil {
				return previous, err
			}
			if fits {
				return cp, nil
			}
			next, cached = saved, true
		}
	}
	replay := func() error { return e.admit(ctx, e.projection(raw, previous), preview) }
	if !cached {
		if !manual && e.deferred == key {
			if err := replay(); err == nil {
				return previous, nil
			} else if !exceedsBudget(err) {
				return previous, err
			}
		}
		if manual {
			next.Catalog = *cut.manualCatalog
		} else {
			next.Catalog, err = notebook.Snapshot(ctx, e.Binding.Notebook, e.Binding.View)
			if err != nil {
				return previous, err
			}
		}
		next.Notebook, next.View = e.Binding.Notebook, e.Binding.NotebookView()
		if !cut.mid && !manual {
			next.Tail = shortTail(raw, refs, end, min(8192, e.Binding.Threshold()/8))
		}
		if e.Write == nil && !preview && !e.Binding.ArchiveOnly() {
			return previous, errors.New("no checkpoint writer configured")
		}
		base, baseErr := e.writerBase(ctx, previous, refs, keys)
		if baseErr != nil {
			return previous, baseErr
		}
		if base.Count > 0 && !preview && !e.Binding.ArchiveOnly() && !e.writerTakes(base) {
			e.noticeOnce("records", "Writing the handoff from the original records: the handoff through message %d leaves the writer no room for them", base.Count)
			base = contextstate.Checkpoint{}
		}
		next.Interrupted = interruptedWork(raw, refs, base.Count, end)
		next.Handoff, err = e.newHandoff(ctx, base, raw, refs, end, preview)
	}
	limit := e.handoffLimit()
	if err == nil {
		err = e.fitCheckpoint(ctx, raw, refs, &next, preview)
		if exceedsBudget(err) && len(next.Handoff) > limit && e.Write != nil && !preview {
			next.Handoff, err = e.shorten(ctx, next.Handoff, limit)
			if err == nil && strings.TrimSpace(next.Handoff) == "" {
				err = errors.New("checkpoint writer returned an empty handoff")
			}
			if err == nil {
				err = e.fitCheckpoint(ctx, raw, refs, &next, false)
			}
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return previous, ctx.Err()
		}
		if manual || preview || credentialRefusal(err) || errors.Is(err, context.Canceled) {
			return previous, err
		}
		replayErr := replay()
		if replayErr == nil {
			e.deferred = key
			if e.Notice != nil {
				e.Notice("Checkpoint unavailable; original input fits: %v", err)
			}
			return previous, nil
		}
		if !exceedsBudget(replayErr) {
			return previous, replayErr
		}
		if !e.CanReadHistory || e.Binding.ArchiveOnly() {
			return previous, fmt.Errorf("write checkpoint: %w (original projection: %v)", pinBudgetError(err, pins), pinBudgetError(replayErr, pins))
		}
		if e.Notice != nil {
			e.Notice("Using archive fallback after checkpoint failure: %v", err)
		}
		next.Handoff = strings.TrimSpace(archivePrompt)
		next.Fallback = true
		if fitErr := e.fitCheckpoint(ctx, raw, refs, &next, false); fitErr != nil {
			return previous, fmt.Errorf("protected input after archive fallback: %w", pinBudgetError(fitErr, pins))
		}
	}
	e.deferred = ""
	if !shortEnough(next) {
		// Report the size, but leave this candidate unpublished so the
		// operator can retry a writer that produced an oversized summary.
		return next, nil
	}
	if stored && !preview {
		if !cached && !next.Fallback {
			winner, err := e.t.Publish(ctx, end, tag, next)
			if err != nil {
				return previous, err
			}
			// Another run may have published first. Its checkpoint serves
			// this request when it fits, its result whole if this request's
			// own handoff kept the result whole.
			if shortEnough(winner) {
				cp, fits, err := e.reuse(ctx, raw, end, tag, winner, next.PagedResult != "", false)
				if err != nil {
					return previous, err
				}
				if fits {
					return cp, nil
				}
			}
		}
		// A concurrent writer can have a smaller incoming request. Keep
		// its published candidate addressable by earlier receipts.
		tag = derivedCheckpointTag(tag, next)
		return e.t.Publish(ctx, end, tag, next)
	}
	return next, nil
}

type checkpointCut struct {
	end       int
	mid       bool
	requested bool
	// A manual cut snapshots the notebook after its handoff. An empty
	// snapshot is valid, including when the run has no notebook.
	manualCatalog *string
	// overflow identifies the request a cut during a tool turn measured too
	// large.
	overflow string
}

// dueCut selects a completed exchange. A tool result stays in its current
// window until the estimated ceiling is reached and the request cannot fit.
func (e *Engine) dueCut(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, keys []string, visible []core.TypedMessage, cp contextstate.Checkpoint, est int, preview bool) (checkpointCut, error) {
	committed := len(refs)
	requested := e.Binding.PhaseHints() && phaseRequest(raw[cp.Count:committed])
	atTrigger := est >= e.Binding.CutThreshold()
	if !atTrigger && !requested {
		return checkpointCut{}, nil
	}
	end, mid := cutPoint(raw, cp.Count, committed)
	hard := est >= e.Binding.Ceiling()
	if end == cp.Count || (mid && !hard) {
		return checkpointCut{}, nil
	}
	overflow := ""
	if mid {
		// A record at this boundary means an earlier attempt at this same
		// request measured it too large. Any other request is measured, in
		// a preview too, which says when only a live measurement can decide.
		// A request the provider could not measure is cut, and measured
		// again on its next attempt.
		overflow = e.requestDigest(visible)
		if !e.recordedOverflow(ctx, refs, keys, end, overflow) {
			if err := e.admit(ctx, visible, preview); !exceedsBudget(err) {
				return checkpointCut{}, err
			} else if errors.Is(err, errMeterUnavailable) {
				overflow = ""
			}
		}
	}
	progress := estimate(raw[cp.Count:end], 0)
	minimum := e.Binding.Threshold() / 3
	requested = requested && progress >= min(8192, minimum)
	if !requested && (!atTrigger || (cp.Count > 0 && !hard && !cp.Continue && progress < minimum)) {
		return checkpointCut{}, nil
	}
	return checkpointCut{end: end, mid: mid, requested: requested, overflow: overflow}, nil
}

// requestDigest identifies a request for sizing: what Sizing names, the
// tokens that tool schemas and an answer schema add, and the messages.
func (e *Engine) requestDigest(messages []core.TypedMessage) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00%s\x00%d\x00%d\x00", len(e.Sizing), e.Sizing, e.ExtraTokens, e.SchemaTokens)
	for _, m := range messages {
		fmt.Fprintf(h, "%s\x00%d\x00", m.Role, len(m.Blocks))
		for _, b := range m.Blocks {
			fmt.Fprintf(h, "%T\x00", b)
			_ = json.NewEncoder(h).Encode(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// overflowTag names the record of a checkpoint that a request used at a
// boundary after it measured too large, one for a checkpoint that pages its
// tool result and one for a checkpoint that keeps it whole.
func (e *Engine) overflowTag(request string, paged bool) string {
	kind := "whole"
	if paged {
		kind = "paged"
	}
	digest := sha256.Sum256([]byte(e.tag() + "\x00overflow\x00" + kind + "\x00" + request))
	return hex.EncodeToString(digest[:12])
}

// recordedOverflow reports whether request measured too large at the
// boundary end in an earlier attempt, which recorded the checkpoint it used.
func (e *Engine) recordedOverflow(ctx context.Context, refs []contextstate.Ref, keys []string, end int, request string) bool {
	if !e.t.Stored(end) {
		return false
	}
	for _, paged := range []bool{false, true} {
		if _, ok, err := e.t.Checkpoint(ctx, end, e.overflowTag(request, paged)); err == nil && ok {
			return true
		}
	}
	return false
}

// recoverHandoff replaces an archive fallback, whose covered exchanges only
// history reads reach, with a written handoff. It chooses where the handoff
// ends before any writer call. The handoff ends at the fallback's boundary
// unless a window with a handoff of the writer's byte limit there, and with
// any paged result whole, would already be due for a checkpoint. It then
// also covers the latest completed exchange, as that checkpoint would. A
// handoff another run published for that boundary serves at once. Without
// one, the fallback stays while it fits when an earlier attempt at the
// boundary failed.
func (e *Engine) recoverHandoff(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, keys []string, cp contextstate.Checkpoint, preview bool) (contextstate.Checkpoint, error) {
	written := cp
	written.Fallback, written.PagedResult, written.Handoff = false, "", ""
	est := addEstimatedTokens(estimate(e.projection(raw, written), e.ExtraTokens), (e.handoffLimit()+1)/2)
	cut := checkpointCut{end: cp.Count, mid: cp.Continue}
	if est >= e.Binding.CutThreshold() {
		if end, mid := cutPoint(raw, cp.Count, len(refs)); end > cp.Count && (!mid || est >= e.Binding.Ceiling()) {
			cut = checkpointCut{end: end, mid: mid}
		}
	}
	if e.deferred != e.tag()+":"+keys[cut.end-1] {
		e.noticeOnce("rebuild", "Rebuilding the handoff through message %d: its archive fallback relies on the history tools, which are off", cut.end)
	}
	return e.nextCheckpoint(ctx, raw, refs, keys, cp, cut, preview)
}

func (e *Engine) selectWindow(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, preview bool) ([]core.TypedMessage, contextstate.Checkpoint, error) {
	if !e.Binding.Compacts() && e.Binding.ManualCheckpoint == nil {
		if !e.loaded && len(refs) > 0 && e.t.Stored(len(refs)) {
			e.readReceipt(refs)
		}
		e.loaded = true
		cp := contextstate.Checkpoint{}
		return e.projection(raw, cp), cp, nil
	}
	keys := transcriptKeys(e.t, len(refs))
	cp, err := e.checkpoint(ctx, raw, refs, keys)
	if err != nil {
		return nil, cp, err
	}
	if cp.Continue && (cp.Count < 2 || !completedBatch(raw, cp.Count-1)) {
		return nil, cp, errors.New("saved checkpoint has an invalid tool boundary")
	}
	if cp.Fallback && !e.CanReadHistory && !e.Binding.ArchiveOnly() && e.t.Stored(cp.Count) {
		if cp, err = e.recoverHandoff(ctx, raw, refs, keys, cp, preview); err != nil {
			return nil, cp, err
		}
	}
	visible := e.projection(raw, cp)
	if !e.Binding.Compacts() {
		return visible, cp, nil
	}
	est := estimate(visible, e.ExtraTokens)
	// A receipt identifies the projection a previous reply actually saw.
	// Keep that prefix while it fits. If a changed budget can no longer
	// admit it with this input, choose a new checkpoint from the originals.
	// Other settings, such as the notebook view, keep the previous handoff
	// as the writer's starting point.
	if cp.Count > 0 && e.Binding.Window > 0 && est > e.Binding.Ceiling() && (cp.Window != e.Binding.Window || cp.Threshold != e.Binding.Threshold()) {
		if err := e.admit(ctx, visible, preview); exceedsBudget(err) {
			cp = contextstate.Checkpoint{}
			visible = e.projection(raw, cp)
			est = estimate(visible, e.ExtraTokens)
		} else if err != nil {
			return nil, cp, err
		}
	}
	cut, err := e.dueCut(ctx, raw, refs, keys, visible, cp, est, preview)
	if err != nil {
		return nil, cp, err
	}
	if cut.end > 0 {
		if e.Notice != nil {
			e.Notice("Writing %s checkpoint for messages %d–%d (estimated input %d, requested %t)", e.Binding.Policy, cp.Count+1, cut.end, est, cut.requested)
		}
		cp, err = e.nextCheckpoint(ctx, raw, refs, keys, cp, cut, preview)
		if err != nil {
			return nil, cp, err
		}
		visible = e.projection(raw, cp)
		if cp.Count == cut.end && e.Notice != nil {
			e.Notice("Context checkpoint through message %d (%d handoff bytes)", cp.Count, len(cp.Handoff))
		}
	}
	return visible, cp, nil
}

// Project uses the exact ancestor snapshot for this request, whose history
// is kept in transcript t. Each new window starts with its own evidence and
// leaves the old window's signed state behind.
func (e *Engine) Project(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, t Transcript, preview bool) ([]core.TypedMessage, error) {
	if t == nil {
		return nil, errors.New("context engine called without a transcript")
	}
	if len(refs) > len(raw) {
		return nil, errors.New("session provenance longer than transcript")
	}
	e.t = t
	visible, cp, err := e.selectWindow(ctx, raw, refs, preview)
	if err != nil {
		return nil, err
	}
	err = e.admit(ctx, visible, preview)
	catalog := cp.Count == 0 && e.Binding.Initial != ""
	if exceedsBudget(err) && catalog && !e.omitInitial && !e.sentInitial {
		// Nothing before this window could be cut and no request of the
		// window has carried its notebook catalog, so the catalog is the one
		// part it can leave out. Receipts keep the window's choice either
		// way, which keeps the prefix of its later requests stable.
		e.omitInitial = true
		trimmed := e.projection(raw, cp)
		if err = e.admit(ctx, trimmed, preview); err == nil {
			visible = trimmed
			e.noticeOnce("catalog", "Leaving the notebook catalog out of this window: it does not fit within the input ceiling of %d tokens", e.Binding.Ceiling())
		} else {
			e.omitInitial = false
		}
	}
	if err != nil {
		if exceedsBudget(err) {
			pins, pinErr := contextstate.ActivePins(raw, refs)
			if pinErr != nil {
				return nil, pinErr
			}
			err = pinBudgetError(err, pins)
		}
		return nil, err
	}
	if catalog && !e.omitInitial {
		e.sentInitial = true
	}
	e.reportOmissions(cp)
	e.current = cp
	if !cp.Fallback && !e.Binding.ArchiveOnly() {
		e.lastWritten = cp
	}
	e.History.Bind(raw, refs, cp.Count, t)
	if !preview {
		t.Used(*e.Receipt())
	}
	return visible, nil
}
