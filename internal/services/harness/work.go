package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/turn"
)

// Turn steps named in a failed reply.
const (
	stepPrepare = "prepare"
	stepAgent   = "agent"
	stepVerify  = "verify"
	stepPublish = "publish"
)

// outcome is the result of the work of one turn: the agent text and, for a failed turn, the step and the exit code.
type outcome struct {
	text string
	step string
	exit int
}

// turn runs the work of next under the watchdog deadline, limit after the poll that began at start.
func (h *Harness) turn(ctx context.Context, start time.Time, next turn.Turn) outcome {
	wctx, cancel := h.p.Clock.WithDeadline(ctx, start.Add(h.cfg.Watchdog))
	defer cancel()

	return h.work(wctx, next)
}

// work runs the turn: prepare the workspace, run the agent, then read the default branch, verify and publish. A
// failure ends the turn; the agent's exit code other than 0 skips the publish steps.
func (h *Harness) work(ctx context.Context, next turn.Turn) outcome {
	if err := h.prepare(ctx); err != nil {
		return failed(ctx, stepPrepare, err)
	}

	text, rc, err := h.p.Agent.Run(ctx, prompt(next.Comments))
	if err != nil {
		return failed(ctx, stepAgent, err)
	}

	if rc != 0 {
		return outcome{text: text, step: stepAgent, exit: exitCode(ctx, nil, rc)}
	}

	if step, err := h.publish(ctx); err != nil {
		return failed(ctx, step, err)
	}

	return outcome{text: text}
}

// prepare clones and sets up until that succeeded once in the session, then fetches.
func (h *Harness) prepare(ctx context.Context) error {
	fresh := !h.started
	if err := h.p.Workspace.Prepare(ctx, fresh); err != nil {
		return err
	}

	if fresh {
		h.markStarted()
	}

	return nil
}

// markStarted records that clone and setup succeeded. A failed store write leaves the flag unset: the next turn
// clones and sets up again.
func (h *Harness) markStarted() {
	h.started = true

	if err := h.p.State.Save(h.cfg.Session, h.state()); err != nil {
		h.log.Error("Cannot save the session-started flag", "error", err)

		h.started = false
	}
}

// publish reads the default branch, verifies and publishes; on failure it returns the step that failed.
func (h *Harness) publish(ctx context.Context) (string, error) {
	branch, err := h.p.Notes.DefaultBranch(ctx, h.cfg.Key.ProjectID)
	if err != nil {
		return stepPublish, err
	}

	if err := h.p.Workspace.Verify(ctx); err != nil {
		return stepVerify, err
	}

	if err := h.p.Workspace.Publish(ctx, branch); err != nil {
		return stepPublish, err
	}

	return "", nil
}

// exitFailed is the exit code of a turn that failed without an agent exit code; exitWatchdog of a turn cut by the
// watchdog.
const (
	exitFailed   = 1
	exitWatchdog = 124
)

// failed is the outcome of a step that returned err under ctx.
func failed(ctx context.Context, step string, err error) outcome {
	return outcome{step: step, exit: exitCode(ctx, err, exitFailed)}
}

// exitCode returns exit, or exitWatchdog when the watchdog cut the step.
func exitCode(ctx context.Context, err error, exit int) int {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return exitWatchdog
	}

	return exit
}

// body returns the reply body of o for turn next: the agent text, a status line, the marker.
func (o outcome) body(session string, next turn.Turn) string {
	var parts []string

	if text := strings.TrimSpace(o.text); text != "" {
		parts = append(parts, text)
	}

	switch {
	case o.exit != 0:
		parts = append(parts, fmt.Sprintf("Turn failed at %s, exit %d.", o.step, o.exit))
	case len(parts) == 0:
		parts = append(parts, "Turn done.")
	}

	m := turn.Marker{Session: session, Turn: next.N, Exit: o.exit, Last: next.Last}

	return turn.Reply(strings.Join(parts, "\n\n"), m)
}

func prompt(comments []models.Note) string {
	parts := make([]string, 0, len(comments))
	for _, c := range comments {
		parts = append(parts, c.Author+":\n"+c.Body)
	}

	return strings.Join(parts, "\n\n")
}
