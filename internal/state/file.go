// Package state is the file adapter of the harness StateStore port; the in-memory fake is in statetest.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
)

const (
	fileName = "state.json"
	dirMode  = 0o700
)

// File keeps the state of one session in <dir>/state.json. Save replaces the file atomically; state of another
// session is ignored and replaced.
type File struct {
	dir string
}

// NewFile returns a File in dir; Save creates dir.
func NewFile(dir string) *File { return &File{dir: dir} }

type fileState struct {
	Session string        `json:"session"`
	Pending *pendingState `json:"pending,omitempty"`
	Started bool          `json:"started,omitempty"`
}

type pendingState struct {
	Turn int    `json:"turn"`
	Body string `json:"body"`
}

func (f *File) path() string { return filepath.Join(f.dir, fileName) }

func (f *File) Load(session string) (models.HarnessState, error) {
	b, err := os.ReadFile(f.path())
	if errors.Is(err, fs.ErrNotExist) {
		return models.HarnessState{}, nil
	}

	if err != nil {
		return models.HarnessState{}, fmt.Errorf("read harness state: %w", err)
	}

	var rec fileState
	if err := json.Unmarshal(b, &rec); err != nil {
		return models.HarnessState{}, fmt.Errorf("harness state: %w", models.ErrStateCorrupt)
	}

	if rec.Session != session {
		return models.HarnessState{}, nil
	}

	s := models.HarnessState{Started: rec.Started}
	if rec.Pending != nil {
		s.Pending = &models.PendingReply{Turn: rec.Pending.Turn, Body: rec.Pending.Body}
	}

	return s, nil
}

func (f *File) Save(session string, s models.HarnessState) error {
	rec := fileState{Session: session, Started: s.Started}
	if s.Pending != nil {
		rec.Pending = &pendingState{Turn: s.Pending.Turn, Body: s.Pending.Body}
	}

	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode harness state: %w", err)
	}

	if err := os.MkdirAll(f.dir, dirMode); err != nil {
		return fmt.Errorf("create harness state dir: %w", err)
	}

	tmp, err := os.CreateTemp(f.dir, ".state-*")
	if err != nil {
		return fmt.Errorf("write harness state: %w", err)
	}

	if err := writeSync(tmp, b); err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("write harness state: %w", err)
	}

	if err := os.Rename(tmp.Name(), f.path()); err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("write harness state: %w", err)
	}

	if err := syncDir(f.dir); err != nil {
		return fmt.Errorf("sync harness state dir: %w", err)
	}

	return nil
}

// writeSync writes b, flushes it to disk and closes f.
func writeSync(f *os.File, b []byte) error {
	_, err := f.Write(b)
	if err == nil {
		err = f.Sync()
	}

	return errors.Join(err, f.Close())
}

// syncDir flushes the directory entry of a renamed file.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}

	return errors.Join(d.Sync(), d.Close())
}
