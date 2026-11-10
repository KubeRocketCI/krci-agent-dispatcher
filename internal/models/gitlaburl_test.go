package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGitLabURL(t *testing.T) {
	tests := []struct {
		in, canonical, host, scope string
	}{
		{"https://gitlab.example", "https://gitlab.example", "gitlab.example", "https://gitlab.example/"},
		{"https://gitlab.example/", "https://gitlab.example", "gitlab.example", "https://gitlab.example/"},
		{"https://gitlab.example//", "https://gitlab.example", "gitlab.example", "https://gitlab.example/"},
		{
			"https://gitlab.example/gitlab/", "https://gitlab.example/gitlab", "gitlab.example",
			"https://gitlab.example/gitlab/",
		},
		{
			"https://gitlab.example:8443/a/b/", "https://gitlab.example:8443/a/b", "gitlab.example",
			"https://gitlab.example:8443/a/b/",
		},
		{
			"HTTPS://GitLab.Example/GitLab/", "https://gitlab.example/GitLab", "gitlab.example",
			"https://gitlab.example/GitLab/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			u, err := ParseGitLabURL(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.canonical, u.String())
			assert.Equal(t, tt.host, u.Host())
			assert.Equal(t, tt.scope, u.GitConfigScope())
		})
	}
}

func TestParseGitLabURLRejects(t *testing.T) {
	const want = "must be https://<host>[:<port>][/<path>] without user info, query or fragment"

	for _, v := range []string{
		"",
		"http://gitlab.internal",
		"gitlab.example",
		"https://",
		"https:///gitlab",
		"https://gitlab.example:",
		"https://bot:hunter2@gitlab.example",
		"https://gitlab.example?private_token=hunter2",
		"https://gitlab.example?",
		"https://gitlab.example#hunter2",
		"https://gitlab.example/a%2Fb",
		" https://gitlab.example",
		"not a url",
	} {
		t.Run(v, func(t *testing.T) {
			u, err := ParseGitLabURL(v)
			require.EqualError(t, err, want)
			assert.Equal(t, GitLabURL{}, u)
		})
	}
}
