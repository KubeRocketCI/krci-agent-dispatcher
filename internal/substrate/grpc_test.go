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

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/substrate/substratetest"
)

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
func newGRPC(t *testing.T, f *substratetest.Fake) grpcEnv {
	t.Helper()

	cert, caPEM := serverCert(t)
	lis := bufconn.Listen(1 << 20)
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
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

	c, err := substrate.NewGRPC("passthrough:///api.ate-system.svc:443", env.tokenFile, env.caFile, atespace, env.dial)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	env.client = c

	return env
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

	c, err := substrate.NewGRPC("passthrough:///api.ate-system.svc:443", env.tokenFile, other, atespace, env.dial)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.List(context.Background(), "issue-")
	require.Error(t, err)
	assert.Empty(t, f.Authorizations(), "no token reaches an untrusted server")
}

func TestNewGRPCBadCAFile(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing.crt")
	_, err := substrate.NewGRPC("api.ate-system.svc:443", filepath.Join(dir, "token"), missing, atespace)
	require.Error(t, err)
}
