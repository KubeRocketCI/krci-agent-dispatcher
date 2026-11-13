package main

import (
	"bytes"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/harness"
)

func agentImage(uid int) harness.System {
	return harness.System{
		UID:        func() int { return uid },
		Getenv:     func(string) string { return "/home/agent" },
		Executable: func() (string, error) { return "/usr/local/bin/krci-agent-harness", nil },
		LookPath:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
		ReadDir:    func(string) ([]fs.DirEntry, error) { return nil, fs.ErrNotExist },
		WriteProbe: func(string) error { return nil },
		MountInfo:  func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil },
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		uid        int
		want       int
		wantStdout string
		wantStderr string
	}{
		{name: "no command", want: exitUsage, wantStderr: "usage:"},
		{name: "unknown command", args: []string{"turn"}, want: exitUsage, wantStderr: "usage:"},
		{name: "extra argument", args: []string{"selftest", "-v"}, want: exitUsage, wantStderr: "usage:"},
		{
			name:       "selftest passes",
			args:       []string{"selftest"},
			uid:        harness.AgentUID,
			want:       exitOK,
			wantStdout: `"name":"mount_path_free","ok":true`,
		},
		{name: "selftest gap", args: []string{"selftest"}, uid: 0, want: exitGap, wantStdout: `"name":"uid_1000","ok":false`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			got := run(t.Context(), tt.args, agentImage(tt.uid), &stdout, &stderr)
			if got != tt.want {
				t.Fatalf("exit = %d, want %d; stderr %q", got, tt.want, stderr.String())
			}

			if !strings.Contains(stdout.String(), tt.wantStdout) || !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stdout %q, stderr %q", stdout.String(), stderr.String())
			}
		})
	}
}
