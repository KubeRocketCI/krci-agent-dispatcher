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

// phase is a part of a turn; a failed reply names the phase that failed.
type phase string

const (
	phasePrepare phase = "prepare"
	phaseAgent   phase = "agent"
	phaseVerify  phase = "verify"
	phasePublish phase = "publish"
)

// outcome is the result of the work of one turn: the agent text and, for a failed turn, the phase and the exit code.
type outcome struct {
	text  string
	phase phase
	exit  int
}

// runTurn runs the work of next on issue under the watchdog deadline, limit after the poll that began at start.
func (h *Harness) runTurn(ctx context.Context, start time.Time, next turn.Turn, issue models.Issue) outcome {
	wctx, cancel := h.p.Clock.WithDeadline(ctx, start.Add(h.cfg.Watchdog))
	defer cancel()

	return h.work(wctx, next, issue)
}

// work runs the turn: prepare the workspace, run the agent, then read the default branch, verify and publish. A
// failure ends the turn; the agent's exit code other than 0 skips the publish phases. Once the agent ran with exit
// code 0, a failure in the later phases keeps its text.
func (h *Harness) work(ctx context.Context, next turn.Turn, issue models.Issue) outcome {
	if err := h.prepare(ctx); err != nil {
		return failed(ctx, phasePrepare, err)
	}

	text, rc, err := h.p.Agent.Run(ctx, prompt(next, issue))
	if err != nil {
		return failed(ctx, phaseAgent, err)
	}

	if rc != 0 {
		return outcome{text: text, phase: phaseAgent, exit: exitCode(ctx, nil, rc)}
	}

	if ph, err := h.publish(ctx); err != nil {
		o := failed(ctx, ph, err)
		o.text = text

		return o
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

// publish reads the default branch, verifies and publishes; on failure it returns the phase that failed.
func (h *Harness) publish(ctx context.Context) (phase, error) {
	branch, err := h.p.GitLab.DefaultBranch(ctx, h.cfg.Key.ProjectID)
	if err != nil {
		return phasePublish, err
	}

	if err := h.p.Workspace.Verify(ctx); err != nil {
		return phaseVerify, err
	}

	if err := h.p.Workspace.Publish(ctx, branch); err != nil {
		return phasePublish, err
	}

	return "", nil
}

// exitFailed is the exit code of a turn that failed without an agent exit code; exitWatchdog of a turn cut by the
// watchdog.
const (
	exitFailed   = 1
	exitWatchdog = 124
)

// failed is the outcome of a phase that returned err under ctx.
func failed(ctx context.Context, ph phase, err error) outcome {
	return outcome{phase: ph, exit: exitCode(ctx, err, exitFailed)}
}

// exitCode returns exit, or exitWatchdog when the watchdog cut the phase.
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
		parts = append(parts, fmt.Sprintf("Turn failed at %s, exit %d.", o.phase, o.exit))
	case len(parts) == 0:
		parts = append(parts, "Turn done.")
	}

	m := turn.Marker{Session: session, Turn: next.N, Exit: o.exit, Last: next.Last}

	return turn.Reply(strings.Join(parts, "\n\n"), m)
}

// prompt is the issue title and description for turn 1, then the comments of the turn.
func prompt(next turn.Turn, issue models.Issue) string {
	parts := make([]string, 0, len(next.Comments)+1)

	if next.N == 1 {
		parts = append(parts, strings.TrimSpace("Issue: "+issue.Title+"\n\n"+issue.Description))
	}

	for _, c := range next.Comments {
		parts = append(parts, c.Author+":\n"+c.Body)
	}

	return strings.Join(parts, "\n\n")
}
