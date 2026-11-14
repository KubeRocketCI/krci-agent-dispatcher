package harness_test

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
)

// file returns a ReadFile that serves body at the agent profile path and fs.ErrNotExist elsewhere.
func file(body string) harness.ReadFile {
	return func(path string) ([]byte, error) {
		if path != "/etc/krci-agent/profile.yaml" {
			return nil, fs.ErrNotExist
		}

		return []byte(body), nil
	}
}

func TestReadProfileParsesTheProfileFile(t *testing.T) {
	p, err := harness.ReadProfile(file("setup: go mod download\nverify: make test\nprompt: Run make fmt first.\n"))
	require.NoError(t, err)
	assert.Equal(t, harness.Profile{Setup: "go mod download", Verify: "make test", Prompt: "Run make fmt first."}, p)
}

func TestReadProfileWithoutTheFileIsTheEmptyProfile(t *testing.T) {
	p, err := harness.ReadProfile(func(string) ([]byte, error) { return nil, fs.ErrNotExist })
	require.NoError(t, err)
	assert.Equal(t, harness.Profile{}, p)
}

func TestReadProfileRefusesABadFile(t *testing.T) {
	const leak = "leak-7f3a"

	tests := map[string]harness.ReadFile{
		"not yaml":      file("setup: [" + leak),
		"unknown field": file("setup: x\nverfy: " + leak + "\n"),
		"not a string":  file("verify:\n  - " + leak + "\n"),
		"unreadable":    func(string) ([]byte, error) { return nil, errors.New("permission denied " + leak) },
	}

	for name, read := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := harness.ReadProfile(read)

			var he *harness.HandshakeError

			require.ErrorAs(t, err, &he)
			assert.Equal(t, "profile file /etc/krci-agent/profile.yaml", he.Item)
			assert.NotContains(t, err.Error(), leak, "the error names the item, never the value")
		})
	}
}
