package models

import (
	"errors"
	"net/url"
	"strings"
)

// GitLabURL is a canonical GitLab base URL: https://<host>[:<port>][/<path>], lower-case host, no trailing slash.
type GitLabURL struct {
	canonical string
	host      string
}

var errGitLabURL = errors.New("must be https://<host>[:<port>][/<path>] without user info, query or fragment")

// ParseGitLabURL validates raw and returns its canonical form.
// Trailing slashes are dropped. The error never holds raw.
func ParseGitLabURL(raw string) (GitLabURL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return GitLabURL{}, errGitLabURL
	}

	if u.Scheme != "https" || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || u.Opaque != "" {
		return GitLabURL{}, errGitLabURL
	}

	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return GitLabURL{}, errGitLabURL
	}

	// A non-default path encoding (for example %2F) has no single canonical form.
	if u.RawPath != "" {
		return GitLabURL{}, errGitLabURL
	}

	host := strings.ToLower(u.Host)

	return GitLabURL{
		canonical: "https://" + host + strings.TrimRight(u.EscapedPath(), "/"),
		host:      strings.ToLower(u.Hostname()),
	}, nil
}

// String returns the canonical URL.
func (g GitLabURL) String() string { return g.canonical }

// Host returns the host name without the port.
func (g GitLabURL) Host() string { return g.host }

// GitConfigScope returns the URL that scopes a git http.<url>.* setting to this GitLab and its sub-paths.
func (g GitLabURL) GitConfigScope() string { return g.canonical + "/" }
