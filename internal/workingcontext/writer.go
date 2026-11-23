package workingcontext

import "context"

func (*Engine) writerKey() string { return handoffPrompt }

func (e *Engine) writeChunk(ctx context.Context, length, source string) (string, error) {
	return e.Write(ctx, handoffPrompt+length, source)
}
