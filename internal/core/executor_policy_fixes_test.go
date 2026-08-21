package core

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

// A timeout is a number the model chooses, and time.Duration counts nanoseconds
// in an int64, so the multiply that turns seconds into a duration overflows
// above roughly 9.2e9. The deadline then lands in the past and the command is
// reported as having timed out before it started. The wrap is not detectable
// from the product either — 20000000000 seconds comes back positive — so the
// bound has to be on the seconds.
func TestCommandTimeoutNeverProducesANonPositiveDuration(t *testing.T) {
	const fallback = 90 * time.Second

	t.Run("in range and sentinel values pass through", func(t *testing.T) {
		for _, tt := range []struct {
			seconds int
			want    time.Duration
		}{
			{seconds: 0, want: fallback},
			{seconds: -1, want: fallback},
			{seconds: math.MinInt32, want: fallback},
			{seconds: 1, want: time.Second},
			{seconds: 5, want: 5 * time.Second},
			{seconds: MaxCommandTimeoutSeconds, want: MaxCommandTimeoutSeconds * time.Second},
		} {
			if got := commandTimeout(tt.seconds, fallback); got != tt.want {
				t.Errorf("commandTimeout(%d) = %v, want %v", tt.seconds, got, tt.want)
			}
		}
	})

	t.Run("out of range values clamp", func(t *testing.T) {
		// Written as int64 and converted at run time: these do not fit in a
		// 32-bit int, and a constant that does not fit is a compile error.
		overflowing := []int64{
			MaxCommandTimeoutSeconds + 1,
			math.MaxInt64/int64(time.Second) + 1, // first value that wraps
			9999999999,                           // wraps negative
			17179869184,                          // wraps negative
			20000000000,                          // wraps back positive, still wrong
			math.MaxInt64,
		}
		for _, seconds := range overflowing {
			got := commandTimeout(int(seconds), fallback)
			if got <= 0 {
				t.Errorf("commandTimeout(%d) = %v, want a positive duration", seconds, got)
			}
			if got != MaxCommandTimeoutSeconds*time.Second {
				t.Errorf("commandTimeout(%d) = %v, want the %v ceiling",
					seconds, got, MaxCommandTimeoutSeconds*time.Second)
			}
		}
	})
}

// The overflow was reachable straight from a tool call: the context was born
// expired, so the process never started and the model was told
// "command timed out after -2346317h47m53.709551616s".
func TestUnboundedTimeoutRunsTheCommandInsteadOfExpiringInstantly(t *testing.T) {
	var unbounded int64 = 10000000000

	executor := newStdioTestExecutor(t, 0, nil)

	result := executor.ExecuteParallel(context.Background(), []ToolCall{{
		ID:   "unbounded-timeout",
		Name: UniversalCommandToolName,
		Args: marshalCommandArgs(t, UniversalCommandArgs{
			Command: []string{"/bin/echo", "hi"},
			Timeout: int(unbounded),
		}),
	}}, nil)[0]

	if result.Error != "" || result.Code != "" {
		t.Fatalf("result = %#v, want the command to have run", result)
	}
	if got := strings.TrimSpace(result.Output); got != "hi" {
		t.Fatalf("output = %q, want %q", got, "hi")
	}
}

func TestEffectiveMaxParallelFloorsAtOne(t *testing.T) {
	for _, tt := range []struct{ maxParallel, want int }{
		{maxParallel: math.MinInt32, want: 1},
		{maxParallel: -1, want: 1},
		{maxParallel: 0, want: 1},
		{maxParallel: 1, want: 1},
		{maxParallel: DefaultMaxToolParallel, want: DefaultMaxToolParallel},
	} {
		if got := effectiveMaxParallel(tt.maxParallel); got != tt.want {
			t.Errorf("effectiveMaxParallel(%d) = %d, want %d", tt.maxParallel, got, tt.want)
		}
	}
}

// Zero workers is not a serial batch, it is a permanent one: the jobs channel is
// unbuffered, so the first send has nobody to hand the job to. NewExecutor
// cannot produce a non-positive maxParallel today, which is exactly why nothing
// else stands between a policy assembled by hand and a turn that never returns.
func TestExecuteParallelWithNonPositiveMaxParallelStillRuns(t *testing.T) {
	for name, maxParallel := range map[string]int{"zero": 0, "negative": -4} {
		t.Run(name, func(t *testing.T) {
			executor := &Executor{
				defaultTimeout: 5 * time.Second,
				maxOutputSize:  DefaultMaxOutputSize,
				maxParallel:    maxParallel,
				policy:         approvalPolicy{autoApprove: true},
			}
			calls := []ToolCall{
				{ID: "a", Name: UniversalCommandToolName, Args: marshalCommandArgs(t, UniversalCommandArgs{
					Command: []string{"/bin/echo", "alpha"}, Timeout: 5,
				})},
				{ID: "b", Name: UniversalCommandToolName, Args: marshalCommandArgs(t, UniversalCommandArgs{
					Command: []string{"/bin/echo", "beta"}, Timeout: 5,
				})},
			}

			// The regression is a deadlock, so the failure has to be a timeout
			// rather than a hung test binary.
			done := make(chan []ToolResult, 1)
			go func() { done <- executor.ExecuteParallel(context.Background(), calls, nil) }()

			select {
			case results := <-done:
				for i, want := range []string{"alpha", "beta"} {
					if results[i].Error != "" {
						t.Fatalf("results[%d] = %#v, want success", i, results[i])
					}
					if got := strings.TrimSpace(results[i].Output); got != want {
						t.Fatalf("results[%d] output = %q, want %q", i, got, want)
					}
				}
			case <-time.After(15 * time.Second):
				t.Fatalf("ExecuteParallel did not return with maxParallel=%d: no workers were started and the job send blocked forever",
					maxParallel)
			}
		})
	}
}
