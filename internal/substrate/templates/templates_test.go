package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
)

func gitLabURL(t *testing.T, raw string) models.GitLabURL {
	t.Helper()

	u, err := models.ParseGitLabURL(raw)
	require.NoError(t, err)

	return u
}

func goldenParams(t *testing.T) Params {
	t.Helper()

	return Params{
		Deployment: Deployment{
			Atespace:         "krci-agents",
			WorkerPool:       "workers",
			SecretsNamespace: "krci-agents",
			HarnessImage:     "docker.io/epamedp/krci-agent-harness@sha256:" + strings.Repeat("1", 64),
			GitLabURL:        gitLabURL(t, "https://gitlab.127.0.0.1.nip.io"),
			Model:            "claude-haiku-5-5",
			AgentSteps:       40,
			TurnLimit:        20,
			TurnTimeout:      15 * time.Minute,
			HarnessPoll:      15 * time.Second,
		},
		ActorSpec: models.ActorSpec{
			ProjectID:   12,
			ProjectPath: "krci/test-go-app",
			IID:         7,
			SessionID:   "00000000-0000-0000-0000-00000000000a",
			BotUsername: "krci-agent",
			Profile:     goProfile(),
		},
	}
}

func goProfile() models.ProfileEntry {
	return models.ProfileEntry{
		Name:   "go",
		Image:  "localhost:5001/krci-agent-go@sha256:" + strings.Repeat("0", 64),
		Egress: []string{"proxy.golang.org", "sum.golang.org", "storage.googleapis.com"},
		Env: []models.EnvVar{
			{Name: "GOPROXY", Value: "https://proxy.golang.org"},
			{Name: "GOSUMDB", Value: "sum.golang.org"},
		},
	}
}

func nodeProfile() models.ProfileEntry {
	return models.ProfileEntry{
		Name:   "node",
		Image:  "epamedp/krci-agent-node@sha256:" + strings.Repeat("2", 64),
		Egress: []string{"registry.npmjs.org"},
		Env:    []models.EnvVar{{Name: "NPM_CONFIG_REGISTRY", Value: "https://registry.npmjs.org"}},
	}
}

func loadGolden(t *testing.T, name string, m proto.Message) {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	j, err := yaml.YAMLToJSON(b)
	require.NoError(t, err)
	require.NoError(t, protojson.Unmarshal(j, m))
}

func env(tmpl *ateapi.ActorTemplate, name string) string {
	for _, e := range tmpl.GetContainers()[0].GetEnv() {
		if e.GetName() == name {
			return e.GetValue()
		}
	}

	return ""
}

func TestRenderGolden(t *testing.T) {
	tmpl, pol, err := Render(goldenParams(t))
	require.NoError(t, err)

	var wantTmpl ateapi.ActorTemplate

	var wantPol ateapi.EgressPolicy

	loadGolden(t, "actortemplate.golden.yaml", &wantTmpl)
	loadGolden(t, "egresspolicy.golden.yaml", &wantPol)

	assert.True(t, proto.Equal(&wantTmpl, tmpl), "actor template differs:\nwant %v\ngot  %v", &wantTmpl, tmpl)
	assert.True(t, proto.Equal(&wantPol, pol), "egress policy differs:\nwant %v\ngot  %v", &wantPol, pol)
}

// envOf returns the env of the first container as a harness Getenv.
func envOf(tmpl *ateapi.ActorTemplate) harness.Getenv {
	m := map[string]string{}
	for _, e := range tmpl.GetContainers()[0].GetEnv() {
		m[e.GetName()] = e.GetValue()
	}

	return func(k string) string { return m[k] }
}

func TestRenderPassesTheHarnessHandshake(t *testing.T) {
	p := goldenParams(t)
	p.ProjectID, p.IID, p.SessionID = 31, 4, "session-b"
	p.Model, p.AgentSteps, p.TurnLimit = "model-y", 25, 9
	p.TurnTimeout, p.HarnessPoll = 10*time.Minute, 20*time.Second

	tmpl, _, err := Render(p)
	require.NoError(t, err)

	cfg, err := harness.Handshake(envOf(tmpl))
	require.NoError(t, err)

	assert.Equal(t, models.IssueKey{ProjectID: 31, IID: 4}, cfg.Key)
	assert.Equal(t, "krci/test-go-app", cfg.ProjectPath)
	assert.Equal(t, "krci-agent", cfg.Bot)
	assert.Equal(t, "session-b", cfg.Session)
	assert.Equal(t, "model-y", cfg.Model)
	assert.Equal(t, 25, cfg.AgentSteps)
	assert.Equal(t, 9, cfg.TurnLimit)
	assert.Equal(t, 10*time.Minute, cfg.TurnTimeout)
	assert.Equal(t, 20*time.Second, cfg.Poll)
	assert.Equal(t, "/home/agent", cfg.Home)
}

func TestRenderMountsTheHarness(t *testing.T) {
	tmpl, _, err := Render(goldenParams(t))
	require.NoError(t, err)

	c := tmpl.GetContainers()[0]
	assert.Equal(t, []string{"/opt/krci-agent/krci-agent-harness", "run"}, c.GetCommand())

	var mounted bool

	for _, m := range c.GetVolumeMounts() {
		if m.GetMountPath() == "/opt/krci-agent" {
			mounted = true

			for _, v := range tmpl.GetVolumes() {
				if v.GetName() == m.GetName() {
					assert.Equal(t, "docker.io/epamedp/krci-agent-harness@sha256:"+strings.Repeat("1", 64),
						v.GetImage().GetReference())
				}
			}
		}
	}

	assert.True(t, mounted, "the harness image is mounted at /opt/krci-agent")
}

func TestRenderValues(t *testing.T) {
	p := goldenParams(t)
	p.IID, p.ProjectPath, p.SessionID, p.BotUsername = 3, "team/other-app", "s2", "other-bot"

	tmpl, pol, err := Render(p)
	require.NoError(t, err)

	assert.Equal(t, "3", env(tmpl, "ISSUE_IID"))
	assert.Equal(t, "team/other-app", env(tmpl, "PROJECT_PATH"))
	assert.Equal(t, "s2", env(tmpl, "SESSION_ID"))
	assert.Equal(t, "other-bot", env(tmpl, "BOT_USERNAME"))
	assert.Equal(t, "workers", tmpl.GetWorkerSelector().GetMatchLabels()["workload"])
	assert.Nil(t, tmpl.GetMetadata())
	assert.Nil(t, pol.GetMetadata())
	assert.Equal(t, []string{"gitlab.127.0.0.1.nip.io"}, pol.GetRules()[1].GetHttps().GetHostnames())
}

func TestRenderHasNoSecretValue(t *testing.T) {
	tmpl, pol, err := Render(goldenParams(t))
	require.NoError(t, err)

	assert.Equal(t, "placeholder", env(tmpl, "ANTHROPIC_API_KEY"))

	uris := 0

	for _, r := range pol.GetRules() {
		for _, h := range r.GetHttps().GetEffects().GetReplaceHeaders() {
			uris++

			assert.True(t, strings.HasPrefix(h.GetCredentialUri(), "ate-secret://k8s.io/default/krci-agents/"),
				h.GetCredentialUri())
		}
	}

	assert.Equal(t, 3, uris)
}

// The git auth placeholder goes only to the harness's own git commands, not to every process of the actor.
func TestRenderHasNoGitAuthEnv(t *testing.T) {
	tmpl, _, err := Render(goldenParams(t))
	require.NoError(t, err)

	for _, e := range tmpl.GetContainers()[0].GetEnv() {
		assert.False(t, strings.HasPrefix(e.GetName(), "GIT_CONFIG_"), e.GetName())
		assert.NotContains(t, e.GetValue(), "Authorization", e.GetName())
	}
}

func TestRenderQuotesPayloadValues(t *testing.T) {
	p := goldenParams(t)
	p.ProjectPath = "krci/app\"\n  injected: true"

	tmpl, _, err := Render(p)
	require.NoError(t, err)
	assert.Equal(t, p.ProjectPath, env(tmpl, "PROJECT_PATH"))
}

func TestRenderGitLabURLForms(t *testing.T) {
	tests := []struct {
		in, url, host string
	}{
		{
			in:   "https://gitlab.example.com/",
			url:  "https://gitlab.example.com",
			host: "gitlab.example.com",
		},
		{
			in:   "https://gitlab.example.com/gitlab",
			url:  "https://gitlab.example.com/gitlab",
			host: "gitlab.example.com",
		},
		{
			in:   "https://gitlab.example.com/gitlab/",
			url:  "https://gitlab.example.com/gitlab",
			host: "gitlab.example.com",
		},
		{
			in:   "https://GitLab.Example.com:8443/gitlab//",
			url:  "https://gitlab.example.com:8443/gitlab",
			host: "gitlab.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			p := goldenParams(t)
			p.GitLabURL = gitLabURL(t, tt.in)

			tmpl, pol, err := Render(p)
			require.NoError(t, err)

			assert.Equal(t, tt.url, env(tmpl, "GITLAB_URL"))
			assert.Equal(t, []string{tt.host}, pol.GetRules()[1].GetHttps().GetHostnames())
		})
	}
}

func TestRenderRejectsEmptyGitLabURL(t *testing.T) {
	p := goldenParams(t)
	p.GitLabURL = models.GitLabURL{}

	_, _, err := Render(p)
	require.Error(t, err)
}

func TestRenderRejectsInvalidSpec(t *testing.T) {
	p := goldenParams(t)
	p.SessionID = ""

	_, _, err := Render(p)
	require.ErrorContains(t, err, "session ID")
}

func TestRenderTakesImageEnvAndEgressFromTheProfile(t *testing.T) {
	p := goldenParams(t)
	p.Profile = nodeProfile()

	tmpl, pol, err := Render(p)
	require.NoError(t, err)

	assert.Equal(t, "epamedp/krci-agent-node@sha256:"+strings.Repeat("2", 64), tmpl.GetContainers()[0].GetImage())
	assert.Equal(t, "node", env(tmpl, "AGENT_PROFILE"))
	assert.Equal(t, "https://registry.npmjs.org", env(tmpl, "NPM_CONFIG_REGISTRY"))
	assert.Empty(t, env(tmpl, "GOPROXY"), "no Go env outside the go profile")

	require.Len(t, pol.GetRules(), 3)
	assert.Equal(t, []string{"api.anthropic.com"}, pol.GetRules()[0].GetHttps().GetHostnames())
	assert.Equal(t, []string{"gitlab.127.0.0.1.nip.io"}, pol.GetRules()[1].GetHttps().GetHostnames())
	assert.Equal(t, []string{"registry.npmjs.org"}, pol.GetRules()[2].GetTlsPassthrough().GetHostnames())
	assert.Equal(t, []int32{443}, pol.GetRules()[2].GetTlsPassthrough().GetPorts().GetNumbers())
}

func TestRenderWithoutProfileEgressHasOnlyTheFixedRules(t *testing.T) {
	p := goldenParams(t)
	p.Profile.Egress, p.Profile.Env = nil, nil

	tmpl, pol, err := Render(p)
	require.NoError(t, err)

	assert.Len(t, pol.GetRules(), 2)
	assert.Equal(t, "go", env(tmpl, "AGENT_PROFILE"))
}

func TestRenderQuotesProfileEnvValues(t *testing.T) {
	p := goldenParams(t)
	p.Profile.Env = []models.EnvVar{{Name: "X", Value: "a\"\n  - name: HOME\n    value: /root"}}

	tmpl, _, err := Render(p)
	require.NoError(t, err)

	assert.Equal(t, p.Profile.Env[0].Value, env(tmpl, "X"))
	assert.Equal(t, "/home/agent", env(tmpl, "HOME"))
}

func TestRenderRefusesAProfileThatClashesWithTheTemplate(t *testing.T) {
	p := goldenParams(t)
	p.Profile.Env = []models.EnvVar{{Name: "HOME", Value: "/root"}}

	_, _, err := Render(p)
	require.ErrorContains(t, err, `profile env name "HOME" is set by the actor template`)
}

func TestRenderRefusesAnInvalidProfile(t *testing.T) {
	p := goldenParams(t)
	p.Profile.Image = "epamedp/krci-agent-go:0.1.1"

	_, _, err := Render(p)
	require.ErrorContains(t, err, "image must be pinned by digest")
}

func TestCheckProfile(t *testing.T) {
	d := goldenParams(t).Deployment

	cases := map[string]struct {
		mod  func(*models.ProfileEntry)
		want []string
	}{
		"valid": {func(*models.ProfileEntry) {}, nil},
		"every rule": {
			func(p *models.ProfileEntry) { p.Image, p.Env = "", []models.EnvVar{{Name: "HOME"}} },
			[]string{"image is required", `env name "HOME" is set by the actor template`},
		},
		"contract env": {
			func(p *models.ProfileEntry) {
				p.Env = []models.EnvVar{{Name: "SESSION_ID"}, {Name: "AGENT_PROFILE"}, {Name: "GOPROXY"}}
			},
			[]string{
				`env name "SESSION_ID" is set by the actor template`,
				`env name "AGENT_PROFILE" is set by the actor template`,
			},
		},
		"env twice": {
			func(p *models.ProfileEntry) { p.Env = []models.EnvVar{{Name: "A"}, {Name: "A"}} },
			[]string{`env name "A" is listed twice`},
		},
		"fixed hosts": {
			func(p *models.ProfileEntry) { p.Egress = []string{"api.anthropic.com", "gitlab.127.0.0.1.nip.io"} },
			[]string{
				`egress host "api.anthropic.com" has a fixed egress rule`,
				`egress host "gitlab.127.0.0.1.nip.io" has a fixed egress rule`,
			},
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := goProfile()
			c.mod(&p)
			assert.Equal(t, c.want, CheckProfile(d, p))
		})
	}
}

func TestCheckProfileWithoutGitLabURL(t *testing.T) {
	d := goldenParams(t).Deployment
	d.GitLabURL = models.GitLabURL{}

	p := goProfile()
	p.Egress = []string{"api.anthropic.com", "gitlab.127.0.0.1.nip.io"}

	assert.Equal(t, []string{`egress host "api.anthropic.com" has a fixed egress rule`}, CheckProfile(d, p))
}

func TestProfileAndImageOf(t *testing.T) {
	tmpl, _, err := Render(goldenParams(t))
	require.NoError(t, err)

	assert.Equal(t, "go", ProfileOf(tmpl))
	assert.Equal(t, goProfile().Image, ImageOf(tmpl))
	assert.Equal(t, "00000000-0000-0000-0000-00000000000a", SessionOf(tmpl))

	empty := &ateapi.ActorTemplate{}
	assert.Empty(t, ProfileOf(empty))
	assert.Empty(t, ImageOf(empty))
	assert.Empty(t, SessionOf(empty))
}
