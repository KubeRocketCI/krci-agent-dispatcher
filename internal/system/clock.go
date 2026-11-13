package system

import (
	"context"
	"time"
)

// Clock is the wall clock.
type Clock struct{}

func (Clock) Now() time.Time { return time.Now() }

// Sleep waits for d or the end of ctx and returns the error of ctx in that case.
func (Clock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (Clock) WithDeadline(ctx context.Context, t time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, t)
}
