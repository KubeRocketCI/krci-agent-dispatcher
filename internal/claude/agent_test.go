package claude_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/claude"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
)

const (
	session      = "s-1A"
	systemPrompt = "Work on the issue.\nKeep it short."
	model        = "claude-test-model"
	steps        = 7
)

// prelude records each call of the stub in <rec>/<n>.{argv,stdin,pwd,env}, sets $c to the conversation id of the
// argv and defines init and result.
const prelude = `#!/bin/sh
rec=%REC%
n=$(( $(cat "$rec/count" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$rec/count"
for a in "$@"; do printf '%s\0' "$a"; done > "$rec/$n.argv"
cat > "$rec/$n.stdin"
pwd -P > "$rec/$n.pwd"
printf '%s' "$KRCI_CLAUDE_TEST_ENV" > "$rec/$n.env"
c=
while [ $# -gt 0 ]; do
  case $1 in --session-id|--resume) c=$2 ;; esac
  shift
done
init() { printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$c"; }
result() { printf '{"type":"result","subtype":"success","is_error":false,"result":"%s"}\n' "$1"; }
`

type stub struct {
	path string
	rec  string
}

// newStub writes a stub claude that runs body after the prelude.
func newStub(t *testing.T, body string) stub {
	t.Helper()

	dir := t.TempDir()
	rec := filepath.Join(dir, "rec")

	if err := os.Mkdir(rec, 0o700); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "claude")
	script := strings.ReplaceAll(prelude, "%REC%", rec) + body + "\n"

	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	return stub{path: path, rec: rec}
}

func (s stub) file(t *testing.T, name string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(s.rec, name))
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func (s stub) argv(t *testing.T, n int) []string {
	t.Helper()

	return strings.Split(strings.TrimSuffix(s.file(t, strconv.Itoa(n)+".argv"), "\x00"), "\x00")
}

// conversation returns the flag and the id that select the conversation in call n.
func (s stub) conversation(t *testing.T, n int) (string, string) {
	t.Helper()

	argv := s.argv(t, n)
	if len(argv) < 2 {
		t.Fatalf("argv %q has no conversation flag", argv)
	}

	return argv[len(argv)-2], argv[len(argv)-1]
}

type agentOptions struct {
	stateDir string
	session  string
	termLead time.Duration
	stderr   *bytes.Buffer
	clock    claude.Clock
}

func newAgent(t *testing.T, s stub, o agentOptions) *claude.Agent {
	t.Helper()

	if o.stateDir == "" {
		o.stateDir = filepath.Join(t.TempDir(), ".krci-agent")
	}

	if o.session == "" {
		o.session = session
	}

	if o.clock == nil {
		o.clock = system.Clock{}
	}

	opts := claude.Options{
		Command:      s.path,
		Dir:          t.TempDir(),
		Model:        model,
		Steps:        steps,
		SystemPrompt: systemPrompt,
		Session:      o.session,
		StateDir:     o.stateDir,
		Clock:        o.clock,
		TermLead:     o.termLead,
	}
	if o.stderr != nil {
		opts.Stderr = o.stderr
	}

	a, err := claude.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return a
}

func run(t *testing.T, a *claude.Agent, prompt string) (string, int) {
	t.Helper()

	text, rc, err := a.Run(t.Context(), prompt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	return text, rc
}

func TestRunPassesArgvStdinDirAndEnv(t *testing.T) {
	t.Setenv("KRCI_CLAUDE_TEST_ENV", "inherited")

	s := newStub(t, `init; result done`)
	dir := t.TempDir()

	a, err := claude.New(claude.Options{
		Command:      s.path,
		Dir:          dir,
		Model:        model,
		Steps:        steps,
		SystemPrompt: systemPrompt,
		Session:      session,
		StateDir:     filepath.Join(t.TempDir(), ".krci-agent"),
		Clock:        system.Clock{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	prompt := "Fix the bug.\nSee the comments."
	run(t, a, prompt)

	argv := s.argv(t, 1)
	want := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--max-turns", "7", "--model", model,
		"--permission-mode", "bypassPermissions", "--disallowedTools", "Bash(git push:*)",
		"--append-system-prompt", systemPrompt, "--session-id",
	}

	if len(argv) != len(want)+1 || !slices.Equal(argv[:len(want)], want) {
		t.Fatalf("argv = %q, want %q followed by the conversation id", argv, want)
	}

	id, err := uuid.Parse(argv[len(want)])
	if err != nil || id.Version() != 5 {
		t.Fatalf("conversation id %q: want a uuid v5, err %v", argv[len(want)], err)
	}

	if got := s.file(t, "1.stdin"); got != prompt {
		t.Fatalf("stdin = %q, want %q", got, prompt)
	}

	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.TrimSpace(s.file(t, "1.pwd")); got != wantDir {
		t.Fatalf("dir = %q, want %q", got, wantDir)
	}

	if got := s.file(t, "1.env"); got != "inherited" {
		t.Fatalf("env = %q, want inherited", got)
	}
}

func TestRunResumesTheConversationAfterInit(t *testing.T) {
	t.Parallel()

	s := newStub(t, `init; result done`)
	stateDir := filepath.Join(t.TempDir(), ".krci-agent")

	run(t, newAgent(t, s, agentOptions{stateDir: stateDir}), "one")
	run(t, newAgent(t, s, agentOptions{stateDir: stateDir}), "two")

	flag1, id1 := s.conversation(t, 1)
	flag2, id2 := s.conversation(t, 2)

	if flag1 != "--session-id" || flag2 != "--resume" || id1 != id2 {
		t.Fatalf("runs = %s %s, %s %s; want --session-id C, --resume C", flag1, id1, flag2, id2)
	}

	info, err := os.Stat(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestRunSameSessionSameConversation(t *testing.T) {
	t.Parallel()

	s := newStub(t, `init; result done`)

	run(t, newAgent(t, s, agentOptions{}), "one")
	run(t, newAgent(t, s, agentOptions{}), "two")

	_, id1 := s.conversation(t, 1)
	_, id2 := s.conversation(t, 2)

	if id1 != id2 {
		t.Fatalf("conversation ids %s, %s; want the same id for the same session", id1, id2)
	}
}

func TestRunAnotherSessionStartsANewConversation(t *testing.T) {
	t.Parallel()

	s := newStub(t, `init; result done`)
	stateDir := filepath.Join(t.TempDir(), ".krci-agent")

	run(t, newAgent(t, s, agentOptions{stateDir: stateDir}), "one")
	run(t, newAgent(t, s, agentOptions{stateDir: stateDir, session: "s-2B"}), "two")

	_, id1 := s.conversation(t, 1)
	flag2, id2 := s.conversation(t, 2)

	if flag2 != "--session-id" || id2 == id1 {
		t.Fatalf("second run = %s %s after %s; want --session-id with another id", flag2, id2, id1)
	}
}

func TestRunWithoutInitStartsANewConversationAgain(t *testing.T) {
	t.Parallel()

	s := newStub(t, `result done`)
	a := newAgent(t, s, agentOptions{})

	run(t, a, "one")
	run(t, a, "two")

	if flag, _ := s.conversation(t, 2); flag != "--session-id" {
		t.Fatalf("second run flag = %s, want --session-id", flag)
	}
}

func TestRunInitOfAnotherConversationStartsANewConversationAgain(t *testing.T) {
	t.Parallel()

	s := newStub(t, `c=00000000-0000-0000-0000-000000000000; init; result done`)
	a := newAgent(t, s, agentOptions{})

	run(t, a, "one")
	run(t, a, "two")

	if flag, _ := s.conversation(t, 2); flag != "--session-id" {
		t.Fatalf("second run flag = %s, want --session-id", flag)
	}
}

func TestFreshBeforeAnyRun(t *testing.T) {
	t.Parallel()

	if !newAgent(t, newStub(t, `init; result done`), agentOptions{}).Fresh() {
		t.Fatal("Fresh before any run = false, want true")
	}
}

func TestFreshFalseAfterARunWithInit(t *testing.T) {
	t.Parallel()

	s := newStub(t, `init; result done`)
	stateDir := filepath.Join(t.TempDir(), ".krci-agent")
	a := newAgent(t, s, agentOptions{stateDir: stateDir})

	run(t, a, "one")

	if a.Fresh() {
		t.Fatal("Fresh after a run with init = true, want false")
	}

	if newAgent(t, s, agentOptions{stateDir: stateDir}).Fresh() {
		t.Fatal("Fresh of a new agent on the same state dir = true, want false")
	}
}

func TestFreshAfterARunWithoutInit(t *testing.T) {
	t.Parallel()

	a := newAgent(t, newStub(t, `result done`), agentOptions{})

	run(t, a, "one")

	if !a.Fresh() {
		t.Fatal("Fresh after a run without init = false, want true")
	}
}

func TestFreshForAnotherSession(t *testing.T) {
	t.Parallel()

	s := newStub(t, `init; result done`)
	stateDir := filepath.Join(t.TempDir(), ".krci-agent")

	run(t, newAgent(t, s, agentOptions{stateDir: stateDir}), "one")

	if !newAgent(t, s, agentOptions{stateDir: stateDir, session: "s-2B"}).Fresh() {
		t.Fatal("Fresh for another session = false, want true")
	}
}

func TestFreshWhenTheConversationFileCannotBeRead(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(stateDir, "conversation"), 0o700); err != nil {
		t.Fatal(err)
	}

	if !newAgent(t, newStub(t, `init; result done`), agentOptions{stateDir: stateDir}).Fresh() {
		t.Fatal("Fresh with an unreadable conversation file = false, want true")
	}
}

func TestRunRecordsTheConversationAtInit(t *testing.T) {
	s := newStub(t, `trap 'exit 143' TERM
init
: > "$rec/ready"
sleep 30 &
wait`)
	stateDir := filepath.Join(t.TempDir(), ".krci-agent")
	clk, done := virtualRun(t, s, agentOptions{stateDir: stateDir, termLead: 30 * time.Second}, "one")

	awaitFile(t, filepath.Join(s.rec, "ready"))

	other := newAgent(t, s, agentOptions{stateDir: stateDir})
	recorded := false

	for end := time.Now().Add(settleBy); !recorded && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		recorded = !other.Fresh()
	}

	clk.Advance(30 * time.Second)

	if err := returned(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want an error wrapping context.DeadlineExceeded", err)
	}

	if !recorded {
		t.Fatal("conversation not recorded while the run after init was under way")
	}
}

func TestRunReturnsTheLastResultEvent(t *testing.T) {
	t.Parallel()

	s := newStub(t, `init
echo 'not json'
echo '{"type":"assistant","message":{"content":[]}}'
result first
echo '["type","result"]'
printf '%s\n' '{"type":"result","subtype":"success","result":"last\nline"}'
echo 'trailing noise'`)

	text, rc := run(t, newAgent(t, s, agentOptions{}), "go")

	if text != "last\nline" || rc != 0 {
		t.Fatalf("Run = %q, %d; want %q, 0", text, rc, "last\nline")
	}
}

func TestRunReturnsANonZeroExitCodeWithItsResult(t *testing.T) {
	t.Parallel()

	s := newStub(t, `init; result partial; exit 3`)

	text, rc := run(t, newAgent(t, s, agentOptions{}), "go")

	if text != "partial" || rc != 3 {
		t.Fatalf("Run = %q, %d; want partial, 3", text, rc)
	}
}

func TestRunReturnsANonZeroExitCodeWithoutAResult(t *testing.T) {
	t.Parallel()

	s := newStub(t, `init; exit 2`)

	text, rc := run(t, newAgent(t, s, agentOptions{}), "go")

	if text != "" || rc != 2 {
		t.Fatalf("Run = %q, %d; want empty text, 2", text, rc)
	}
}

func TestRunFailsOnExitZeroWithoutAResult(t *testing.T) {
	t.Parallel()

	s := newStub(t, `init; exit 0`)

	if _, _, err := newAgent(t, s, agentOptions{}).Run(t.Context(), "go"); err == nil {
		t.Fatal("Run without a result event: want an error")
	}
}

func TestRunFailsWhenEndedByASignal(t *testing.T) {
	t.Parallel()

	s := newStub(t, `init; result done; kill -KILL $$`)

	if _, _, err := newAgent(t, s, agentOptions{}).Run(t.Context(), "go"); err == nil {
		t.Fatal("Run ended by a signal: want an error")
	}
}

func TestRunFailsWhenTheCommandDoesNotStart(t *testing.T) {
	t.Parallel()

	s := stub{path: filepath.Join(t.TempDir(), "missing")}

	if _, _, err := newAgent(t, s, agentOptions{}).Run(t.Context(), "go"); err == nil {
		t.Fatal("Run of a missing command: want an error")
	}
}

func TestRunCopiesStderr(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer

	s := newStub(t, `echo 'agent says' >&2; result done`)

	run(t, newAgent(t, s, agentOptions{stderr: &stderr}), "go")

	if got := stderr.String(); got != "agent says\n" {
		t.Fatalf("stderr = %q, want %q", got, "agent says\n")
	}
}

func TestRunReadsLongLinesAndSkipsLinesOver16MiB(t *testing.T) {
	t.Parallel()

	s := newStub(t, `printf '{"type":"result","result":"'; head -c 1048576 /dev/zero | tr '\0' a; printf '"}\n'
head -c 16777300 /dev/zero | tr '\0' b; echo
echo 'after'`)

	text, _ := run(t, newAgent(t, s, agentOptions{}), "go")

	if len(text) != 1<<20 || strings.Trim(text, "a") != "" {
		t.Fatalf("result text: %d bytes, want 1 MiB of a", len(text))
	}

	s = newStub(t, `printf '{"type":"result","result":"'; head -c 16777300 /dev/zero | tr '\0' b; printf '"}\n'
result short`)

	if text, _ := run(t, newAgent(t, s, agentOptions{}), "go"); text != "short" {
		t.Fatalf("result text after an over-long line: %d bytes, want short", len(text))
	}
}

func TestRunFailsWhenTheConversationFileCannotBeWritten(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only dir")
	}

	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })

	s := newStub(t, `init; result done`)

	if _, _, err := newAgent(t, s, agentOptions{stateDir: stateDir}).Run(t.Context(), "go"); err == nil {
		t.Fatal("Run with an unwritable state dir: want an error")
	}
}

func TestRunFailsWithoutStartingWhenTheConversationFileCannotBeRead(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(stateDir, "conversation"), 0o700); err != nil {
		t.Fatal(err)
	}

	s := newStub(t, `init; result done`)

	if _, _, err := newAgent(t, s, agentOptions{stateDir: stateDir}).Run(t.Context(), "go"); err == nil {
		t.Fatal("Run with an unreadable conversation file: want an error")
	}

	if _, err := os.Stat(filepath.Join(s.rec, "count")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agent started: %v", err)
	}
}

// Deadline tests run on virtual time: the stub writes <rec>/ready once its trap is set, then the test moves the clock.
// Watchdog tests do not call t.Parallel: TestRunCancelledTermsThenKillsAfterTermLead measures real time.
// A stub that must run its TERM trap ends on a foreground loop, not on wait: TERM to the group also ends the child,
// and sh can leave wait and exit before it runs the trap.

// ignoreTerm records each TERM in <rec>/term and goes on; its child ignores TERM, holds stdout and writes
// <rec>/child and <rec>/ready once it ignores TERM.
const ignoreTerm = `trap 'echo term >> "$rec/term"' TERM
init
sh -c "trap '' TERM; echo \$\$ > '$rec/child'; : > '$rec/ready'; exec sleep 30" &
child=$!
while kill -0 "$child" 2>/dev/null; do wait "$child"; done`

// virtualRun starts Run under a virtual deadline one minute ahead and returns the clock and the result channel.
func virtualRun(t *testing.T, s stub, o agentOptions, prompt string) (*systemtest.Clock, <-chan error) {
	t.Helper()

	clk := systemtest.NewClock(time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC))
	o.clock = clk
	a := newAgent(t, s, o)

	ctx, cancel := clk.WithDeadline(t.Context(), clk.Now().Add(time.Minute))
	t.Cleanup(cancel)

	done := make(chan error, 1)

	go func() {
		_, _, err := a.Run(ctx, prompt)
		done <- err
	}()

	return clk, done
}

// returned waits for the result of Run; it fails the test after 10 s.
func returned(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")

		return nil
	}
}

// notReturned fails the test when Run returns within 200 ms.
func notReturned(t *testing.T, done <-chan error, why string) {
	t.Helper()

	select {
	case err := <-done:
		t.Fatalf("Run returned %v, want it running: %s", err, why)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRunTermsTheAgentTermLeadBeforeTheDeadline(t *testing.T) {
	s := newStub(t, `trap 'echo term > "$rec/term"; result cut; exit 0' TERM
init
: > "$rec/ready"
while :; do sleep 1; done`)
	clk, done := virtualRun(t, s, agentOptions{termLead: 30 * time.Second}, "go")

	awaitFile(t, filepath.Join(s.rec, "ready"))
	clk.Advance(29 * time.Second)
	notReturned(t, done, "TERM is 30 s before the deadline")

	clk.Advance(time.Second)

	if err := returned(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want an error wrapping context.DeadlineExceeded", err)
	}

	if got := s.file(t, "term"); got != "term\n" {
		t.Fatalf("term record = %q, want one TERM", got)
	}
}

func TestRunCutAfterInitResumesTheNextRun(t *testing.T) {
	s := newStub(t, `trap 'exit 143' TERM
init
: > "$rec/ready"
sleep 30 &
wait`)
	stateDir := filepath.Join(t.TempDir(), ".krci-agent")
	clk, done := virtualRun(t, s, agentOptions{stateDir: stateDir, termLead: 30 * time.Second}, "one")

	awaitFile(t, filepath.Join(s.rec, "ready"))
	clk.Advance(30 * time.Second)

	if err := returned(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want an error wrapping context.DeadlineExceeded", err)
	}

	s.body(t, `init; result done`)
	run(t, newAgent(t, s, agentOptions{stateDir: stateDir}), "two")

	if flag, _ := s.conversation(t, 2); flag != "--resume" {
		t.Fatalf("run after a cut = %s, want --resume", flag)
	}
}

func TestRunKillsTheProcessGroupAtTheDeadline(t *testing.T) {
	s := newStub(t, ignoreTerm)
	clk, done := virtualRun(t, s, agentOptions{termLead: 30 * time.Second}, "go")

	awaitFile(t, filepath.Join(s.rec, "ready"))
	clk.Advance(30 * time.Second)
	awaitFile(t, filepath.Join(s.rec, "term"))
	notReturned(t, done, "KILL is at the deadline")

	clk.Advance(30 * time.Second)

	if err := returned(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want an error wrapping context.DeadlineExceeded", err)
	}

	if got := s.file(t, "term"); got != "term\n" {
		t.Fatalf("term record = %q, want one TERM", got)
	}

	awaitGone(t, s.pid(t, "child"))
}

func TestRunCancelledTermsThenKillsAfterTermLead(t *testing.T) {
	s := newStub(t, ignoreTerm)
	lead := 500 * time.Millisecond
	a := newAgent(t, s, agentOptions{termLead: lead})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cancelled := make(chan time.Time, 1)

	go func() {
		awaitFile(t, filepath.Join(s.rec, "child"))

		cancelled <- time.Now()

		cancel()
	}()

	_, _, err := a.Run(ctx, "go")
	ended := time.Now()

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want an error wrapping context.Canceled", err)
	}

	if got := ended.Sub(<-cancelled); got < lead {
		t.Fatalf("Run returned %v after cancel, want KILL %v after TERM", got, lead)
	}

	if got := s.file(t, "term"); got != "term\n" {
		t.Fatalf("term record = %q, want one TERM", got)
	}

	awaitGone(t, s.pid(t, "child"))
}

func TestRunDoesNotStartPastTheTermPoint(t *testing.T) {
	t.Parallel()

	s := newStub(t, `init; result done`)
	a := newAgent(t, s, agentOptions{termLead: time.Minute})

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	if _, _, err := a.Run(ctx, "go"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want an error wrapping context.DeadlineExceeded", err)
	}

	if _, err := os.Stat(filepath.Join(s.rec, "count")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agent started: %v", err)
	}
}

func TestRunKillsProcessesLeftInTheGroup(t *testing.T) {
	t.Parallel()

	s := newStub(t, `sleep 30 &
echo $! > "$rec/child"
result done`)

	if text, _ := run(t, newAgent(t, s, agentOptions{}), "go"); text != "done" {
		t.Fatalf("text = %q, want done", text)
	}

	awaitGone(t, s.pid(t, "child"))
}

func TestRunReturnsWhileAProcessOutsideTheGroupHoldsStdout(t *testing.T) {
	t.Parallel()

	s := newStub(t, `set -m
sleep 30 &
echo $! > "$rec/escaped"
result done`)

	t.Cleanup(func() { _ = syscall.Kill(s.pid(t, "escaped"), syscall.SIGKILL) })

	begin := time.Now()

	if text, _ := run(t, newAgent(t, s, agentOptions{}), "go"); text != "done" {
		t.Fatalf("text = %q, want done", text)
	}

	if got := time.Since(begin); got > 10*time.Second {
		t.Fatalf("Run took %v while a process outside the group held stdout", got)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	t.Parallel()

	valid := func() claude.Options {
		return claude.Options{
			Model: model, Steps: steps, Session: session, StateDir: t.TempDir(), Clock: system.Clock{},
		}
	}

	cases := map[string]func(o *claude.Options){
		"no model":          func(o *claude.Options) { o.Model = "" },
		"zero steps":        func(o *claude.Options) { o.Steps = 0 },
		"empty session":     func(o *claude.Options) { o.Session = "" },
		"session with /":    func(o *claude.Options) { o.Session = "a/b" },
		"no state dir":      func(o *claude.Options) { o.StateDir = "" },
		"no clock":          func(o *claude.Options) { o.Clock = nil },
		"negative termlead": func(o *claude.Options) { o.TermLead = -time.Second },
	}

	if _, err := claude.New(valid()); err != nil {
		t.Fatalf("New(valid) = %v", err)
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			o := valid()
			mutate(&o)

			if _, err := claude.New(o); err == nil {
				t.Fatal("New: want an error")
			}
		})
	}
}

func (s stub) body(t *testing.T, body string) {
	t.Helper()

	script := strings.ReplaceAll(prelude, "%REC%", s.rec) + body + "\n"
	if err := os.WriteFile(s.path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func (s stub) pid(t *testing.T, name string) int {
	t.Helper()

	pid, err := strconv.Atoi(strings.TrimSpace(s.file(t, name)))
	if err != nil {
		t.Fatal(err)
	}

	return pid
}

const settleBy = 5 * time.Second

func awaitGone(t *testing.T, pid int) {
	t.Helper()

	for end := time.Now().Add(settleBy); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) || zombie(pid) {
			return
		}
	}

	_ = syscall.Kill(pid, syscall.SIGKILL)

	t.Fatalf("process %d still runs", pid)
}

// zombie reports whether /proc shows pid as exited and not yet reaped; false without /proc.
func zombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}

	i := bytes.LastIndexByte(b, ')')

	return i >= 0 && len(b) > i+2 && b[i+2] == 'Z'
}

func awaitFile(t *testing.T, path string) {
	for end := time.Now().Add(settleBy); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}

	t.Errorf("%s not written", path)
}
