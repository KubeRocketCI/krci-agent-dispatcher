// Package systemtest is the in-memory adapter of the selftest System port.
package systemtest

import (
	"io/fs"
	"os/exec"
	"slices"
	"testing/fstest"
)

const (
	opOpen  = "open"
	opProbe = "write probe"
)

// Fake is an operating system described by its fields; the zero value has no tools, no env and no dirs.
type Fake struct {
	User int
	Env  map[string]string
	// OnPath are the tools LookPath finds, at /usr/bin/<tool>.
	OnPath []string
	// Dirs maps a directory to the names it holds; a directory absent from Dirs does not exist.
	Dirs map[string][]string
	// ReadOnly are directories WriteProbe refuses.
	ReadOnly []string
	// Unreadable are directories ReadDir refuses.
	Unreadable []string
}

func (f *Fake) UID() int { return f.User }

func (f *Fake) Getenv(key string) string { return f.Env[key] }

func (f *Fake) LookPath(name string) (string, error) {
	if !slices.Contains(f.OnPath, name) {
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}

	return "/usr/bin/" + name, nil
}

func (f *Fake) ReadDir(dir string) ([]fs.DirEntry, error) {
	names, ok := f.Dirs[dir]
	if !ok {
		return nil, &fs.PathError{Op: opOpen, Path: dir, Err: fs.ErrNotExist}
	}

	if slices.Contains(f.Unreadable, dir) {
		return nil, &fs.PathError{Op: opOpen, Path: dir, Err: fs.ErrPermission}
	}

	m := fstest.MapFS{}
	for _, n := range names {
		m[n] = &fstest.MapFile{}
	}

	return fs.ReadDir(m, ".")
}

func (f *Fake) WriteProbe(dir string) error {
	if _, ok := f.Dirs[dir]; !ok {
		return &fs.PathError{Op: opProbe, Path: dir, Err: fs.ErrNotExist}
	}

	if slices.Contains(f.ReadOnly, dir) {
		return &fs.PathError{Op: opProbe, Path: dir, Err: fs.ErrPermission}
	}

	return nil
}
