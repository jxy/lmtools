package retry

import (
	"context"
	"time"
)

// Progress describes the request attempt about to begin, or its preceding wait.
// It carries no request body, URL, or credentials.
type Progress struct {
	Attempt int
	Total   int
	Delay   time.Duration
}

type progressKey struct{}

func WithProgress(ctx context.Context, report func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, report)
}

func reportProgress(ctx context.Context, progress Progress) {
	if report, ok := ctx.Value(progressKey{}).(func(Progress)); ok && report != nil {
		report(progress)
	}
}
