// Package profilestest is the in-memory fake of the Profiles port: the profiles file content is held in memory.
package profilestest

import (
	"sync"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/profiles"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/templates"
)

// Fake is the Profiles port over profiles file content in memory; without content the file does not exist.
type Fake struct {
	mu       sync.Mutex
	deploy   templates.Deployment
	content  []byte
	exists   bool
	resolved []string
}

// NewFake returns a fake without a profiles file; entries are checked against d.
func NewFake(d templates.Deployment) *Fake { return &Fake{deploy: d} }

// Set replaces the profiles file content.
func (f *Fake) Set(content string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.content, f.exists = []byte(content), true
}

// Remove deletes the profiles file.
func (f *Fake) Remove() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.content, f.exists = nil, false
}

// Resolved returns the project paths of every Resolve call, in order.
func (f *Fake) Resolved() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.resolved...)
}

// Resolve implements the Profiles port.
func (f *Fake) Resolve(projectPath string) (models.ProfileEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.resolved = append(f.resolved, projectPath)

	if !f.exists {
		return models.ProfileEntry{}, &models.ProfileError{Rule: "profiles file does not exist"}
	}

	c, err := profiles.Parse(f.content)
	if err != nil {
		return models.ProfileEntry{}, err
	}

	return c.Resolve(f.deploy, projectPath)
}
