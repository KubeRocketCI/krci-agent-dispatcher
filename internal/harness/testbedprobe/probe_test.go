package testbedprobe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/harness"
)

// mounted is a System with the harness mounted read-only and every check passing.
func mounted(t *testing.T) harness.System {
	t.Helper()

	home := t.TempDir()

	return harness.System{
		UID:        func() int { return harness.AgentUID },
		Getenv:     func(key string) string { return map[string]string{"HOME": home}[key] },
		Executable: func() (string, error) { return harness.Command, nil },
		LookPath:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
		ReadDir:    func(string) ([]fs.DirEntry, error) { return nil, nil },
		WriteProbe: func(dir string) error {
			if dir == harness.MountPath {
				return syscall.EACCES
			}

			return nil
		},
		MountInfo: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("40 30 0:41 / /opt/krci-agent ro - overlay overlay ro\n")), nil
		},
	}
}

func newProbe(t *testing.T, trust bool) *Probe {
	t.Helper()

	trustFile := filepath.Join(t.TempDir(), "trust-bundle.pem")
	if trust {
		if err := os.WriteFile(trustFile, []byte("pem"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	p, err := New(mounted(t), t.TempDir(), trustFile)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func TestState(t *testing.T) {
	p := newProbe(t, true)

	s := p.State()
	if !s.OK || s.Counter != 0 || s.FileCounter != -1 || s.Executable != harness.Command || len(s.Instance) != 16 {
		t.Fatalf("initial state = %+v", s)
	}

	for range 3 {
		if err := p.Beat(); err != nil {
			t.Fatal(err)
		}
	}

	if s := p.State(); s.Counter != 3 || s.FileCounter != 3 {
		t.Fatalf("after 3 beats: counter %d, file counter %d", s.Counter, s.FileCounter)
	}
}

func TestStateFailsWithoutTrustBundle(t *testing.T) {
	s := newProbe(t, false).State()
	if s.OK {
		t.Fatal("want not ok")
	}

	last := s.Checks[len(s.Checks)-1]
	if last.Name != "trust_bundle" || last.OK {
		t.Fatalf("last check = %+v", last)
	}
}

func TestStateExecutableError(t *testing.T) {
	sys := mounted(t)
	sys.Executable = func() (string, error) { return "", errors.New("no exe") }

	p, err := New(sys, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}

	if s := p.State(); s.Executable != "error: no exe" || s.OK {
		t.Fatalf("state = %+v", s)
	}
}

func TestFileCounterUnparsable(t *testing.T) {
	p := newProbe(t, true)
	if err := os.WriteFile(p.heartbeat, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if n := p.fileCounter(); n != -1 {
		t.Fatalf("file counter = %d, want -1", n)
	}
}

func TestHandler(t *testing.T) {
	srv := httptest.NewServer(newProbe(t, true).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/state")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	var s State
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusOK || !s.OK {
		t.Fatalf("status %d, state %+v", resp.StatusCode, s)
	}
}

// syncBuffer is an io.Writer safe for Run's goroutine and the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []string

	sc := bufio.NewScanner(bytes.NewReader(b.buf.Bytes()))
	for sc.Scan() {
		out = append(out, sc.Text())
	}

	return out
}

func TestRun(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	out := &syncBuffer{}
	done := make(chan error, 1)

	go func() { done <- Run(ctx, newProbe(t, true), ln, 10*time.Millisecond, out) }()

	deadline := time.Now().Add(5 * time.Second)
	for len(out.lines()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	resp, err := http.Get("http://" + ln.Addr().String() + "/state")
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	lines := out.lines()
	if len(lines) < 2 {
		t.Fatalf("heartbeat lines = %d, want >= 2", len(lines))
	}

	var s State
	if err := json.Unmarshal([]byte(lines[1]), &s); err != nil || s.Counter != 2 {
		t.Fatalf("second line = %s (%v)", lines[1], err)
	}
}

func TestRunServeError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	_ = ln.Close()

	if err := Run(t.Context(), newProbe(t, true), ln, time.Hour, io.Discard); err == nil {
		t.Fatal("want serve error on a closed listener")
	}
}

func TestRunHeartbeatError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	p, err := New(mounted(t), filepath.Join(t.TempDir(), "absent"), "")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	out := &syncBuffer{}
	done := make(chan error, 1)

	go func() { done <- Run(ctx, p, ln, 10*time.Millisecond, out) }()

	deadline := time.Now().Add(5 * time.Second)
	for len(out.lines()) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	if lines := out.lines(); len(lines) == 0 || !strings.Contains(lines[0], "heartbeat:") {
		t.Fatalf("lines = %v, want a heartbeat error first", lines)
	}
}
