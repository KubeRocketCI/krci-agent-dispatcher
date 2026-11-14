package harness

import (
	"errors"
	"io/fs"

	"sigs.k8s.io/yaml"
)

// ProfilePath is the agent profile file of the agent image.
const ProfilePath = "/etc/krci-agent/profile.yaml"

// ReadFile reads one file; a missing file is an error that wraps fs.ErrNotExist.
type ReadFile func(path string) ([]byte, error)

// Profile is the agent profile file: shell commands run with sh -c in the checkout, and text for the system prompt.
// Setup runs once after the clone, Verify before publish, Prompt follows the harness system prompt. Empty fields are
// no step and no text.
type Profile struct {
	Setup  string `json:"setup,omitempty"`
	Verify string `json:"verify,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// ReadProfile reads ProfilePath. No file is the empty profile. A file that cannot be read, is not YAML or has a field
// other than the string fields of Profile is a HandshakeError; it never holds the file content.
func ReadProfile(read ReadFile) (Profile, error) {
	item := "profile file " + ProfilePath

	b, err := read(ProfilePath)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Profile{}, nil
	case err != nil:
		return Profile{}, &HandshakeError{Item: item, Reason: "cannot be read"}
	}

	var p Profile
	if err := yaml.UnmarshalStrict(b, &p); err != nil {
		return Profile{}, &HandshakeError{Item: item, Reason: "must be YAML with the string fields setup, verify, prompt"}
	}

	return p, nil
}
