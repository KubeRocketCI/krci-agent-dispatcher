// Package testbedprobe is the harness subcommand testbed-probe: a long-running process that reports its
// identity, a heartbeat counter and the harness checks, for the image volume mount check on the testbed.
// The turn loop replaces it as the actor command.
package testbedprobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/harness"
)

const (
	// Addr is where the probe serves GET /state.
	Addr = ":8080"
	// Tick is the heartbeat interval.
	Tick = 2 * time.Second
	// TrustFile is the egress trust bundle the actor template mounts.
	TrustFile = "/run/ate/egress-trust-bundle/trust-bundle.pem"
)

// State is what GET /state and each heartbeat line report.
type State struct {
	// Instance is random per process start; the pid is 1 after every cold boot.
	Instance    string          `json:"instance"`
	PID         int             `json:"pid"`
	StartedAt   time.Time       `json:"started_at"`
	Now         time.Time       `json:"now"`
	Counter     int64           `json:"counter"`
	FileCounter int64           `json:"file_counter"`
	Executable  string          `json:"executable"`
	Checks      []harness.Check `json:"checks"`
	OK          bool            `json:"ok"`
}

// Probe keeps the heartbeat counter in memory and in a file, so a revert shows both memory and rootfs rewind.
type Probe struct {
	sys       harness.System
	heartbeat string
	trustFile string
	instance  string
	startedAt time.Time

	mu      sync.Mutex
	counter int64
}

// New returns a probe that writes its heartbeat file into workDir.
func New(sys harness.System, workDir, trustFile string) (*Probe, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("instance id: %w", err)
	}

	return &Probe{
		sys:       sys,
		heartbeat: filepath.Join(workDir, "heartbeat"),
		trustFile: trustFile,
		instance:  hex.EncodeToString(buf),
		startedAt: time.Now().UTC(),
	}, nil
}

// Beat advances the counter and writes it to the heartbeat file.
func (p *Probe) Beat() error {
	p.mu.Lock()
	p.counter++
	n := p.counter
	p.mu.Unlock()

	return os.WriteFile(p.heartbeat, []byte(strconv.FormatInt(n, 10)), 0o600)
}

// State runs the checks and reports the probe's state.
func (p *Probe) State() State {
	p.mu.Lock()
	n := p.counter
	p.mu.Unlock()

	exe, err := p.sys.Executable()
	if err != nil {
		exe = "error: " + err.Error()
	}

	checks := append(harness.ContractChecks(p.sys), harness.MountChecks(p.sys)...)

	_, err = os.Stat(p.trustFile)
	checks = append(checks, check("trust_bundle", err))

	return State{
		Instance:    p.instance,
		PID:         os.Getpid(),
		StartedAt:   p.startedAt,
		Now:         time.Now().UTC(),
		Counter:     n,
		FileCounter: p.fileCounter(),
		Executable:  exe,
		Checks:      checks,
		OK:          harness.AllOK(checks),
	}
}

func check(name string, err error) harness.Check {
	if err != nil {
		return harness.Check{Name: name, Detail: err.Error()}
	}

	return harness.Check{Name: name, OK: true}
}

// fileCounter is -1 when the heartbeat file is missing or unreadable.
func (p *Probe) fileCounter() int64 {
	b, err := os.ReadFile(p.heartbeat)
	if err != nil {
		return -1
	}

	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return -1
	}

	return n
}

// Handler serves GET /state.
func (p *Probe) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.State())
	})

	return mux
}

// Run beats every tick, writes one State line per beat to out and serves Handler on ln until ctx ends.
func Run(ctx context.Context, p *Probe, ln net.Listener, tick time.Duration, out io.Writer) error {
	srv := &http.Server{Handler: p.Handler(), ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)

	go func() { serveErr <- srv.Serve(ln) }()

	enc := json.NewEncoder(out)
	t := time.NewTicker(tick)

	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()

			return srv.Shutdown(shutdownCtx)
		case err := <-serveErr:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}

			return err
		case <-t.C:
			if err := p.Beat(); err != nil {
				_ = enc.Encode(map[string]string{"error": "heartbeat: " + err.Error()})
			}

			_ = enc.Encode(p.State())
		}
	}
}
