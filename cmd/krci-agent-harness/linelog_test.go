package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lines returns the "line" field of each JSON record in buf.
func lines(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()

	var out []string

	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}

		var rec map[string]any

		require.NoError(t, json.Unmarshal([]byte(l), &rec))
		assert.Equal(t, "git", rec["source"])
		out = append(out, rec["line"].(string))
	}

	return out
}

func TestLineLogLogsEachLineAcrossWrites(t *testing.T) {
	var buf bytes.Buffer

	l := &lineLog{log: slog.New(slog.NewJSONHandler(&buf, nil)), source: "git"}

	_, _ = l.Write([]byte("Cloning into 'app'...\nremote: Count"))
	_, _ = l.Write([]byte("ing objects: 3\n"))

	assert.Equal(t, []string{"Cloning into 'app'...", "remote: Counting objects: 3"}, lines(t, &buf))
}

func TestLineLogFlushLogsTheUnterminatedLine(t *testing.T) {
	var buf bytes.Buffer

	l := &lineLog{log: slog.New(slog.NewJSONHandler(&buf, nil)), source: "git"}

	_, _ = l.Write([]byte("fatal: no newline"))

	assert.Empty(t, lines(t, &buf), "a partial line waits for its newline")

	l.Flush()
	l.Flush()
	assert.Equal(t, []string{"fatal: no newline"}, lines(t, &buf), "logged once")
}

// Output without a newline is logged in records of at most maxLine bytes.
func TestLineLogSplitsALineAtMaxLine(t *testing.T) {
	var buf bytes.Buffer

	l := &lineLog{log: slog.New(slog.NewJSONHandler(&buf, nil)), source: "git"}

	_, _ = l.Write(bytes.Repeat([]byte("a"), 64<<10+10))

	got := lines(t, &buf)
	require.Len(t, got, 1, "the full first 64 KiB is logged at once")
	assert.Len(t, got[0], 64<<10)

	l.Flush()
	assert.Equal(t, strings.Repeat("a", 10), lines(t, &buf)[1])
}
