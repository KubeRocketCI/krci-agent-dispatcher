package models

import (
	"fmt"
	"regexp"
)

// ProfileEntry is the dispatcher side of one agent profile: the agent image, the extra egress hosts and the extra
// plain env of the actors of its projects. Env is ordered by name.
type ProfileEntry struct {
	Name   string
	Image  string
	Egress []string
	Env    []EnvVar
}

// EnvVar is one plain env variable of a profile entry.
type EnvVar struct {
	Name  string
	Value string
}

var (
	profileNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	// Substrate accepts only actor images pinned by digest.
	digestPinnedRe = regexp.MustCompile(`^[^[:space:]@]+@sha256:[a-f0-9]{64}$`)
	hostRe         = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	envNameRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// maxHostLen is the longest DNS name.
const maxHostLen = 253

// DigestPinned reports whether image is <repo>[:<tag>]@sha256:<digest>.
func DigestPinned(image string) bool { return digestPinnedRe.MatchString(image) }

// Check returns every rule p breaks, in field order; empty when p is valid. A rule names hosts and env names, never
// an env value.
func (p ProfileEntry) Check() []string {
	var rules []string

	if !profileNameRe.MatchString(p.Name) {
		rules = append(rules, "name must be [a-z0-9]([a-z0-9-]*[a-z0-9])?")
	}

	switch {
	case p.Image == "":
		rules = append(rules, "image is required")
	case !DigestPinned(p.Image):
		rules = append(rules, "image must be pinned by digest: <repo>[:<tag>]@sha256:<digest>")
	}

	seen := make(map[string]bool, len(p.Egress))

	for _, h := range p.Egress {
		switch {
		case len(h) > maxHostLen || !hostRe.MatchString(h):
			rules = append(rules, fmt.Sprintf("egress host %q must be a DNS name", h))
		case seen[h]:
			rules = append(rules, fmt.Sprintf("egress host %q is listed twice", h))
		}

		seen[h] = true
	}

	for _, e := range p.Env {
		if !envNameRe.MatchString(e.Name) {
			rules = append(rules, fmt.Sprintf("env name %q must be [A-Za-z_][A-Za-z0-9_]*", e.Name))
		}
	}

	return rules
}

// ProfileError is a failed profile resolution: the profile, empty when the failure is not one profile's, the rule
// and the cause, such as a parser or file system error. Detail is stable text for the issue; Error adds the cause,
// for logs. It is an ErrAgentProfile and never holds an env value.
type ProfileError struct {
	Profile string
	Rule    string
	Cause   string
}

// Detail returns "<profile>: <rule>", or the rule alone without a profile: the status note row Detail.
func (e *ProfileError) Detail() string {
	if e.Profile == "" {
		return e.Rule
	}

	return e.Profile + ": " + e.Rule
}

func (e *ProfileError) Error() string {
	if e.Cause == "" {
		return ErrAgentProfile.Error() + ": " + e.Detail()
	}

	return ErrAgentProfile.Error() + ": " + e.Detail() + ": " + e.Cause
}

// Is reports ErrAgentProfile.
func (e *ProfileError) Is(target error) bool { return target == ErrAgentProfile }
