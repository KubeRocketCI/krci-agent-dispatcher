package harness

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/models"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/turn"
)

const (
	maxSeconds = math.MaxInt64 / int64(time.Second)
	// minWatchdog is the shortest watchdog limit that leaves a turn time to start.
	minWatchdog = time.Minute
)

// Config is the handshake result.
type Config struct {
	GitLabURL   models.GitLabURL
	Key         models.IssueKey
	ProjectPath string
	Bot         string
	Session     string
	Model       string
	Home        string
	TurnLimit   int
	AgentSteps  int
	TurnTimeout time.Duration
	Poll        time.Duration
	Watchdog    time.Duration
}

// HandshakeError names the env item the handshake refused and why; it never holds the item's value.
type HandshakeError struct {
	Item   string
	Reason string
}

func (e *HandshakeError) Error() string { return e.Item + ": " + e.Reason }

// reader reads env items; the first refused item stays and later reads return zero values.
type reader struct {
	getenv Getenv
	err    *HandshakeError
}

func (r *reader) refuse(item, reason string) {
	if r.err == nil {
		r.err = &HandshakeError{Item: item, Reason: reason}
	}
}

func (r *reader) nonEmpty(item string) string {
	v := r.getenv(item)
	if v == "" {
		r.refuse(item, "must not be empty")
	}

	return v
}

func (r *reader) session(item string) string {
	v := r.getenv(item)
	if !models.ValidSessionID(v) {
		r.refuse(item, "must be [0-9A-Za-z-]+")
	}

	return v
}

// positive reads a decimal integer above zero.
func (r *reader) positive(item string) int64 {
	v, err := strconv.ParseInt(r.getenv(item), 10, 64)
	if err != nil || v <= 0 {
		r.refuse(item, "must be an integer above zero")

		return 0
	}

	return v
}

// seconds reads a positive number of seconds that fits in a time.Duration.
func (r *reader) seconds(item string) time.Duration {
	v := r.positive(item)
	if v > maxSeconds {
		r.refuse(item, "is too large")

		return 0
	}

	return time.Duration(v) * time.Second
}

func (r *reader) contractVersion(item string) {
	if r.getenv(item) != strconv.Itoa(turn.ContractVersion) {
		r.refuse(item, "is not the contract version of this harness")
	}
}

func (r *reader) gitlabURL(item string) models.GitLabURL {
	u, err := models.ParseGitLabURL(r.getenv(item))
	if err != nil {
		r.refuse(item, fmt.Sprint(err))
	}

	return u
}

// Handshake reads and checks the env contract.
func Handshake(getenv Getenv) (Config, error) {
	r := &reader{getenv: getenv}

	c := Config{
		GitLabURL:   r.gitlabURL("GITLAB_URL"),
		ProjectPath: r.nonEmpty("PROJECT_PATH"),
		Bot:         r.nonEmpty("BOT_USERNAME"),
		Session:     r.session("SESSION_ID"),
		Model:       r.nonEmpty("ANTHROPIC_MODEL"),
		Home:        r.nonEmpty("HOME"),
	}

	c.Key = models.IssueKey{ProjectID: r.positive("PROJECT_ID"), IID: r.positive("ISSUE_IID")}
	c.TurnLimit = int(r.positive("TURN_LIMIT"))
	c.AgentSteps = int(r.positive("AGENT_STEPS"))
	c.TurnTimeout, c.Poll = r.seconds("TURN_TIMEOUT_SECONDS"), r.seconds("POLL_SECONDS")

	r.contractVersion("AGENT_CONTRACT_VERSION")

	// WatchdogLimit takes a poll below the turn timeout.
	if c.TurnTimeout > c.Poll {
		c.Watchdog = turn.WatchdogLimit(c.TurnTimeout, c.Poll)
	}

	if c.Watchdog < minWatchdog {
		r.refuse("watchdog limit", "TURN_TIMEOUT_SECONDS minus POLL_SECONDS minus one minute is below one minute")
	}

	if r.err != nil {
		return Config{}, r.err
	}

	return c, nil
}
