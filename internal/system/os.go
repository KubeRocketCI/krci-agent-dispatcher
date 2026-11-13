// Package system holds the adapters of the selftest System port: OS here, the in-memory fake in systemtest.
package system

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
)

// OS is the System of the running process.
type OS struct{}

func (OS) UID() int { return os.Getuid() }

func (OS) Getenv(key string) string { return os.Getenv(key) }

func (OS) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (OS) ReadDir(dir string) ([]fs.DirEntry, error) { return os.ReadDir(dir) }

// WriteProbe uses a unique name, so it never clashes with a file in dir; it removes the file even when Close fails.
// Errors name dir, not the probe file.
func (OS) WriteProbe(dir string) error {
	f, err := os.CreateTemp(dir, ".write-probe-*")
	if err != nil {
		return probeError(dir, err)
	}

	return errors.Join(probeError(dir, f.Close()), probeError(dir, os.Remove(f.Name())))
}

// probeError takes one operation's error, so a joined error never reaches it.
func probeError(dir string, err error) error {
	if err == nil {
		return nil
	}

	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		err = pe.Err
	}

	return &fs.PathError{Op: "write probe", Path: dir, Err: err}
}
