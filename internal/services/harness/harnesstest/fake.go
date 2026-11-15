// Package harnesstest holds the in-memory adapters of the harness Agent and Workspace ports.
package harnesstest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
)

// BlockLimit bounds a blocked fake call in real time.
const BlockLimit = 5 * time.Second

// ErrNotCut is the error of a blocked fake call whose context did not end within BlockLimit.
var ErrNotCut = errors.New("call not cut by the watchdog")

// Await waits for ctx to end and returns its error, or ErrNotCut after BlockLimit: a call outside the watchdog fails
// the test instead of hanging it.
func Await(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(BlockLimit):
		return ErrNotCut
	}
}

// Agent records its prompts. OnTurn runs inside each run with the number of the call, from 1. Result decides the
// outcome of a run; the default is the text "agent run <call>", exit 0. NoInit reports the calls that end before the
// session starts, as a claude run without the init event; nil is none. Fresh is true until a run that NoInit does not
// report. Set the hooks before the first run. Safe for concurrent use.
type Agent struct {
	OnTurn func(call int)
	Result func(ctx context.Context, call int) (text string, exitCode int, err error)
	NoInit func(call int) bool

	mu      sync.Mutex
	log     []string
	started bool
}

// Fresh implements the harness Agent port.
func (a *Agent) Fresh() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return !a.started
}

// Run implements the harness Agent port.
func (a *Agent) Run(ctx context.Context, prompt string) (string, int, error) {
	a.mu.Lock()
	a.log = append(a.log, prompt)
	call := len(a.log)

	if a.NoInit == nil || !a.NoInit(call) {
		a.started = true
	}
	a.mu.Unlock()

	if a.OnTurn != nil {
		a.OnTurn(call)
	}

	if a.Result != nil {
		return a.Result(ctx, call)
	}

	return fmt.Sprintf("agent run %d", call), 0, nil
}

// Prompts returns the prompts of the runs so far.
func (a *Agent) Prompts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Clone(a.log)
}

// Workspace records its calls as "prepare fresh=<bool>", "new-commits check", "verify" and "push".
// NewCommitsCheck reports new commits unless NoNewCommits is set. A non-nil error field fails that call after it is
// recorded. A Block field makes that call run OnBlock, when set, then Await; the error field is not reached. A
// successful Prepare returns Prepared. Untracked are the untracked paths of the checkout: a successful Prepare that is
// not fresh returns them in Prepared.Untracked, and NewCommitsCheck with new commits fails on them. Change the fields
// only between calls. Safe for concurrent use.
type Workspace struct {
	PrepareErr, NewCommitsCheckErr, VerifyErr, PushErr error
	BlockPrepare, BlockNewCommitsCheck, BlockVerify    bool
	BlockPush                                          bool
	NoNewCommits                                       bool
	Prepared                                           harness.Prepared
	Untracked                                          []string
	OnBlock                                            func()

	mu     sync.Mutex
	log    []string
	notCut bool
}

func (w *Workspace) record(call string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.log = append(w.log, call)
}

// block runs OnBlock, then Await, when on.
func (w *Workspace) block(ctx context.Context, on bool) error {
	if !on {
		return nil
	}

	if w.OnBlock != nil {
		w.OnBlock()
	}

	err := Await(ctx)
	if errors.Is(err, ErrNotCut) {
		w.mu.Lock()
		w.notCut = true
		w.mu.Unlock()
	}

	return err
}

// NotCut reports whether a blocked call returned ErrNotCut.
func (w *Workspace) NotCut() bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.notCut
}

// Prepare implements the harness Workspace port.
func (w *Workspace) Prepare(ctx context.Context, fresh bool) (harness.Prepared, error) {
	w.record(fmt.Sprintf("prepare fresh=%t", fresh))

	if err := w.block(ctx, w.BlockPrepare); err != nil {
		return harness.Prepared{}, err
	}

	if w.PrepareErr != nil {
		return harness.Prepared{}, w.PrepareErr
	}

	prep := w.Prepared
	if !fresh && len(w.Untracked) > 0 {
		prep.Untracked = slices.Clone(w.Untracked)
	}

	return prep, nil
}

// NewCommitsCheck implements the harness Workspace port.
func (w *Workspace) NewCommitsCheck(ctx context.Context) (harness.BranchState, error) {
	w.record("new-commits check")

	if err := w.block(ctx, w.BlockNewCommitsCheck); err != nil {
		return harness.BranchState{}, err
	}

	if w.NewCommitsCheckErr != nil {
		return harness.BranchState{}, w.NewCommitsCheckErr
	}

	if !w.NoNewCommits && len(w.Untracked) > 0 {
		return harness.BranchState{}, fmt.Errorf("new-commits check: untracked files in %d paths", len(w.Untracked))
	}

	return harness.BranchState{NewCommits: !w.NoNewCommits}, nil
}

// Verify implements the harness Workspace port.
func (w *Workspace) Verify(ctx context.Context) error {
	w.record("verify")

	if err := w.block(ctx, w.BlockVerify); err != nil {
		return err
	}

	return w.VerifyErr
}

// Push implements the harness Workspace port.
func (w *Workspace) Push(ctx context.Context) error {
	w.record("push")

	if err := w.block(ctx, w.BlockPush); err != nil {
		return err
	}

	return w.PushErr
}

// Calls returns the calls so far.
func (w *Workspace) Calls() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return slices.Clone(w.log)
}
