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

// outcome is the result of the work of one turn: the agent text and, for a failed turn, the phase, the exit code
// and the error.
type outcome struct {
	text  string
	phase phase
	exit  int
	err   error
}

// runTurn runs the work of next on issue under the watchdog deadline, limit after the poll that began at start.
func (h *Harness) runTurn(ctx context.Context, start time.Time, next turn.Turn, hist turn.History,
	issue models.Issue,
) outcome {
	wctx, cancel := h.p.Clock.WithDeadline(ctx, start.Add(h.cfg.Watchdog))
	defer cancel()

	return h.work(wctx, next, hist, issue)
}

// work runs the turn: prepare the workspace, run the agent, then publish. A failure ends the turn; the agent's exit
// code other than 0 skips publish. Once the agent ran with exit code 0, a failure in publish keeps its text.
func (h *Harness) work(ctx context.Context, next turn.Turn, hist turn.History, issue models.Issue) outcome {
	prep, err := h.prepare(ctx)
	if err != nil {
		return failed(ctx, phasePrepare, err)
	}

	text, rc, err := h.p.Agent.Run(ctx, prompt(next, hist, issue, h.p.Agent.Fresh(), prep))
	if err != nil {
		return failed(ctx, phaseAgent, err)
	}

	if rc != 0 {
		return outcome{text: text, phase: phaseAgent, exit: exitCode(ctx, nil, rc)}
	}

	if ph, err := h.publish(ctx, issue); err != nil {
		o := failed(ctx, ph, err)
		o.text = text

		return o
	}

	return outcome{text: text}
}

// prepare clones and sets up until that succeeded once in the session, then fetches.
func (h *Harness) prepare(ctx context.Context) (Prepared, error) {
	fresh := !h.started

	prep, err := h.p.Workspace.Prepare(ctx, fresh)
	if err != nil {
		return Prepared{}, err
	}

	if fresh {
		h.markStarted()
	}

	return prep, nil
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

// publish runs the new-commits check; with new commits it verifies and pushes the issue branch. A push leaves the
// merge request pending until it is ensured, in this turn or a later one; the flag is stored with the turn's reply.
// A merge request that failed after a push opens later; a merge request the person closed stays closed until the
// next push. On failure it returns the phase that failed.
func (h *Harness) publish(ctx context.Context, issue models.Issue) (phase, error) {
	st, err := h.p.Workspace.NewCommitsCheck(ctx)
	if err != nil {
		return phasePublish, err
	}

	if st.NewCommits {
		if err := h.p.Workspace.Verify(ctx); err != nil {
			return phaseVerify, err
		}

		if err := h.p.Workspace.Push(ctx); err != nil {
			return phasePublish, err
		}

		h.mrPending = true
	}

	if !h.mrPending {
		return "", nil
	}

	branch, err := h.p.GitLab.DefaultBranch(ctx, h.cfg.Key.ProjectID)
	if err != nil {
		return phasePublish, err
	}

	if err := h.p.GitLab.EnsureMergeRequest(ctx, h.cfg.Key.ProjectID, mergeRequest(h.cfg.Key, issue, branch)); err != nil {
		return phasePublish, err
	}

	h.mrPending = false

	return "", nil
}

// maxTitle is the GitLab limit of a merge request title, in characters.
const maxTitle = 255

// mergeRequest is the draft merge request of the issue branch into the default branch; it closes the issue on merge.
// The issue title is cut to fit maxTitle and then ends with "…".
func mergeRequest(key models.IssueKey, issue models.Issue, defaultBranch string) models.NewMergeRequest {
	const prefix, suffix = `Draft: Resolve "`, `"`

	title := []rune(issue.Title)
	if room := maxTitle - len([]rune(prefix+suffix)); len(title) > room {
		title = append(title[:room-1], '…')
	}

	return models.NewMergeRequest{
		Source:      key.IssueBranch(),
		Target:      defaultBranch,
		Title:       prefix + string(title) + suffix,
		Description: fmt.Sprintf("Closes #%d", key.IID),
	}
}

// exitFailed is the exit code of a turn that failed without an agent exit code; exitWatchdog of a turn cut by the
// watchdog.
const (
	exitFailed   = 1
	exitWatchdog = 124
)

// failed is the outcome of a phase that returned err under ctx.
func failed(ctx context.Context, ph phase, err error) outcome {
	return outcome{phase: ph, exit: exitCode(ctx, err, exitFailed), err: err}
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

// prompt is, for a new conversation, the issue title and description and every comment up to the turn's last; for a
// resumed one, the comments of the turn. What Prepare moved out of the way and the untracked paths are named last.
func prompt(next turn.Turn, hist turn.History, issue models.Issue, fresh bool, prep Prepared) string {
	comments := next.Comments
	parts := make([]string, 0, len(comments)+2)

	if fresh {
		parts = append(parts, strings.TrimSpace("Issue: "+issue.Title+"\n\n"+issue.Description))
		comments = hist.Comments(next.Last)
	}

	for _, c := range comments {
		parts = append(parts, c.Author+":\n"+c.Body)
	}

	if note := prepNote(prep); note != "" {
		parts = append(parts, note)
	}

	return strings.Join(parts, "\n\n")
}

// prepNote tells the agent what Prepare moved out of the way and the untracked paths it left, one line each; empty
// when nothing.
func prepNote(prep Prepared) string {
	var lines []string

	if len(prep.Aborted) > 0 {
		lines = append(lines, "The unfinished "+strings.Join(prep.Aborted, " and ")+" in the checkout was aborted.")
	}

	if prep.Stashed != "" {
		lines = append(lines, "Uncommitted changes to tracked files are in the stash entry "+prep.Stashed+
			"; git stash apply "+prep.Stashed+" restores them.")
	}

	if prep.SetAside != "" {
		lines = append(lines, "Your unpushed commits conflicted with the remote issue branch. They are on the local "+
			"branch "+prep.SetAside+"; the issue branch is at the remote state.")
	}

	if len(prep.Untracked) > 0 {
		lines = append(lines, "Untracked paths in the checkout: "+untrackedNames(prep.Untracked)+
			". Publish fails on untracked paths while the issue branch has unpushed commits; delete them, add them to "+
			".gitignore or commit them.")
	}

	if len(lines) == 0 {
		return ""
	}

	return "Harness: " + strings.Join(lines, "\n")
}

// maxUntrackedNamed is the most untracked paths the prompt names.
const maxUntrackedNamed = 5

// untrackedNames joins the first maxUntrackedNamed of paths with ", ", then "and <n> more" for the rest.
func untrackedNames(paths []string) string {
	if len(paths) <= maxUntrackedNamed {
		return strings.Join(paths, ", ")
	}

	return strings.Join(paths[:maxUntrackedNamed], ", ") + fmt.Sprintf(" and %d more", len(paths)-maxUntrackedNamed)
}
