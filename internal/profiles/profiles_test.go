package profiles_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/profiles"
)

func TestCheckPassesAValidFile(t *testing.T) {
	c, err := profiles.Parse([]byte(twoProfiles))
	require.NoError(t, err)

	assert.Empty(t, c.Check(deployment(t)))
}

func TestCheckListsEveryProblem(t *testing.T) {
	content := `
profiles:
  go:
    image: epamedp/krci-agent-go:0.1.1
    env:
      HOME: /root
  node:
    image: epamedp/krci-agent-node@sha256:` + strings.Repeat("d", 64) + `
    egress: [api.anthropic.com]
projects:
  team/app: missing
defaultProfile: other
`
	c, err := profiles.Parse([]byte(content))
	require.NoError(t, err)

	assert.Equal(t, []*models.ProfileError{
		{Profile: "go", Rule: "image must be pinned by digest: <repo>[:<tag>]@sha256:<digest>"},
		{Profile: "go", Rule: `env name "HOME" is set by the actor template`},
		{Profile: "node", Rule: `egress host "api.anthropic.com" has a fixed egress rule`},
		{Profile: "missing", Rule: `profile of project "team/app" is not defined`},
		{Profile: "other", Rule: "defaultProfile is not defined"},
	}, c.Check(deployment(t)))
}

func TestCheckAcceptsAnEmptyDefaultProfile(t *testing.T) {
	c, err := profiles.Parse([]byte(strings.Replace(twoProfiles, "defaultProfile: go", "", 1)))
	require.NoError(t, err)

	assert.Empty(t, c.Check(deployment(t)))
}

func TestParseKeepsEnvValuesAsWritten(t *testing.T) {
	content := `
profiles:
  go:
    image: r@sha256:` + strings.Repeat("0", 64) + `
    env:
      PY: 3.10
      A: yes
      B: off
      C: 0777
      D: 1e3
      E: 0x10
      F: ~
      G: "quoted: text"
defaultProfile: go
`
	c, err := profiles.Parse([]byte(content))
	require.NoError(t, err)

	p, err := c.Resolve(deployment(t), "any/project")
	require.NoError(t, err)

	assert.Equal(t, []models.EnvVar{
		{Name: "A", Value: "yes"}, {Name: "B", Value: "off"}, {Name: "C", Value: "0777"}, {Name: "D", Value: "1e3"},
		{Name: "E", Value: "0x10"}, {Name: "F", Value: "~"}, {Name: "G", Value: "quoted: text"},
		{Name: "PY", Value: "3.10"},
	}, p.Env)
}

func TestABrokenEntryFailsOnlyItsProjects(t *testing.T) {
	cases := map[string]struct {
		entry, rule string
	}{
		"unknown key":      {"    typo: 1\n", `unknown key "typo"`},
		"image not text":   {"", "image must be a string"},
		"egress not list":  {"    egress: proxy.golang.org\n", "egress must be a list of hosts"},
		"host not text":    {"    egress: [[a]]\n", "egress must be a list of hosts"},
		"env not map":      {"    env: [A]\n", "env must be a map of names to values"},
		"env value a list": {"    env:\n      A: [a]\n", `env value of "A" must be a scalar`},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			image := "    image: r@sha256:" + strings.Repeat("0", 64) + "\n"
			if name == "image not text" {
				image = "    image: [r]\n"
			}

			content := "profiles:\n  go:\n" + image + c.entry +
				"  node:\n    image: r@sha256:" + strings.Repeat("1", 64) + "\n" +
				"projects:\n  team/web: node\ndefaultProfile: go\n"

			cfg, err := profiles.Parse([]byte(content))
			require.NoError(t, err, "a broken entry does not refuse the file")

			_, err = cfg.Resolve(deployment(t), "krci/app")
			assert.Equal(t, &models.ProfileError{Profile: "go", Rule: c.rule}, profileError(t, err))

			_, err = cfg.Resolve(deployment(t), "team/web")
			assert.NoError(t, err, "projects of other entries resolve")

			assert.Contains(t, cfg.Check(deployment(t)), &models.ProfileError{Profile: "go", Rule: c.rule})
		})
	}
}

func TestParseRefusesAFileOfAnotherShape(t *testing.T) {
	for name, content := range map[string]string{
		"not YAML":           "profiles: [go\n",
		"a list":             "- a\n",
		"unknown top key":    "profile: {}\n",
		"profiles a list":    "profiles: [go]\n",
		"projects a list":    "projects: [a]\n",
		"default not text":   "defaultProfile: [go]\n",
		"profile twice":      "profiles:\n  go: {}\n  go: {}\n",
		"project value list": "projects:\n  a/b: [go]\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := profiles.Parse([]byte(content))

			pe := profileError(t, err)
			assert.Empty(t, pe.Profile)
			assert.Equal(t, "profiles file is not valid", pe.Rule, "the rule is stable text")
			assert.NotEmpty(t, pe.Cause, "the cause goes to logs and check-profiles")
			assert.NotContains(t, pe.Detail(), "json", "no parser internals in the issue note")
		})
	}
}

func TestEmptyProjectMapping(t *testing.T) {
	c, err := profiles.Parse([]byte(strings.Replace(twoProfiles, "team/Web-App: node", `team/Web-App: ""`, 1)))
	require.NoError(t, err)

	want := &models.ProfileError{Rule: `profile of project "team/web-app" is empty`}

	_, err = c.Resolve(deployment(t), "team/web-app")
	assert.Equal(t, want, profileError(t, err))
	assert.Equal(t, []*models.ProfileError{want}, c.Check(deployment(t)))
}

func TestChartProfilesFileIsValid(t *testing.T) {
	c, err := profiles.Read(filepath.Join("..", "..", "deploy-templates", "profiles.yaml"))
	require.NoError(t, err)

	assert.Empty(t, c.Check(deployment(t)))

	p, err := c.Resolve(deployment(t), "krci/test-go-app")
	require.NoError(t, err)
	assert.Equal(t, "go", p.Name)
}

func TestReadNamesWhyTheFileCannotBeRead(t *testing.T) {
	_, err := profiles.Read(t.TempDir())

	pe := profileError(t, err)
	assert.Equal(t, "profiles file cannot be read", pe.Rule)
	assert.Contains(t, pe.Cause, "is a directory")
	assert.Contains(t, pe.Error(), "is a directory", "the cause is logged")
}

func TestAnEmptyFileHasNoProfile(t *testing.T) {
	c, err := profiles.Parse(nil)
	require.NoError(t, err)

	assert.Empty(t, c.Check(deployment(t)))

	_, err = c.Resolve(deployment(t), "krci/app")
	assert.Equal(t, &models.ProfileError{Rule: "no project mapping and no defaultProfile"}, profileError(t, err))
}

func TestAnEntryThatIsNotAMap(t *testing.T) {
	c, err := profiles.Parse([]byte("profiles:\n  go: krci-agent-go\ndefaultProfile: go\n"))
	require.NoError(t, err)

	_, err = c.Resolve(deployment(t), "krci/app")
	assert.Equal(t, &models.ProfileError{
		Profile: "go", Rule: "entry must be a map with the keys image, egress, env",
	}, profileError(t, err))
}
