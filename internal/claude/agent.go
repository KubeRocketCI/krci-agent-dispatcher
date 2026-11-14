// Package claude is the Claude Code adapter of the harness Agent port: one headless claude run per call, with the
// conversation of the session resumed across runs.
package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

const (
	defaultCommand  = "claude"
	defaultTermLead = 30 * time.Second
)

// Clock is time for the watchdog. WithDeadline returns a child of ctx that ends with context.DeadlineExceeded at t.
type Clock interface {
	Now() time.Time
	WithDeadline(ctx context.Context, t time.Time) (context.Context, context.CancelFunc)
}

// Options configure an Agent.
type Options struct {
	// Command is the claude executable, looked up in PATH when it has no slash. Default "claude".
	Command string
	// Dir is the working directory of a run. Empty: the working directory of the caller.
	Dir string
	// Model is the value of --model. Required.
	Model string
	// Steps is the value of --max-turns. Required, > 0.
	Steps int
	// SystemPrompt is the value of --append-system-prompt.
	SystemPrompt string
	// Session is the harness session, [0-9A-Za-z-]+. Required.
	Session string
	// StateDir holds the conversation file; created 0700 when missing. Required.
	StateDir string
	// Clock is required.
	Clock Clock
	// TermLead is the time between TERM and KILL. Default 30 s.
	TermLead time.Duration
	// Stderr receives the stderr of a run. Default: discarded.
	Stderr io.Writer
}

// Agent runs claude, one process group per Run, in Dir with the prompt on stdin and the environment inherited. Runs of
// one Agent must not overlap.
//
// The conversation id is a uuid v5 of Session. <StateDir>/conversation holds it from the moment a run's stream has
// the init event of that id; a run then resumes it (--resume), else starts it (--session-id). A failing resume is not
// retried as a new conversation.
type Agent struct {
	opts         Options
	conversation string
}

// New validates o and returns an Agent.
func New(o Options) (*Agent, error) {
	if o.Command == "" {
		o.Command = defaultCommand
	}

	if o.TermLead == 0 {
		o.TermLead = defaultTermLead
	}

	if o.Stderr == nil {
		o.Stderr = io.Discard
	}

	if err := validate(o); err != nil {
		return nil, err
	}

	return &Agent{opts: o, conversation: conversationID(o.Session)}, nil
}

func validate(o Options) error {
	switch {
	case o.Model == "":
		return errors.New("claude: model is required")
	case o.Steps <= 0:
		return errors.New("claude: steps must be > 0")
	case !models.ValidSessionID(o.Session):
		return errors.New("claude: session must match [0-9A-Za-z-]+")
	case o.StateDir == "":
		return errors.New("claude: state dir is required")
	case o.Clock == nil:
		return errors.New("claude: clock is required")
	case o.TermLead < 0:
		return errors.New("claude: term lead must be >= 0")
	}

	return nil
}

// conversationNamespace is the uuid v5 namespace of conversation ids.
var conversationNamespace = uuid.MustParse("6f1d2a8e-4c3b-4b7e-9a51-2d0c8e7f3b14")

// conversationID is the claude session id of the conversation of session.
func conversationID(session string) string {
	return uuid.NewSHA1(conversationNamespace, []byte(session)).String()
}

// Fresh reports whether the next Run starts a new conversation (--session-id): the conversation file does not hold the
// conversation id, or cannot be read.
func (a *Agent) Fresh() bool {
	resume, err := started(a.opts.StateDir, a.conversation)

	return err != nil || !resume
}

// Run runs claude once over prompt. It returns the result field of the last result event on stdout and the exit
// code, also a non-zero one. Errors: exit 0 without a result event, an end by a signal, a failed start, a failed
// conversation file read or write.
//
// Watchdog: TERM to the process group TermLead before the deadline of ctx, KILL at the deadline; on a cancelled ctx
// TERM, then KILL TermLead later. A run that got TERM returns an error that wraps the cause of the cut,
// context.DeadlineExceeded for the deadline. A run is not started past its TERM point. When the process exits, the
// rest of its process group is killed.
func (a *Agent) Run(ctx context.Context, prompt string) (string, int, error) {
	resume, err := started(a.opts.StateDir, a.conversation)
	if err != nil {
		return "", 0, fmt.Errorf("claude: %w", err)
	}

	term, cancel := a.termContext(ctx)
	defer cancel()

	if term.Err() != nil {
		return "", 0, fmt.Errorf("claude: agent not started: %w", context.Cause(term))
	}

	cmd := exec.Command(a.opts.Command, a.args(resume)...)
	cmd.Dir = a.opts.Dir

	var onInit func() error
	if !resume {
		onInit = func() error { return saveStarted(a.opts.StateDir, a.conversation) }
	}

	p, err := start(cmd, prompt, a.conversation, onInit, a.opts.Stderr, a.opts.Clock)
	if err != nil {
		return "", 0, fmt.Errorf("claude: %w", err)
	}

	cut := make(chan error, 1)

	go func() { cut <- a.watch(ctx, term, p) }()

	s, state, err := p.wait()

	text, rc, err := outcome(s, state, err, <-cut)
	if err = errors.Join(err, s.saved); err != nil {
		return "", 0, fmt.Errorf("claude: %w", err)
	}

	return text, rc, nil
}

// outcome maps the end of a run to the result of Run; state is nil when the wait failed with err, cut is the cause
// of a TERM sent by the watchdog.
func outcome(s stream, state *os.ProcessState, err, cut error) (string, int, error) {
	switch {
	case cut != nil:
		return "", 0, fmt.Errorf("agent run cut: %w", cut)
	case state == nil:
		return "", 0, err
	case !state.Exited():
		return "", 0, fmt.Errorf("agent ended: %s", state)
	case !s.result && state.ExitCode() == 0:
		return "", 0, errors.New("agent exited 0 without a result event")
	}

	return s.text, state.ExitCode(), nil
}

func (a *Agent) args(resume bool) []string {
	conv := "--session-id"
	if resume {
		conv = "--resume"
	}

	return []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", strconv.Itoa(a.opts.Steps),
		"--model", a.opts.Model,
		"--permission-mode", "bypassPermissions",
		"--disallowedTools", "Bash(git push:*)",
		"--append-system-prompt", a.opts.SystemPrompt,
		conv, a.conversation,
	}
}
