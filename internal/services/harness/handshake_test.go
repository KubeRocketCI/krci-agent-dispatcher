package harness_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/harness"
)

func validEnv() map[string]string {
	return map[string]string{
		"GITLAB_URL":             "https://gitlab.example.com",
		"PROJECT_ID":             "12",
		"ISSUE_IID":              "7",
		"PROJECT_PATH":           "group/project",
		"BOT_USERNAME":           "krci-agent",
		"SESSION_ID":             "session-1",
		"AGENT_CONTRACT_VERSION": "1",
		"TURN_TIMEOUT_SECONDS":   "900",
		"POLL_SECONDS":           "10",
		"TURN_LIMIT":             "5",
		"AGENT_STEPS":            "40",
		"ANTHROPIC_MODEL":        "model-x",
	}
}

func getenv(m map[string]string) harness.Getenv {
	return func(k string) string { return m[k] }
}

func TestNewAcceptsTheEnvContract(t *testing.T) {
	h, err := harness.New(getenv(validEnv()), harness.Ports{})
	require.NoError(t, err)
	assert.NotNil(t, h)
}

// refused runs the handshake over validEnv with item set to value and returns the handshake error.
func refused(t *testing.T, item, value string) *harness.HandshakeError {
	t.Helper()

	env := validEnv()
	env[item] = value

	_, err := harness.New(getenv(env), harness.Ports{})
	require.Error(t, err, "%s", item)

	var he *harness.HandshakeError

	require.ErrorAs(t, err, &he)

	return he
}

func TestHandshakeNamesTheItemNotTheValue(t *testing.T) {
	const leak = "leak-7f3a"

	tests := []struct {
		item   string
		values []string
	}{
		{"GITLAB_URL", []string{
			"", "http://gitlab.example.com/" + leak, "https://user:" + leak + "@gitlab.example.com", leak,
		}},
		{"PROJECT_PATH", []string{""}},
		{"BOT_USERNAME", []string{""}},
		{"ANTHROPIC_MODEL", []string{""}},
		{"SESSION_ID", []string{"", "s_" + leak, "s 1", "s=1"}},
	}
	for _, tt := range tests {
		for _, v := range tt.values {
			he := refused(t, tt.item, v)
			assert.Equal(t, tt.item, he.Item, "value %q", v)
			assert.NotContains(t, he.Error(), leak)
		}
	}
}

func TestHandshakeRefusesNonPositiveIntegers(t *testing.T) {
	for _, item := range []string{"PROJECT_ID", "ISSUE_IID", "TURN_LIMIT", "AGENT_STEPS"} {
		for _, v := range []string{"", "0", "-3", "1.5", "leak-12x", "99999999999999999999"} {
			he := refused(t, item, v)
			assert.Equal(t, item, he.Item, "value %q", v)

			if v != "" {
				assert.NotContains(t, he.Error(), v)
			}
		}
	}
}

func TestHandshakeRefusesOtherContractVersions(t *testing.T) {
	for _, v := range []string{"", "0", "2", "leak-1"} {
		assert.Equal(t, "AGENT_CONTRACT_VERSION", refused(t, "AGENT_CONTRACT_VERSION", v).Item, "value %q", v)
	}
}

func TestHandshakeRefusesDurationsThatAreNotPositiveOrOverflow(t *testing.T) {
	// 9223372036 s is the largest whole second count a time.Duration holds.
	for _, item := range []string{"TURN_TIMEOUT_SECONDS", "POLL_SECONDS"} {
		for _, v := range []string{"", "0", "-60", "leak-9", "9223372037", "9223372036854775807"} {
			assert.Equal(t, item, refused(t, item, v).Item, "value %q", v)
		}
	}
}

func TestHandshakeAcceptsTheLargestDuration(t *testing.T) {
	env := validEnv()
	env["TURN_TIMEOUT_SECONDS"] = "9223372036"

	_, err := harness.New(getenv(env), harness.Ports{})
	require.NoError(t, err)
}

func TestHandshakeRequiresAWatchdogLimitOfOneMinute(t *testing.T) {
	tests := []struct {
		timeout, poll string
		ok            bool
	}{
		{"130", "10", true},
		{"129", "10", false},
		{"120", "1", false},
		{"9223372036", "9223372036", false},
		{"1", "9223372036", false},
	}
	for _, tt := range tests {
		env := validEnv()
		env["TURN_TIMEOUT_SECONDS"], env["POLL_SECONDS"] = tt.timeout, tt.poll

		_, err := harness.New(getenv(env), harness.Ports{})
		if tt.ok {
			require.NoError(t, err, "%s/%s", tt.timeout, tt.poll)
			continue
		}

		var he *harness.HandshakeError

		require.ErrorAs(t, err, &he, "%s/%s", tt.timeout, tt.poll)
		assert.Equal(t, "watchdog limit", he.Item)
		assert.NotContains(t, he.Error(), tt.timeout)
	}
}
