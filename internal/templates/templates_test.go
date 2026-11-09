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
)

func goldenParams() Params {
	return Params{
		Atespace:         "krci-agents",
		Pool:             "workers",
		SecretsNamespace: "krci-agents",
		Name:             "issue-12-7",
		Image:            "localhost:5001/krci-agent-go@sha256:" + strings.Repeat("0", 64),
		GitLabURL:        "https://gitlab.127.0.0.1.nip.io",
		ProjectPath:      "krci/test-go-app",
		BotUsername:      "krci-agent",
		SessionID:        "00000000-0000-0000-0000-00000000000a",
		IID:              7,
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
	tmpl, pol, err := Render(goldenParams())
	require.NoError(t, err)

	var wantTmpl ateapi.ActorTemplate

	var wantPol ateapi.EgressPolicy

	loadGolden(t, "actortemplate.golden.yaml", &wantTmpl)
	loadGolden(t, "egresspolicy.golden.yaml", &wantPol)

	assert.True(t, proto.Equal(&wantTmpl, tmpl), "actor template differs:\nwant %v\ngot  %v", &wantTmpl, tmpl)
	assert.True(t, proto.Equal(&wantPol, pol), "egress policy differs:\nwant %v\ngot  %v", &wantPol, pol)
}

func TestRenderValues(t *testing.T) {
	p := goldenParams()
	p.Name, p.IID, p.ProjectPath = "issue-40-3", 3, "team/other-app"

	tmpl, pol, err := Render(p)
	require.NoError(t, err)

	assert.Equal(t, "issue-40-3", tmpl.GetMetadata().GetName())
	assert.Equal(t, "krci-agents", tmpl.GetMetadata().GetAtespace())
	assert.Equal(t, "3", env(tmpl, "ISSUE_IID"))
	assert.Equal(t, "team/other-app", env(tmpl, "PROJECT_PATH"))
	assert.Equal(t, "workers", tmpl.GetWorkerSelector().GetMatchLabels()["workload"])
	assert.Equal(t, "default", pol.GetMetadata().GetName())
	assert.Equal(t, []string{"gitlab.127.0.0.1.nip.io"}, pol.GetRules()[1].GetHttps().GetHostnames())
}

func TestRenderHasNoSecretValue(t *testing.T) {
	tmpl, pol, err := Render(goldenParams())
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
	p := goldenParams()
	p.ProjectPath = "krci/app\"\n  injected: true"

	tmpl, _, err := Render(p)
	require.NoError(t, err)
	assert.Equal(t, p.ProjectPath, env(tmpl, "PROJECT_PATH"))
}

func TestRenderRejectsBadGitLabURL(t *testing.T) {
	p := goldenParams()
	p.GitLabURL = "not a url"

	_, _, err := Render(p)
	require.Error(t, err)
}
