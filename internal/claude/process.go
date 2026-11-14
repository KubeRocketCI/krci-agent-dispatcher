package claude

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/procgroup"
)

// process is a started claude in its own process group.
type process struct {
	*procgroup.Process

	cmd    *exec.Cmd
	stdout *io.PipeWriter
	stream chan stream
}

// start starts cmd with prompt on stdin, reads stdout as a stream of conversation with onInit and copies stderr to
// errOut; clock times the drain of the pipes.
func start(
	cmd *exec.Cmd, prompt, conversation string, onInit func() error, errOut io.Writer, clock procgroup.Clock,
) (*process, error) {
	r, w := io.Pipe()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(prompt), w, errOut

	pg, err := procgroup.Start(cmd, procgroup.Options{Clock: clock})
	if err != nil {
		_ = w.Close()

		return nil, err
	}

	p := &process{Process: pg, cmd: cmd, stdout: w, stream: make(chan stream, 1)}

	go func() { p.stream <- readStream(r, conversation, onInit) }()

	return p, nil
}

// wait waits for the process group with procgroup.Process.Wait and returns the stream and the process state; the
// state is nil when the wait failed.
func (p *process) wait() (stream, *os.ProcessState, error) {
	err := p.Wait()

	_ = p.stdout.Close()
	s := <-p.stream

	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return s, nil, fmt.Errorf("wait: %w", err)
	}

	return s, p.cmd.ProcessState, nil
}
