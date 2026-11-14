// Package harnesstest holds the in-memory adapters of the harness Agent and Workspace ports.
package harnesstest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
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
// outcome of a run; the default is the text "agent run <call>", exit 0. Set the hooks before the first run. Safe
// for concurrent use.
type Agent struct {
	OnTurn func(call int)
	Result func(ctx context.Context, call int) (text string, exitCode int, err error)

	mu  sync.Mutex
	log []string
}

// Run implements the harness Agent port.
func (a *Agent) Run(ctx context.Context, prompt string) (string, int, error) {
	a.mu.Lock()
	a.log = append(a.log, prompt)
	call := len(a.log)
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

// Workspace records its calls as "prepare fresh=<bool>", "verify" and "publish <branch>". A non-nil error field
// fails that call after it is recorded. A Block field makes that call run OnBlock, when set, then Await; the error
// field is not reached. Set the fields before the first call. Safe for concurrent use.
type Workspace struct {
	PrepareErr, VerifyErr, PublishErr error
	BlockPrepare, BlockVerify         bool
	BlockPublish                      bool
	OnBlock                           func()

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
func (w *Workspace) Prepare(ctx context.Context, fresh bool) error {
	w.record(fmt.Sprintf("prepare fresh=%t", fresh))

	if err := w.block(ctx, w.BlockPrepare); err != nil {
		return err
	}

	return w.PrepareErr
}

// Verify implements the harness Workspace port.
func (w *Workspace) Verify(ctx context.Context) error {
	w.record("verify")

	if err := w.block(ctx, w.BlockVerify); err != nil {
		return err
	}

	return w.VerifyErr
}

// Publish implements the harness Workspace port.
func (w *Workspace) Publish(ctx context.Context, branch string) error {
	w.record("publish " + branch)

	if err := w.block(ctx, w.BlockPublish); err != nil {
		return err
	}

	return w.PublishErr
}

// Calls returns the calls so far.
func (w *Workspace) Calls() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return slices.Clone(w.log)
}
