package profiles_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/profiles"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/profiles/profilestest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/dispatcher"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/templates"
)

// subject is a Profiles adapter whose profiles file the test writes and removes between calls.
type subject struct {
	dispatcher.Profiles
	write  func(content string)
	remove func()
}

func deployment(t *testing.T) templates.Deployment {
	t.Helper()

	u, err := models.ParseGitLabURL("https://gitlab.example.com")
	require.NoError(t, err)

	return templates.Deployment{GitLabURL: u}
}

func subjects() map[string]func(t *testing.T) subject {
	return map[string]func(t *testing.T) subject{
		"fake": func(t *testing.T) subject {
			f := profilestest.NewFake(deployment(t))
			return subject{Profiles: f, write: f.Set, remove: f.Remove}
		},
		"file": func(t *testing.T) subject {
			path := filepath.Join(t.TempDir(), "profiles.yaml")

			return subject{
				Profiles: profiles.NewFile(path, deployment(t)),
				write:    func(c string) { require.NoError(t, os.WriteFile(path, []byte(c), 0o600)) },
				remove:   func() { require.NoError(t, os.Remove(path)) },
			}
		},
	}
}

var (
	goImage   = "epamedp/krci-agent-go:0.1.1@sha256:" + strings.Repeat("c", 64)
	nodeImage = "epamedp/krci-agent-node@sha256:" + strings.Repeat("d", 64)
)

const twoProfiles = `
profiles:
  go:
    image: epamedp/krci-agent-go:0.1.1@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
    egress: [proxy.golang.org, sum.golang.org]
    env:
      GOSUMDB: sum.golang.org
      GOPROXY: https://proxy.golang.org
  node:
    image: epamedp/krci-agent-node@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
projects:
  team/Web-App: node
defaultProfile: go
`

func TestContract(t *testing.T) {
	cases := map[string]func(t *testing.T, s subject){
		"ResolvesTheDefaultProfile":      resolvesTheDefaultProfile,
		"ResolvesTheProjectMapping":      resolvesTheProjectMapping,
		"ReadsEveryCall":                 readsEveryCall,
		"MissingFile":                    missingFile,
		"FileThatDoesNotParse":           fileThatDoesNotParse,
		"UnknownKey":                     unknownKey,
		"NoProfileForTheProject":         noProfileForTheProject,
		"UndefinedProfile":               undefinedProfile,
		"InvalidEntry":                   invalidEntry,
		"EntryClashesWithTheTemplate":    entryClashesWithTheTemplate,
		"OneBrokenEntryLeavesTheOthers":  oneBrokenEntryLeavesTheOthers,
		"ProjectListedTwiceIgnoringCase": projectListedTwiceIgnoringCase,
	}

	for subject, build := range subjects() {
		for name, run := range cases {
			t.Run(subject+"/"+name, func(t *testing.T) { run(t, build(t)) })
		}
	}
}

func profileError(t *testing.T, err error) *models.ProfileError {
	t.Helper()

	require.ErrorIs(t, err, models.ErrAgentProfile)

	var pe *models.ProfileError

	require.True(t, errors.As(err, &pe))

	return pe
}

func resolvesTheDefaultProfile(t *testing.T, s subject) {
	s.write(twoProfiles)

	got, err := s.Resolve("krci/test-go-app")
	require.NoError(t, err)

	assert.Equal(t, models.ProfileEntry{
		Name:   "go",
		Image:  goImage,
		Egress: []string{"proxy.golang.org", "sum.golang.org"},
		Env: []models.EnvVar{
			{Name: "GOPROXY", Value: "https://proxy.golang.org"},
			{Name: "GOSUMDB", Value: "sum.golang.org"},
		},
	}, got, "env ordered by name")
}

func resolvesTheProjectMapping(t *testing.T, s subject) {
	s.write(twoProfiles)

	for _, path := range []string{"team/Web-App", "team/web-app", "TEAM/WEB-APP"} {
		got, err := s.Resolve(path)
		require.NoError(t, err, path)
		assert.Equal(t, models.ProfileEntry{Name: "node", Image: nodeImage}, got, path)
	}
}

func readsEveryCall(t *testing.T, s subject) {
	s.write(twoProfiles)

	_, err := s.Resolve("krci/test-go-app")
	require.NoError(t, err)

	s.write(strings.Replace(twoProfiles, "defaultProfile: go", "defaultProfile: node", 1))

	got, err := s.Resolve("krci/test-go-app")
	require.NoError(t, err)
	assert.Equal(t, "node", got.Name, "a change applies to the next call")
}

func missingFile(t *testing.T, s subject) {
	s.write(twoProfiles)
	s.remove()

	_, err := s.Resolve("krci/test-go-app")
	assert.Equal(t, &models.ProfileError{Rule: "profiles file does not exist"}, profileError(t, err))
}

func fileThatDoesNotParse(t *testing.T, s subject) {
	s.write("profiles: [go\n")

	_, err := s.Resolve("krci/test-go-app")
	pe := profileError(t, err)
	assert.Equal(t, "profiles file is not valid", pe.Detail())
	assert.NotEmpty(t, pe.Cause)
}

func unknownKey(t *testing.T, s subject) {
	s.write(strings.Replace(twoProfiles, "    egress:", "    egres:", 1))

	_, err := s.Resolve("krci/test-go-app")
	assert.Equal(t, &models.ProfileError{Profile: "go", Rule: `unknown key "egres"`}, profileError(t, err))

	_, err = s.Resolve("team/web-app")
	assert.NoError(t, err, "the node entry is not broken")
}

func noProfileForTheProject(t *testing.T, s subject) {
	s.write(strings.Replace(twoProfiles, "defaultProfile: go", "", 1))

	_, err := s.Resolve("krci/test-go-app")
	assert.Equal(t, &models.ProfileError{Rule: "no project mapping and no defaultProfile"}, profileError(t, err))

	_, err = s.Resolve("team/web-app")
	assert.NoError(t, err, "a mapped project needs no default")
}

func undefinedProfile(t *testing.T, s subject) {
	s.write(strings.Replace(twoProfiles, "team/Web-App: node", "team/Web-App: missing", 1))

	_, err := s.Resolve("team/web-app")
	assert.Equal(t, &models.ProfileError{Profile: "missing", Rule: "profile is not defined"}, profileError(t, err))
}

func invalidEntry(t *testing.T, s subject) {
	s.write(strings.Replace(twoProfiles, "krci-agent-go:0.1.1@sha256:"+strings.Repeat("c", 64), "krci-agent-go:0.1.1", 1))

	_, err := s.Resolve("krci/test-go-app")
	assert.Equal(t, &models.ProfileError{
		Profile: "go", Rule: "image must be pinned by digest: <repo>[:<tag>]@sha256:<digest>",
	}, profileError(t, err))
}

func entryClashesWithTheTemplate(t *testing.T, s subject) {
	s.write(strings.Replace(twoProfiles, "      GOSUMDB: sum.golang.org", "      HOME: /root", 1))

	_, err := s.Resolve("krci/test-go-app")
	pe := profileError(t, err)
	assert.Equal(t, &models.ProfileError{Profile: "go", Rule: `env name "HOME" is set by the actor template`}, pe)
	assert.NotContains(t, pe.Error(), "/root", "never an env value")

	s.write(strings.Replace(twoProfiles, "[proxy.golang.org, sum.golang.org]", "[gitlab.example.com]", 1))

	_, err = s.Resolve("krci/test-go-app")
	assert.Equal(t, &models.ProfileError{
		Profile: "go", Rule: `egress host "gitlab.example.com" has a fixed egress rule`,
	}, profileError(t, err))
}

func oneBrokenEntryLeavesTheOthers(t *testing.T, s subject) {
	s.write(strings.Replace(twoProfiles, "krci-agent-node@sha256:"+strings.Repeat("d", 64), "krci-agent-node:latest", 1))

	_, err := s.Resolve("team/web-app")
	require.ErrorIs(t, err, models.ErrAgentProfile)

	_, err = s.Resolve("krci/test-go-app")
	assert.NoError(t, err)
}

func projectListedTwiceIgnoringCase(t *testing.T, s subject) {
	s.write(strings.Replace(twoProfiles, "  team/Web-App: node", "  team/Web-App: node\n  team/web-app: go", 1))

	_, err := s.Resolve("krci/test-go-app")
	pe := profileError(t, err)
	assert.Equal(t, "profiles file is not valid", pe.Detail())
	assert.Contains(t, pe.Cause, `project "team/web-app" is listed twice`)
}
