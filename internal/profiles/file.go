package profiles

import (
	"errors"
	"io/fs"
	"os"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/templates"
)

// File is the Profiles port adapter over the profiles file at a path, read at each Resolve. Its fake is
// profilestest.Fake.
type File struct {
	path   string
	deploy templates.Deployment
}

// NewFile returns the adapter of the profiles file at path; entries are checked against d.
func NewFile(path string, d templates.Deployment) *File { return &File{path: path, deploy: d} }

// Resolve reads and parses the file, then resolves projectPath (Config.Resolve).
func (f *File) Resolve(projectPath string) (models.ProfileEntry, error) {
	c, err := Read(f.path)
	if err != nil {
		return models.ProfileEntry{}, err
	}

	return c.Resolve(f.deploy, projectPath)
}

// Read reads and parses the profiles file at path. A missing or unreadable file is a *models.ProfileError.
func Read(path string) (Config, error) {
	b, err := os.ReadFile(path)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Config{}, &models.ProfileError{Rule: "profiles file does not exist"}
	case err != nil:
		return Config{}, &models.ProfileError{Rule: "profiles file cannot be read", Cause: err.Error()}
	}

	return Parse(b)
}
