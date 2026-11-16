package models

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const pinnedImage = "epamedp/krci-agent-go:0.1.1@" +
	"sha256:c7b5f305bb3eb3d1628d97df031b0a83b44a42f1ec243cf0d2ac1fe715923e2a"

func goProfile() ProfileEntry {
	return ProfileEntry{
		Name:   "go",
		Image:  pinnedImage,
		Egress: []string{"proxy.golang.org", "sum.golang.org"},
		Env:    []EnvVar{{Name: "GOPROXY", Value: "https://proxy.golang.org"}},
	}
}

func TestProfileEntryCheckPassesAValidEntry(t *testing.T) {
	assert.Empty(t, goProfile().Check())

	bare := ProfileEntry{Name: "a", Image: "r@sha256:" + strings.Repeat("0", 64)}
	assert.Empty(t, bare.Check(), "egress and env are optional")
}

func TestProfileEntryCheckNamesEachBrokenRule(t *testing.T) {
	cases := map[string]struct {
		mod  func(*ProfileEntry)
		want string
	}{
		"empty name":        {func(p *ProfileEntry) { p.Name = "" }, "name must be [a-z0-9]([a-z0-9-]*[a-z0-9])?"},
		"upper-case name":   {func(p *ProfileEntry) { p.Name = "Go" }, "name must be [a-z0-9]([a-z0-9-]*[a-z0-9])?"},
		"name ends in dash": {func(p *ProfileEntry) { p.Name = "go-" }, "name must be [a-z0-9]([a-z0-9-]*[a-z0-9])?"},
		"no image":          {func(p *ProfileEntry) { p.Image = "" }, "image is required"},
		"tag only": {
			func(p *ProfileEntry) { p.Image = "epamedp/krci-agent-go:0.1.1" },
			"image must be pinned by digest: <repo>[:<tag>]@sha256:<digest>",
		},
		"short digest": {
			func(p *ProfileEntry) { p.Image = "r@sha256:abc" },
			"image must be pinned by digest: <repo>[:<tag>]@sha256:<digest>",
		},
		"empty host": {func(p *ProfileEntry) { p.Egress = []string{""} }, `egress host "" must be a DNS name`},
		"wildcard host": {
			func(p *ProfileEntry) { p.Egress = []string{"*.golang.org"} },
			`egress host "*.golang.org" must be a DNS name`,
		},
		"host with port": {
			func(p *ProfileEntry) { p.Egress = []string{"proxy.golang.org:443"} },
			`egress host "proxy.golang.org:443" must be a DNS name`,
		},
		"upper-case host": {
			func(p *ProfileEntry) { p.Egress = []string{"Proxy.golang.org"} },
			`egress host "Proxy.golang.org" must be a DNS name`,
		},
		"duplicate host": {
			func(p *ProfileEntry) { p.Egress = []string{"a.org", "a.org"} },
			`egress host "a.org" is listed twice`,
		},
		"empty env name": {
			func(p *ProfileEntry) { p.Env = []EnvVar{{Name: "", Value: "x"}} },
			`env name "" must be [A-Za-z_][A-Za-z0-9_]*`,
		},
		"env name with dash": {
			func(p *ProfileEntry) { p.Env = []EnvVar{{Name: "GO-PROXY", Value: "x"}} },
			`env name "GO-PROXY" must be [A-Za-z_][A-Za-z0-9_]*`,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := goProfile()
			c.mod(&p)
			assert.Equal(t, []string{c.want}, p.Check())
		})
	}
}

func TestProfileEntryCheckNeverHoldsAnEnvValue(t *testing.T) {
	p := goProfile()
	p.Env = []EnvVar{{Name: "BAD NAME", Value: "secret-looking-value"}}

	assert.NotContains(t, fmt.Sprint(p.Check()), "secret-looking-value")
}

func TestProfileErrorIsAnAgentProfileError(t *testing.T) {
	err := fmt.Errorf("create: %w", &ProfileError{Profile: "go", Rule: "image is required"})

	assert.ErrorIs(t, err, ErrAgentProfile)
	assert.Equal(t, "create: agent profile: go: image is required", err.Error())

	var pe *ProfileError

	assert.True(t, errors.As(err, &pe))
	assert.Equal(t, "go: image is required", pe.Detail())
}

func TestProfileErrorWithoutProfileName(t *testing.T) {
	pe := &ProfileError{Rule: "profiles file cannot be read"}

	assert.Equal(t, "agent profile: profiles file cannot be read", pe.Error())
	assert.Equal(t, "profiles file cannot be read", pe.Detail())
}

func TestDigestPinned(t *testing.T) {
	const d = "sha256:827f0a832eaa27cf150c20cc8c54976d33e597fb9de82332fc33ccb8699e4df3"

	for v, want := range map[string]bool{
		"epamedp/krci-agent-go@" + d:                                   true,
		"epamedp/krci-agent-go:dev@" + d:                               true,
		"localhost:5001/a/b:1.0@" + d:                                  true,
		"epamedp/krci-agent-go:dev":                                    false,
		"epamedp/krci-agent-go@sha256:827f0a83":                        false,
		"epamedp/krci-agent-go@" + strings.ToUpper(d[len("sha256:"):]): false,
		"epamedp/krci-agent-go@sha512:" + d[len("sha256:"):]:           false,
		"epamedp/krci-agent-go @" + d:                                  false,
		"epamedp/krci-agent-go@" + d + "\n":                            false,
	} {
		assert.Equal(t, want, DigestPinned(v), "%q", v)
	}
}

func TestProfileErrorCauseIsLoggedNotShown(t *testing.T) {
	pe := &ProfileError{Rule: "profiles file is not valid", Cause: "yaml: line 1: did not find expected node content"}

	assert.Equal(t, "profiles file is not valid", pe.Detail())
	assert.Equal(t, "agent profile: profiles file is not valid: yaml: line 1: did not find expected node content",
		pe.Error())
}
