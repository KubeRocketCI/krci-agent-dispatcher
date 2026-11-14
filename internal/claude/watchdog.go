package claude

import (
	"context"
	"syscall"
)

// termContext ends TermLead before the deadline of ctx, or with ctx when it has no deadline.
func (a *Agent) termContext(ctx context.Context) (context.Context, context.CancelFunc) {
	d, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}

	return a.opts.Clock.WithDeadline(ctx, d.Add(-a.opts.TermLead))
}

// watch sends TERM to the process group when term ends and KILL at the deadline of ctx or TermLead after TERM,
// whichever comes first. It returns the cause of term when it sent TERM, nil when the process exited before.
func (a *Agent) watch(ctx, term context.Context, p *process) error {
	select {
	case <-p.Exited():
		return nil
	case <-term.Done():
	}

	cause := context.Cause(term)

	p.Signal(syscall.SIGTERM)

	killAt := a.opts.Clock.Now().Add(a.opts.TermLead)
	if d, ok := ctx.Deadline(); ok && d.Before(killAt) {
		killAt = d
	}

	kill, cancel := a.opts.Clock.WithDeadline(context.WithoutCancel(ctx), killAt)
	defer cancel()

	select {
	case <-p.Exited():
	case <-kill.Done():
		p.Signal(syscall.SIGKILL)
	}

	return cause
}
