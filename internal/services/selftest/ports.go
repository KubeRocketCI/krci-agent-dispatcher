package selftest

import "io/fs"

// System is the selftest's view of the operating system it checks.
type System interface {
	UID() int
	Getenv(key string) string
	LookPath(name string) (string, error)
	ReadDir(dir string) ([]fs.DirEntry, error)
	// WriteProbe creates and removes a file in dir; an error means dir is not writable.
	WriteProbe(dir string) error
}
