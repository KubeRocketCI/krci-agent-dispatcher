package substrate_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/test/bufconn"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/substratetest"
)

var deployment = substrate.Deployment{
	Atespace:         atespace,
	Pool:             "workers",
	SecretsNamespace: "agent-secrets",
	Image:            "localhost:5001/krci-agent-go:dev",
	GitLabURL:        mustGitLabURL("https://GitLab.Example/"),
}

func mustGitLabURL(raw string) models.GitLabURL {
	u, err := models.ParseGitLabURL(raw)
	if err != nil {
		panic(err)
	}

	return u
}

type grpcEnv struct {
	client    *substrate.GRPC
	tokenFile string
	caFile    string
	dial      grpc.DialOption
}

// serverCert returns a TLS certificate for api.ate-system.svc and its PEM, self-signed.
func serverCert(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "api.ate-system.svc"},
		DNSNames:     []string{"api.ate-system.svc"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,

		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	require.NoError(t, err)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// newGRPC serves f over bufconn with TLS and returns a GRPC adapter dialed to it.
func newGRPC(t *testing.T, f *substratetest.Fake, opts ...grpc.ServerOption) grpcEnv {
	t.Helper()

	cert, caPEM := serverCert(t)
	lis := bufconn.Listen(1 << 20)
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv := grpc.NewServer(append([]grpc.ServerOption{grpc.Creds(credentials.NewTLS(tlsCfg))}, opts...)...)
	ateapi.RegisterControlServer(srv, f.Server())

	go func() { _ = srv.Serve(lis) }()

	t.Cleanup(srv.Stop)

	dir := t.TempDir()
	env := grpcEnv{tokenFile: filepath.Join(dir, "token"), caFile: filepath.Join(dir, "ca.crt")}
	require.NoError(t, os.WriteFile(env.tokenFile, []byte("token-one\n"), 0o600))
	require.NoError(t, os.WriteFile(env.caFile, caPEM, 0o600))

	env.dial = grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})

	c, err := substrate.NewGRPC("passthrough:///api.ate-system.svc:443", env.tokenFile, env.caFile, deployment, env.dial)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	env.client = c

	return env
}

func TestCallDeadline(t *testing.T) {
	cases := map[string]struct {
		parent  time.Duration
		atMost  time.Duration
		atLeast time.Duration
	}{
		"none":           {atMost: 30 * time.Second, atLeast: 25 * time.Second},
		"longer parent":  {parent: time.Hour, atMost: 30 * time.Second, atLeast: 25 * time.Second},
		"shorter parent": {parent: 5 * time.Second, atMost: 5 * time.Second, atLeast: time.Second},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var left time.Duration

			record := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
				if d, ok := ctx.Deadline(); ok {
					left = time.Until(d)
				}

				return h(ctx, req)
			}

			env := newGRPC(t, substratetest.NewFake(atespace), grpc.UnaryInterceptor(record))

			ctx := context.Background()

			if c.parent > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, c.parent)
				t.Cleanup(cancel)
			}

			_, err := env.client.List(ctx, "issue-")
			require.NoError(t, err)
			assert.LessOrEqual(t, left, c.atMost)
			assert.GreaterOrEqual(t, left, c.atLeast)
		})
	}
}

func TestBearerTokenRereadPerCall(t *testing.T) {
	f := substratetest.NewFake(atespace)
	env := newGRPC(t, f)
	ctx := context.Background()

	_, err := env.client.List(ctx, "issue-")
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(env.tokenFile, []byte("token-two"), 0o600))
	_, err = env.client.List(ctx, "issue-")
	require.NoError(t, err)

	assert.Equal(t, []string{"Bearer token-one", "Bearer token-two"}, f.Authorizations())
}

func TestEmptyTokenFails(t *testing.T) {
	f := substratetest.NewFake(atespace)
	env := newGRPC(t, f)
	require.NoError(t, os.WriteFile(env.tokenFile, nil, 0o600))

	_, err := env.client.List(context.Background(), "issue-")
	require.Error(t, err)
	assert.Empty(t, f.Authorizations())
}

func TestUntrustedServerFails(t *testing.T) {
	f := substratetest.NewFake(atespace)
	env := newGRPC(t, f)

	_, otherCA := serverCert(t)
	other := filepath.Join(t.TempDir(), "other.crt")
	require.NoError(t, os.WriteFile(other, otherCA, 0o600))

	c, err := substrate.NewGRPC("passthrough:///api.ate-system.svc:443", env.tokenFile, other, deployment, env.dial)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.List(context.Background(), "issue-")
	require.Error(t, err)
	assert.Empty(t, f.Authorizations(), "no token reaches an untrusted server")
}

func TestNewGRPCBadCAFile(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing.crt")
	_, err := substrate.NewGRPC("api.ate-system.svc:443", filepath.Join(dir, "token"), missing, deployment)
	require.Error(t, err)
}

func TestNewGRPCEmptyGitLabURL(t *testing.T) {
	f := substratetest.NewFake(atespace)
	env := newGRPC(t, f)

	d := deployment
	d.GitLabURL = models.GitLabURL{}

	_, err := substrate.NewGRPC("passthrough:///api.ate-system.svc:443", env.tokenFile, env.caFile, d, env.dial)
	require.Error(t, err)
}

func TestCreateRenderErrorCallsNothing(t *testing.T) {
	f := substratetest.NewFake(atespace)
	env := newGRPC(t, f)

	s := spec
	s.ProjectPath = ""

	err := env.client.Create(context.Background(), actor, s)
	require.ErrorIs(t, err, models.ErrInvalidSpec)
	assert.ErrorContains(t, err, "project path")
	assert.Empty(t, f.Ops())
	assert.Nil(t, f.SentTemplate(actor))
	assert.Empty(t, f.Authorizations(), "no call reaches the server")
}

func envValue(tmpl *ateapi.ActorTemplate, name string) string {
	for _, e := range tmpl.GetContainers()[0].GetEnv() {
		if e.GetName() == name {
			return e.GetValue()
		}
	}

	return ""
}

func TestCreateRendersWithDeployment(t *testing.T) {
	f := substratetest.NewFake(atespace)
	env := newGRPC(t, f)

	s := models.ActorSpec{ProjectPath: "team/app", IID: 3, SessionID: "session-9", BotUsername: "agent-bot"}
	require.NoError(t, env.client.Create(context.Background(), "issue-40-3", s))

	tmpl := f.SentTemplate("issue-40-3")
	require.NotNil(t, tmpl)
	assert.Equal(t, atespace, tmpl.GetMetadata().GetAtespace())
	assert.Equal(t, "issue-40-3", tmpl.GetMetadata().GetName())
	assert.Equal(t, "workers", tmpl.GetWorkerSelector().GetMatchLabels()["workload"])
	assert.Equal(t, deployment.Image, tmpl.GetContainers()[0].GetImage())
	assert.Equal(t, "https://gitlab.example", envValue(tmpl, "GITLAB_URL"))
	assert.Equal(t, "team/app", envValue(tmpl, "PROJECT_PATH"))
	assert.Equal(t, "3", envValue(tmpl, "ISSUE_IID"))
	assert.Equal(t, "session-9", envValue(tmpl, "SESSION_ID"))
	assert.Equal(t, "agent-bot", envValue(tmpl, "BOT_USERNAME"))

	pol := f.SentEgressPolicy("issue-40-3")
	require.NotNil(t, pol)
	assert.Equal(t, atespace, pol.GetMetadata().GetAtespace())
	assert.Equal(t, "default", pol.GetMetadata().GetName())
	assert.Equal(t, []string{"gitlab.example"}, pol.GetRules()[1].GetHttps().GetHostnames())

	for _, r := range pol.GetRules() {
		for _, h := range r.GetHttps().GetEffects().GetReplaceHeaders() {
			assert.Contains(t, h.GetCredentialUri(), "ate-secret://k8s.io/default/agent-secrets/")
		}
	}
}
