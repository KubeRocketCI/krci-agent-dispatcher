package systemtest

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// Clock is virtual time: Sleep and Advance move it and return at once. OnSleep runs after each Sleep. A context
// from WithDeadline ends with context.DeadlineExceeded when virtual time reaches its deadline. Safe for
// concurrent use.
type Clock struct {
	mu      sync.Mutex
	now     time.Time
	sleeps  []time.Duration
	onSleep func(d time.Duration)
	timers  []*timer
}

type timer struct {
	at     time.Time
	cancel context.CancelCauseFunc
}

// NewClock returns a clock at now.
func NewClock(now time.Time) *Clock { return &Clock{now: now} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *Clock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	onSleep := c.onSleep
	c.mu.Unlock()

	c.Advance(d)

	if onSleep != nil {
		onSleep(d)
	}

	time.Sleep(time.Microsecond)

	return ctx.Err()
}

// Advance moves virtual time by d and ends the contexts whose deadline is reached.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)

	var due []*timer

	c.timers = slices.DeleteFunc(c.timers, func(t *timer) bool {
		if t.at.After(c.now) {
			return false
		}

		due = append(due, t)

		return true
	})
	c.mu.Unlock()

	for _, t := range due {
		t.cancel(context.DeadlineExceeded)
	}
}

// deadlineCtx reports the virtual deadline and context.DeadlineExceeded once the clock reached it.
type deadlineCtx struct {
	context.Context
	at time.Time
}

func (d deadlineCtx) Deadline() (time.Time, bool) { return d.at, true }

func (d deadlineCtx) Err() error {
	err := d.Context.Err()
	if err != nil && errors.Is(context.Cause(d.Context), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}

	return err
}

// WithDeadline returns a child of parent that ends with context.DeadlineExceeded when virtual time reaches at.
func (c *Clock) WithDeadline(parent context.Context, at time.Time) (context.Context, context.CancelFunc) {
	inner, cancel := context.WithCancelCause(parent)
	t := &timer{at: at, cancel: cancel}

	c.mu.Lock()
	reached := !at.After(c.now)

	if !reached {
		c.timers = append(c.timers, t)
	}
	c.mu.Unlock()

	if reached {
		cancel(context.DeadlineExceeded)
	}

	return deadlineCtx{Context: inner, at: at}, func() {
		cancel(context.Canceled)
		c.mu.Lock()
		c.timers = slices.DeleteFunc(c.timers, func(x *timer) bool { return x == t })
		c.mu.Unlock()
	}
}

// SetOnSleep sets the function Sleep calls after it moved time; nil clears it.
func (c *Clock) SetOnSleep(fn func(d time.Duration)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.onSleep = fn
}

// Count returns how many Sleep calls asked for d.
func (c *Clock) Count(d time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(slices.DeleteFunc(slices.Clone(c.sleeps), func(s time.Duration) bool { return s != d }))
}
