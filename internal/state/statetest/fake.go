// Package statetest is the in-memory adapter of the harness StateStore port.
package statetest

import (
	"fmt"
	"sync"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

// Fake keeps the state of one session in memory. Safe for concurrent use.
type Fake struct {
	mu      sync.Mutex
	session string
	state   models.HarnessState
	corrupt bool
	saveErr []error
	loadErr []error
}

func NewFake() *Fake { return &Fake{} }

func (f *Fake) Load(session string) (models.HarnessState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.loadErr) > 0 {
		err := f.loadErr[0]
		f.loadErr = f.loadErr[1:]

		if err != nil {
			return models.HarnessState{}, err
		}
	}

	if f.corrupt {
		return models.HarnessState{}, fmt.Errorf("harness state: %w", models.ErrStateCorrupt)
	}

	if f.session != session {
		return models.HarnessState{}, nil
	}

	return f.state, nil
}

func (f *Fake) Save(session string, s models.HarnessState) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.saveErr) > 0 {
		err := f.saveErr[0]
		f.saveErr = f.saveErr[1:]

		if err != nil {
			return err
		}
	}

	f.session, f.state, f.corrupt = session, s, false

	return nil
}

// Corrupt makes the stored state unreadable until the next Save.
func (f *Fake) Corrupt() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.corrupt = true
}

// FailSave makes the next Save calls return errs, one per call, before any change; a nil error lets its call
// through.
func (f *Fake) FailSave(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.saveErr = append(f.saveErr, errs...)
}

// FailLoad makes the next Load calls return errs, one per call; a nil error lets its call through.
func (f *Fake) FailLoad(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.loadErr = append(f.loadErr, errs...)
}
