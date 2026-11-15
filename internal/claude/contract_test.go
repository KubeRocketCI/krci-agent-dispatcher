package claude_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/claude"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness/harnesstest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
)

var (
	_ harness.Agent = (*harnesstest.Agent)(nil)
	_ harness.Agent = (*claude.Agent)(nil)
)

// agentScenario is the run a contract case asks of a subject: its result text and exit code, or a run that does not
// end until the watchdog cuts it. noInit is a run that ends before the session starts (claude: no init event).
type agentScenario struct {
	text   string
	exit   int
	block  bool
	noInit bool
}

// buildAgent returns a subject on clk for a scenario.
type buildAgent = func(t *testing.T, clk *systemtest.Clock, sc agentScenario) agentSubject

// agentSubject is an Agent under contract on clk; running returns once a blocking run is under way.
type agentSubject struct {
	agent   harness.Agent
	running func()
}

func agentSubjects() map[string]func(t *testing.T, clk *systemtest.Clock, sc agentScenario) agentSubject {
	return map[string]func(t *testing.T, clk *systemtest.Clock, sc agentScenario) agentSubject{
		"fake": func(_ *testing.T, _ *systemtest.Clock, sc agentScenario) agentSubject {
			a := &harnesstest.Agent{
				Result: func(ctx context.Context, _ int) (string, int, error) {
					if sc.block {
						return "", 0, harnesstest.Await(ctx)
					}

					return sc.text, sc.exit, nil
				},
				NoInit: func(int) bool { return sc.noInit },
			}

			return agentSubject{agent: a, running: func() {}}
		},
		"claude": func(t *testing.T, clk *systemtest.Clock, sc agentScenario) agentSubject {
			body := fmt.Sprintf("init; result %s; exit %d", sc.text, sc.exit)
			switch {
			case sc.noInit:
				body = fmt.Sprintf("result %s; exit %d", sc.text, sc.exit)
			case sc.block:
				body = `trap 'exit 143' TERM
init
: > "$rec/ready"
sleep 30 &
wait`
			}

			s := newStub(t, body)

			return agentSubject{
				agent:   newAgent(t, s, agentOptions{clock: clk, termLead: 30 * time.Second}),
				running: func() { awaitFile(t, filepath.Join(s.rec, "ready")) },
			}
		},
	}
}

func TestAgentContract(t *testing.T) {
	cases := map[string]func(t *testing.T, build buildAgent){
		"ReturnsTheResultTextAndExitCode": ReturnsTheResultTextAndExitCode,
		"ReturnsANonZeroExitWithItsText":  ReturnsANonZeroExitWithItsText,
		"WatchdogCutIsDeadlineExceeded":   WatchdogCutIsDeadlineExceeded,
		"FreshUntilARunStartedIt":         FreshUntilARunStartedIt,
		"FreshAfterARunThatEndedBeforeIt": FreshAfterARunThatEndedBeforeIt,
	}

	for name, build := range agentSubjects() {
		for c, run := range cases {
			t.Run(name+"/"+c, func(t *testing.T) { run(t, build) })
		}
	}
}

func newClock() *systemtest.Clock {
	return systemtest.NewClock(time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC))
}

func ReturnsTheResultTextAndExitCode(
	t *testing.T,
	build buildAgent,
) {
	clk := newClock()
	s := build(t, clk, agentScenario{text: "done"})

	text, rc, err := s.agent.Run(t.Context(), "go")
	if err != nil || text != "done" || rc != 0 {
		t.Fatalf("Run = %q, %d, %v; want done, 0, nil", text, rc, err)
	}
}

func ReturnsANonZeroExitWithItsText(
	t *testing.T,
	build buildAgent,
) {
	clk := newClock()
	s := build(t, clk, agentScenario{text: "partial", exit: 3})

	text, rc, err := s.agent.Run(t.Context(), "go")
	if err != nil || text != "partial" || rc != 3 {
		t.Fatalf("Run = %q, %d, %v; want partial, 3, nil", text, rc, err)
	}
}

// FreshAfterARunThatEndedBeforeIt: a run that ends before the session starts leaves the agent fresh, also with an
// exit code and a text.
func FreshAfterARunThatEndedBeforeIt(
	t *testing.T,
	build buildAgent,
) {
	clk := newClock()
	s := build(t, clk, agentScenario{text: "partial", exit: 1, noInit: true})

	if _, _, err := s.agent.Run(t.Context(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !s.agent.Fresh() {
		t.Fatal("Fresh after a run that ended before the session started = false, want true")
	}
}

// FreshUntilARunStartedIt: the scenario's run starts the conversation (claude: the init event; fake: a run that
// NoInit does not report).
func FreshUntilARunStartedIt(
	t *testing.T,
	build buildAgent,
) {
	clk := newClock()
	s := build(t, clk, agentScenario{text: "done"})

	if !s.agent.Fresh() {
		t.Fatal("Fresh before any run = false, want true")
	}

	if _, _, err := s.agent.Run(t.Context(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if s.agent.Fresh() {
		t.Fatal("Fresh after a run that started the conversation = true, want false")
	}
}

func WatchdogCutIsDeadlineExceeded(
	t *testing.T,
	build buildAgent,
) {
	clk := newClock()
	s := build(t, clk, agentScenario{block: true})

	ctx, cancel := clk.WithDeadline(t.Context(), clk.Now().Add(time.Minute))
	defer cancel()

	done := make(chan error, 1)

	go func() {
		_, _, err := s.agent.Run(ctx, "go")
		done <- err
	}()

	s.running()
	clk.Advance(time.Minute)

	if err := returned(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want an error wrapping context.DeadlineExceeded", err)
	}
}
