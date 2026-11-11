package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
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
			Pool:             "workers",
			SecretsNamespace: "krci-agents",
			Image:            "localhost:5001/krci-agent-go@sha256:" + strings.Repeat("0", 64),
			GitLabURL:        gitLabURL(t, "https://gitlab.127.0.0.1.nip.io"),
		},
		ActorSpec: models.ActorSpec{
			ProjectPath: "krci/test-go-app",
			IID:         7,
			SessionID:   "00000000-0000-0000-0000-00000000000a",
			BotUsername: "krci-agent",
		},
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
	assert.Equal(t, "Authorization: Basic placeholder", env(tmpl, "GIT_CONFIG_VALUE_0"))

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

func TestRenderQuotesPayloadValues(t *testing.T) {
	p := goldenParams(t)
	p.ProjectPath = "krci/app\"\n  injected: true"

	tmpl, _, err := Render(p)
	require.NoError(t, err)
	assert.Equal(t, p.ProjectPath, env(tmpl, "PROJECT_PATH"))
}

func TestRenderGitLabURLForms(t *testing.T) {
	tests := []struct {
		in, url, key, host string
	}{
		{
			in:   "https://gitlab.example.com/",
			url:  "https://gitlab.example.com",
			key:  "http.https://gitlab.example.com/.extraHeader",
			host: "gitlab.example.com",
		},
		{
			in:   "https://gitlab.example.com/gitlab",
			url:  "https://gitlab.example.com/gitlab",
			key:  "http.https://gitlab.example.com/gitlab/.extraHeader",
			host: "gitlab.example.com",
		},
		{
			in:   "https://gitlab.example.com/gitlab/",
			url:  "https://gitlab.example.com/gitlab",
			key:  "http.https://gitlab.example.com/gitlab/.extraHeader",
			host: "gitlab.example.com",
		},
		{
			in:   "https://GitLab.Example.com:8443/gitlab//",
			url:  "https://gitlab.example.com:8443/gitlab",
			key:  "http.https://gitlab.example.com:8443/gitlab/.extraHeader",
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
			assert.Equal(t, tt.key, env(tmpl, "GIT_CONFIG_KEY_0"))
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
