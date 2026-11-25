package workingcontext

import (
	"context"
	"errors"
	"lmtools/internal/contextcfg"
	"lmtools/internal/contextstate"
	"lmtools/internal/core"
	"lmtools/internal/notebook"
)

// Compaction reports the estimated input size on either side of a manual
// cut. Checkpoint is set only when selecting it would shorten the request.
type Compaction struct {
	Checkpoint    *contextcfg.CheckpointRef
	Before, After int
}

type compactRange struct {
	previous contextstate.Checkpoint
	keys     []string
	end      int
}

// CompactBoundary reports how much saved history a manual cut would cover,
// or zero when no new exchange is complete. It never calls the model.
func (e *Engine) CompactBoundary(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, t Transcript) (int, error) {
	r, err := e.compactRange(ctx, raw, refs, t)
	return r.end, err
}

func (e *Engine) compactRange(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, t Transcript) (compactRange, error) {
	if err := ctx.Err(); err != nil {
		return compactRange{}, err
	}
	if t == nil || len(raw) != len(refs) {
		return compactRange{}, errors.New("manual compaction requires the saved transcript")
	}
	e.t = t
	keys := transcriptKeys(t, len(refs))
	previous, err := e.checkpoint(ctx, raw, refs, keys)
	if err != nil {
		return compactRange{}, err
	}
	// Leave pending questions, incomplete replies, and whole tool exchanges
	// after the last completed answer in their original form.
	end := len(refs)
	for end > previous.Count {
		ref := refs[end-1]
		if ref.Role == core.RoleAssistant && !ref.Incomplete && !hasCall(raw[end-1]) {
			break
		}
		end--
	}
	if end == previous.Count {
		return compactRange{}, nil
	}
	if !t.Stored(end) {
		return compactRange{}, errors.New("the exchange to compact has not been saved")
	}
	return compactRange{previous: previous, keys: keys, end: end}, nil
}

// Compact summarizes saved exchanges immediately, independently of the
// automatic trigger. The caller activates the result by recording it on
// its own branch after the writer succeeds.
func (e *Engine) Compact(ctx context.Context, raw []core.TypedMessage, refs []contextstate.Ref, t Transcript) (Compaction, error) {
	var result Compaction
	r, err := e.compactRange(ctx, raw, refs, t)
	if err != nil || r.end == 0 {
		return result, err
	}
	if e.Notice != nil {
		e.Notice("Compacting completed exchanges...")
	}
	catalog, err := notebook.Snapshot(ctx, e.Binding.Notebook, e.Binding.View)
	if err != nil {
		return result, err
	}
	next, err := e.writeCheckpoint(ctx, raw, refs, r.keys, r.previous, checkpointCut{end: r.end, manualCatalog: &catalog}, false)
	if err != nil {
		return result, err
	}
	result.Before = estimate(e.projection(raw, r.previous), e.ExtraTokens)
	result.After = estimate(e.projection(raw, next), e.ExtraTokens)
	if result.After < result.Before {
		result.Checkpoint = &contextcfg.CheckpointRef{Prefix: next.Prefix, Tag: next.Tag}
	}
	return result, nil
}
