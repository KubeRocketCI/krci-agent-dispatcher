package claude

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	conversationFile = "conversation"
	stateDirMode     = 0o700
)

// started reports whether <dir>/conversation holds id.
func started(dir, id string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, conversationFile))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("read conversation file: %w", err)
	}

	return strings.TrimSpace(string(b)) == id, nil
}

// saveStarted replaces <dir>/conversation with id atomically; it creates dir when missing.
func saveStarted(dir, id string) error {
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".conversation-*")
	if err != nil {
		return fmt.Errorf("write conversation file: %w", err)
	}

	_, err = tmp.WriteString(id + "\n")
	if err == nil {
		err = tmp.Sync()
	}

	if err = errors.Join(err, tmp.Close()); err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, conversationFile))
	}

	if err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("write conversation file: %w", err)
	}

	return nil
}
