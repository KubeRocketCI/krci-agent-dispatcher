// Package procgroup runs a command as the leader of its own process group: signals go to the whole group, the rest
// of the group is killed when the leader exits, and the reads of its output are bounded after that.
package procgroup

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system"
)

// DefaultDrain is Options.Drain when zero.
const DefaultDrain = 2 * time.Second

// past is a deadline that has passed: I/O on a file with it fails at once.
var past = time.Unix(1, 0)

// Clock is time for the drain bound and the grace. WithDeadline returns a child of ctx that ends at t.
type Clock interface {
	Now() time.Time
	WithDeadline(ctx context.Context, t time.Time) (context.Context, context.CancelFunc)
}

// Options configure a Process.
type Options struct {
	// Clock times Drain and Grace; nil is the wall clock.
	Clock Clock
	// Drain bounds the reads of stdout and stderr after the leader exits; zero is DefaultDrain.
	Drain time.Duration
	// Grace is the time from TERM to KILL in Run; zero is KILL with TERM.
	Grace time.Duration
}

// Process is a started command in its own process group.
type Process struct {
	cmd    *exec.Cmd
	opts   Options
	exited chan struct{}

	mu   sync.Mutex
	gone bool

	stdin    *os.File
	outputs  []*os.File
	child    []*os.File
	inCopy   func()
	outCopy  []func()
	inDone   chan struct{}
	outsDone chan struct{}
}

// Start starts cmd as the leader of a new process group. Stdin, Stdout and Stderr that are set and not an *os.File
// go through pipes; one writer set as both Stdout and Stderr gets one pipe, so their order holds. A Stdin reader
// that blocks keeps its copy running after Wait.
func Start(cmd *exec.Cmd, o Options) (*Process, error) {
	if o.Clock == nil {
		o.Clock = system.Clock{}
	}

	if o.Drain <= 0 {
		o.Drain = DefaultDrain
	}

	p := &Process{
		cmd: cmd, opts: o, exited: make(chan struct{}),
		inDone: make(chan struct{}), outsDone: make(chan struct{}),
	}

	if err := p.pipes(); err != nil {
		p.closeAll()

		return nil, err
	}

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}

	cmd.SysProcAttr.Setpgid = true

	err := cmd.Start()

	for _, f := range p.child {
		_ = f.Close()
	}

	if err != nil {
		p.closeAll()

		return nil, fmt.Errorf("start: %w", err)
	}

	p.copy()

	return p, nil
}

// pipes replaces the stdio of cmd that is not an *os.File with pipes.
func (p *Process) pipes() error {
	cmd := p.cmd
	shared := cmd.Stderr != nil && sameWriter(cmd.Stdout, cmd.Stderr)

	if src := cmd.Stdin; src != nil {
		if _, ok := src.(*os.File); !ok {
			r, w, err := p.pipe()
			if err != nil {
				return err
			}

			cmd.Stdin, p.stdin, p.child = r, w, append(p.child, r)
			p.inCopy = func() {
				defer close(p.inDone)

				_, _ = io.Copy(w, src)
				_ = w.Close()
			}
		}
	}

	if p.stdin == nil {
		close(p.inDone)
	}

	stdout, err := p.output(cmd.Stdout)
	if err != nil {
		return err
	}

	cmd.Stdout = stdout

	if shared {
		cmd.Stderr = stdout

		return nil
	}

	stderr, err := p.output(cmd.Stderr)
	if err != nil {
		return err
	}

	cmd.Stderr = stderr

	return nil
}

// output returns the child end of a pipe copied to dst, or dst when it is nil or an *os.File.
func (p *Process) output(dst io.Writer) (io.Writer, error) {
	if dst == nil {
		return nil, nil
	}

	if f, ok := dst.(*os.File); ok {
		return f, nil
	}

	r, w, err := p.pipe()
	if err != nil {
		return nil, err
	}

	p.outputs, p.child = append(p.outputs, r), append(p.child, w)
	p.outCopy = append(p.outCopy, func() {
		if _, err := io.Copy(dst, r); err != nil {
			_, _ = io.Copy(io.Discard, r)
		}
	})

	return w, nil
}

func (p *Process) pipe() (*os.File, *os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("open pipe: %w", err)
	}

	return r, w, nil
}

// copy starts the copies of the pipes; outsDone closes when the output copies end.
func (p *Process) copy() {
	if p.inCopy != nil {
		go p.inCopy()
	}

	var outs sync.WaitGroup

	for _, c := range p.outCopy {
		outs.Go(c)
	}

	go func() {
		outs.Wait()
		close(p.outsDone)
	}()
}

func (p *Process) closeAll() {
	for _, f := range p.child {
		_ = f.Close()
	}

	for _, f := range p.outputs {
		_ = f.Close()
	}

	if p.stdin != nil {
		_ = p.stdin.Close()
	}
}

// Exited is closed once the leader has exited and Wait has reaped it.
func (p *Process) Exited() <-chan struct{} { return p.exited }

// Signal sends sig to the process group; it does nothing once Wait has seen the leader exit.
func (p *Process) Signal(sig syscall.Signal) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.gone {
		_ = syscall.Kill(-p.cmd.Process.Pid, sig)
	}
}

// Wait waits for the leader to exit, sends KILL to the rest of the group, reaps the leader, closes Exited, stops
// writing stdin and reads stdout and stderr until EOF or Drain. It returns the error of exec.Cmd.Wait; the process
// state is in cmd.ProcessState. Call it once.
//
// On Linux and Darwin the KILL goes out while the exited leader is unreaped, so the group id is not reused yet;
// elsewhere it goes out after the reap.
func (p *Process) Wait() error {
	var err error

	if awaitExit(p.cmd.Process.Pid) {
		p.killRest()
		err = p.cmd.Wait()
	} else {
		err = p.cmd.Wait()
		p.killRest()
	}

	close(p.exited)

	if p.stdin != nil {
		_ = p.stdin.SetWriteDeadline(past)
	}

	p.drain()

	return err
}

// killRest ends Signal and sends KILL to the process group.
func (p *Process) killRest() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.gone = true
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
}

func (p *Process) drain() {
	ctx, cancel := p.opts.Clock.WithDeadline(context.Background(), p.opts.Clock.Now().Add(p.opts.Drain))
	defer cancel()

	select {
	case <-p.outsDone:
	case <-ctx.Done():
		for _, r := range p.outputs {
			_ = r.SetReadDeadline(past)
		}

		<-p.outsDone
	}

	select {
	case <-p.inDone:
	case <-ctx.Done():
	}

	for _, r := range p.outputs {
		_ = r.Close()
	}
}

// Run starts cmd with Start and waits for it. When ctx ends first, the group gets TERM, then KILL Grace later
// unless the leader exited. It returns the error of Wait, or the error of ctx when ctx ended before the start.
func Run(ctx context.Context, cmd *exec.Cmd, o Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	p, err := Start(cmd, o)
	if err != nil {
		return err
	}

	watched := make(chan struct{})

	go func() {
		defer close(watched)

		p.watch(ctx)
	}()

	err = p.Wait()

	<-watched

	return err
}

// watch sends TERM when ctx ends before the leader exits and KILL Grace later.
func (p *Process) watch(ctx context.Context) {
	select {
	case <-p.exited:
		return
	case <-ctx.Done():
	}

	p.Signal(syscall.SIGTERM)

	kill, cancel := p.opts.Clock.WithDeadline(context.WithoutCancel(ctx), p.opts.Clock.Now().Add(p.opts.Grace))
	defer cancel()

	select {
	case <-p.exited:
	case <-kill.Done():
		p.Signal(syscall.SIGKILL)
	}
}

// sameWriter reports whether a and b are the same writer; false for writers that are not comparable.
func sameWriter(a, b io.Writer) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()

	return a == b
}
