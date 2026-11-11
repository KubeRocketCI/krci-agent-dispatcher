package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRole(t *testing.T) {
	for name, want := range map[string]Role{
		"developer": Developer, "maintainer": Maintainer, "owner": Owner,
		"Maintainer": Maintainer, " maintainer ": Maintainer, "OWNER": Owner,
	} {
		r, err := ParseRole(name)
		require.NoError(t, err, name)
		assert.Equal(t, want, r, name)
		assert.True(t, r.IsTrigger(), name)
	}
}

func TestParseRoleRejects(t *testing.T) {
	for _, v := range []string{"", "guest", "reporter", "admin", "30"} {
		_, err := ParseRole(v)
		require.Error(t, err, v)
		assert.Equal(t, "must be one of developer, maintainer, owner", err.Error())
	}
}

func TestRoleIsTrigger(t *testing.T) {
	for _, r := range []Role{0, Guest, Reporter, 25, 60} {
		assert.False(t, r.IsTrigger(), int(r))
	}
}

func TestRoleString(t *testing.T) {
	assert.Equal(t, "guest", Guest.String())
	assert.Equal(t, "owner", Owner.String())
	assert.Equal(t, "role(25)", Role(25).String())
}

func TestStartRefusedText(t *testing.T) {
	for r, want := range map[Role]string{
		Developer:  "Only Developers or higher can start the agent.",
		Maintainer: "Only Maintainers or higher can start the agent.",
		Owner:      "Only Owners or higher can start the agent.",
	} {
		assert.Equal(t, want, StartRefusedText(r))
	}
}
