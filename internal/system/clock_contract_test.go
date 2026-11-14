package system_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
)

type clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
	WithDeadline(ctx context.Context, t time.Time) (context.Context, context.CancelFunc)
}

const (
	tick     = 20 * time.Millisecond
	settleBy = 2 * time.Second
)

func clocks() map[string]func() clock {
	return map[string]func() clock{
		"fake": func() clock { return systemtest.NewClock(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)) },
		"os":   func() clock { return system.Clock{} },
	}
}

func TestClockContract(t *testing.T) {
	cases := map[string]func(t *testing.T, c clock){
		"NowAdvancesWithSleep":               nowAdvancesWithSleep,
		"SleepReturnsCtxErrWhenCancelled":    sleepReturnsCtxErrWhenCancelled,
		"WithDeadlineReportsDeadline":        withDeadlineReportsDeadline,
		"WithDeadlineEndsPastDeadline":       withDeadlineEndsPastDeadline,
		"WithDeadlineHoldsBeforeDeadline":    withDeadlineHoldsBeforeDeadline,
		"WithDeadlineCancelReleases":         withDeadlineCancelReleases,
		"WithDeadlineInThePastEndsAtOnce":    withDeadlineInThePastEndsAtOnce,
		"WithDeadlineKeepsParentCancelCause": withDeadlineKeepsParentCancel,
	}
	for name, newClock := range clocks() {
		for c, run := range cases {
			t.Run(name+"/"+c, func(t *testing.T) { run(t, newClock()) })
		}
	}
}

func awaitDone(t *testing.T, ctx context.Context) {
	t.Helper()

	select {
	case <-ctx.Done():
	case <-time.After(settleBy):
		t.Fatal("context not ended")
	}
}

func nowAdvancesWithSleep(t *testing.T, c clock) {
	before := c.Now()

	if err := c.Sleep(t.Context(), tick); err != nil {
		t.Fatalf("Sleep: %v", err)
	}

	if got := c.Now().Sub(before); got < tick {
		t.Fatalf("Now advanced by %v, want at least %v", got, tick)
	}
}

func sleepReturnsCtxErrWhenCancelled(t *testing.T, c clock) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := c.Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sleep on a cancelled ctx: %v, want context.Canceled", err)
	}
}

func withDeadlineReportsDeadline(t *testing.T, c clock) {
	at := c.Now().Add(time.Hour)

	ctx, cancel := c.WithDeadline(t.Context(), at)
	defer cancel()

	if got, ok := ctx.Deadline(); !ok || !got.Equal(at) {
		t.Fatalf("Deadline = %v, %v, want %v", got, ok, at)
	}
}

func withDeadlineEndsPastDeadline(t *testing.T, c clock) {
	ctx, cancel := c.WithDeadline(t.Context(), c.Now().Add(tick))
	defer cancel()

	if err := c.Sleep(t.Context(), 2*tick); err != nil {
		t.Fatalf("Sleep: %v", err)
	}

	awaitDone(t, ctx)

	if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Err = %v, want context.DeadlineExceeded", err)
	}
}

func withDeadlineHoldsBeforeDeadline(t *testing.T, c clock) {
	ctx, cancel := c.WithDeadline(t.Context(), c.Now().Add(time.Hour))
	defer cancel()

	if err := c.Sleep(t.Context(), tick); err != nil {
		t.Fatalf("Sleep: %v", err)
	}

	if err := ctx.Err(); err != nil {
		t.Fatalf("Err before the deadline = %v, want nil", err)
	}
}

func withDeadlineCancelReleases(t *testing.T, c clock) {
	ctx, cancel := c.WithDeadline(t.Context(), c.Now().Add(time.Hour))
	cancel()

	awaitDone(t, ctx)

	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Err after cancel = %v, want context.Canceled", err)
	}
}

func withDeadlineInThePastEndsAtOnce(t *testing.T, c clock) {
	ctx, cancel := c.WithDeadline(t.Context(), c.Now().Add(-time.Second))
	defer cancel()

	awaitDone(t, ctx)

	if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Err = %v, want context.DeadlineExceeded", err)
	}
}

func withDeadlineKeepsParentCancel(t *testing.T, c clock) {
	parent, stop := context.WithCancel(t.Context())

	ctx, cancel := c.WithDeadline(parent, c.Now().Add(time.Hour))
	defer cancel()

	stop()
	awaitDone(t, ctx)

	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Err after parent cancel = %v, want context.Canceled", err)
	}
}
