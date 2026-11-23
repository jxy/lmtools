package workingcontext

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"lmtools/internal/core"
	lmerrors "lmtools/internal/errors"
	"lmtools/internal/mcp"
	"net/http"
)

type budgetExceeded string

type sizingKey struct {
	native bool
	digest [sha256.Size]byte
}

func (b budgetExceeded) Error() string { return string(b) }

// errMeterUnavailable marks a refusal made because the provider could not
// measure the request. The request is refused on its estimate, which keeps
// it within the window, and a later attempt measures it again.
var errMeterUnavailable = errors.New("input measurement unavailable")

func credentialRefusal(err error) bool {
	var response *lmerrors.HTTPError
	return errors.As(err, &response) && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden)
}

// nativeSegments keeps each user request, its signed assistant messages and
// tool results together. Every probe repeats the leading instructions and
// tool schemas; their extra framing is included in the measured sum. Media
// and individual exchanges that exceed the input ceiling use field-level
// admission. A large exchange that still fits is measured intact on its own.
func nativeSegments(messages []core.TypedMessage, extra, ceiling int) [][]core.TypedMessage {
	prefixEnd := 0
	for prefixEnd < len(messages) && (messages[prefixEnd].Role == "system" || messages[prefixEnd].Role == "developer") {
		prefixEnd++
	}
	if prefixEnd == len(messages) || messages[prefixEnd].Role != "user" || resultMessage(messages[prefixEnd]) {
		return nil
	}
	prefix := messages[:prefixEnd]
	overhead := estimate(prefix, extra) + 128
	available := ceiling/2 - overhead
	if available <= 0 {
		return nil
	}
	// A boundary is the next user request after a finished assistant reply,
	// or the developer message -developer puts before that request.
	// Record these offsets before packing exchanges so a large turn can
	// fall back without issuing any partial sizing requests.
	starts := []int{prefixEnd}
	for i, m := range messages {
		for _, block := range m.Blocks {
			switch b := block.(type) {
			case core.TextBlock, core.ToolUseBlock, core.ReasoningBlock:
			case core.ToolResultBlock:
				if len(b.Images) != 0 {
					return nil
				}
			default:
				return nil
			}
		}
		if i < prefixEnd {
			continue
		}
		if hasCall(m) && (i+1 == len(messages) || !completedBatch(messages, i+1)) {
			return nil
		}
		if resultMessage(m) {
			if !completedBatch(messages, i) {
				return nil
			}
			continue
		}
		if i > prefixEnd && (m.Role == "user" || m.Role == "developer") && messages[i-1].Role == "assistant" && !hasCall(messages[i-1]) {
			starts = append(starts, i)
		}
	}
	starts = append(starts, len(messages))
	var groups [][]core.TypedMessage
	groupStart, groupCost := prefixEnd, 0
	for i, start := range starts[:len(starts)-1] {
		cost := estimate(messages[start:starts[i+1]], 0)
		if cost > ceiling-overhead {
			return nil
		}
		if groupCost > 0 && groupCost+cost > available {
			groups = append(groups, messages[groupStart:start])
			groupStart, groupCost = start, 0
		}
		groupCost += cost
	}
	if len(groups) == 0 {
		return nil
	}
	groups = append(groups, messages[groupStart:])
	for i, group := range groups {
		part := make([]core.TypedMessage, 0, len(prefix)+len(group)+1)
		part = append(part, prefix...)
		part = append(part, group...)
		groups[i] = append(part, core.NewTextMessage("user", "This request measures input size. Reply only OK without calling tools."))
	}
	return groups
}

func (e *Engine) measureNative(ctx context.Context, segments [][]core.TypedMessage, ceiling int) error {
	total := e.SchemaTokens
	for _, messages := range segments {
		// The engine is scoped to one turn and one provider configuration.
		// Cache only a digest of the typed input, including its native blocks.
		h := sha256.New()
		if err := json.NewEncoder(h).Encode(messages); err != nil {
			return err
		}
		key := sizingKey{native: true}
		copy(key.digest[:], h.Sum(nil))
		count, ok := e.counts[key]
		if !ok {
			if e.Notice != nil {
				e.Notice("Measuring a bounded native segment of %d messages", len(messages))
			}
			var err error
			count, err = e.CountMessages(ctx, messages)
			if err != nil {
				return err
			}
			if count <= 0 {
				return fmt.Errorf("provider supplied no input count for native segment")
			}
			e.rememberCount(key, count)
		}
		if count > ceiling-total {
			return budgetExceeded(fmt.Sprintf("measured native segments exceed the input ceiling of %d tokens", ceiling))
		}
		total += count
	}
	return nil
}

func (e *Engine) rememberCount(key sizingKey, count int) {
	if e.counts == nil {
		e.counts = make(map[sizingKey]int)
	}
	if len(e.counts) < 256 {
		e.counts[key] = count
	}
}

// Admit small projections without a probe. For a large multi-turn projection,
// measure complete exchanges in their native provider representation. A single
// oversized exchange can still have its large public text fields measured in
// pieces. Opaque reasoning and media never enter a plain-text probe.
func (e *Engine) admit(ctx context.Context, messages []core.TypedMessage, preview bool) error {
	err := e.measure(ctx, messages, preview)
	if errors.Is(err, errMeterUnavailable) {
		e.meterFailed = true
	}
	return err
}

func (e *Engine) measure(ctx context.Context, messages []core.TypedMessage, preview bool) error {
	if e.Binding.Window == 0 {
		return nil
	}
	ceiling := e.Binding.Ceiling()
	guess := estimate(messages, e.ExtraTokens)
	if guess <= ceiling {
		return nil
	}
	if e.CountMessages != nil {
		if segments := nativeSegments(messages, e.ExtraTokens, ceiling); len(segments) > 0 {
			if preview {
				return fmt.Errorf("size estimate %d exceeds ceiling %d; a live native input measurement is required before sending", guess, ceiling)
			}
			err := e.measureNative(ctx, segments, ceiling)
			if _, over := err.(budgetExceeded); err == nil || over {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Text probes use the same credential. Preserve the provider's
			// refusal so the caller can renew access and retry this input.
			if credentialRefusal(err) || errors.Is(err, context.Canceled) {
				return fmt.Errorf("measure input with provider: %w", err)
			}
			if e.Notice != nil {
				e.Notice("Native input measurement unavailable; using public text sizes: %v", err)
			}
			// Public text sizes can refuse a request its native count
			// admits.
			nativeErr := err
			err = e.measureText(ctx, messages, guess, ceiling, preview)
			if exceedsBudget(err) && !errors.Is(err, errMeterUnavailable) {
				err = fmt.Errorf("%w (%w: native count: %v)", err, errMeterUnavailable, nativeErr)
			}
			return err
		}
	}
	return e.measureText(ctx, messages, guess, ceiling, preview)
}

// measureText measures the large text fields of messages, whose estimate
// is guess, one piece at a time, and estimates the rest.
func (e *Engine) measureText(ctx context.Context, messages []core.TypedMessage, guess, ceiling int, preview bool) error {
	refusal := func(n int) error {
		return budgetExceeded(fmt.Sprintf("request estimated at %d input tokens; ceiling %d for -context-window %d", n, ceiling, e.Binding.Window))
	}
	if e.CountText == nil {
		return refusal(guess)
	}
	var texts []string
	remainder := guess
	add := func(text string) {
		if len(text) < 2048 {
			return
		}
		texts = append(texts, text)
		remainder -= len(text) / 2
	}
	for _, m := range messages {
		if m.Role == "system" || m.Role == "developer" {
			continue
		}
		for _, b := range m.Blocks {
			switch v := b.(type) {
			case core.TextBlock:
				add(v.Text)
			case core.ToolResultBlock:
				add(v.Content)
			}
		}
	}
	if len(texts) == 0 || remainder > ceiling {
		return refusal(guess)
	}
	if preview {
		return fmt.Errorf("size estimate %d exceeds ceiling %d; a live text measurement is required before sending", guess, ceiling)
	}
	maxPiece := min(512<<10, max(512, e.Binding.Window/2))
	observed := remainder
	for _, text := range texts {
		for len(text) > 0 {
			n := mcp.TextBoundary(text, min(len(text), maxPiece))
			part := text[:n]
			text = text[n:]
			key := sizingKey{digest: sha256.Sum256([]byte(part))}
			count, ok := e.counts[key]
			if !ok {
				if e.Notice != nil {
					e.Notice("Measuring %d bytes of protected text using provider input usage", len(part))
				}
				var err error
				count, err = e.CountText(ctx, part)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if credentialRefusal(err) || errors.Is(err, context.Canceled) {
					return fmt.Errorf("measure protected text: %w", err)
				}
				if err != nil || count <= 0 {
					if e.Notice != nil {
						e.Notice("Protected text measurement unavailable (count %d, error %v); using the input estimate", count, err)
					}
					if err != nil {
						return fmt.Errorf("%w (%w: %v)", refusal(guess), errMeterUnavailable, err)
					}
					return fmt.Errorf("%w (%w: the provider reported no count)", refusal(guess), errMeterUnavailable)
				}
				e.rememberCount(key, count)
			}
			if count > ceiling-observed {
				return budgetExceeded(fmt.Sprintf("measured text and request overhead exceed the input ceiling of %d tokens", ceiling))
			}
			observed += count
		}
	}
	return nil
}
