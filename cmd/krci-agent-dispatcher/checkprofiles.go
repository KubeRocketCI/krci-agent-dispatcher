package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/profiles"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/templates"
)

// Exit codes of check-profiles (sysexits).
const (
	exitOK      = 0
	exitInvalid = 1
	exitUsage   = 64
)

const usage = "usage: krci-agent-dispatcher [check-profiles <file>]\n" +
	"  no argument: serve hooks; config from the environment\n" +
	"  check-profiles <file>: check a profiles file; GITLAB_URL, when set, adds the GitLab host check\n"

// checkProfiles checks the profiles file at path with the rules of profile resolution and writes one line per
// problem to stdout: exit 0 without problems, 1 with any, 64 on an invalid gitLabURL. It writes no env value.
func checkProfiles(path, gitLabURL string, stdout, stderr io.Writer) int {
	var d templates.Deployment

	if gitLabURL != "" {
		u, err := models.ParseGitLabURL(gitLabURL)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "GITLAB_URL %v\n", err)
			return exitUsage
		}

		d.GitLabURL = u
	}

	c, err := profiles.Read(path)
	if err != nil {
		var pe *models.ProfileError
		if errors.As(err, &pe) {
			_, _ = fmt.Fprintln(stdout, line(pe))
		}

		return exitInvalid
	}

	problems := c.Check(d)
	for _, pe := range problems {
		_, _ = fmt.Fprintln(stdout, line(pe))
	}

	if len(problems) > 0 {
		return exitInvalid
	}

	return exitOK
}

// line returns the detail of pe, then its cause when set.
func line(pe *models.ProfileError) string {
	if pe.Cause == "" {
		return pe.Detail()
	}

	return pe.Detail() + ": " + pe.Cause
}
