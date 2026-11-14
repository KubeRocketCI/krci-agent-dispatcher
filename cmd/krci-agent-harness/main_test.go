package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/selftest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
)

func agentImage(uid int) *systemtest.Fake {
	return &systemtest.Fake{
		User:   uid,
		Env:    map[string]string{"HOME": "/home/agent"},
		OnPath: []string{"claude", "git", "sh"},
		Dirs:   map[string][]string{"/home/agent": nil, selftest.WorkDir: nil},
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		uid        int
		want       int
		wantStdout string
		wantStderr string
	}{
		{name: "no command", want: 64, wantStderr: "usage:"},
		{name: "unknown command", args: []string{"turn"}, want: 64, wantStderr: "usage:"},
		{name: "extra argument", args: []string{"selftest", "-v"}, want: 64, wantStderr: "usage:"},
		{name: "help", args: []string{"help"}, want: exitOK, wantStdout: "usage:"},
		{name: "-h", args: []string{"-h"}, want: exitOK, wantStdout: "usage:"},
		{name: "--help", args: []string{"--help"}, want: exitOK, wantStdout: "usage:"},
		{
			name: "selftest passes", args: []string{"selftest"}, uid: selftest.AgentUID, want: exitOK,
			wantStdout: `"name":"mount_path_free","ok":true`,
		},
		{
			name: "selftest gap", args: []string{"selftest"}, uid: 0, want: exitGap,
			wantStdout: `"name":"agent_uid","ok":false`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			got := run(tt.args, agentImage(tt.uid), &stdout, &stderr)
			if got != tt.want {
				t.Fatalf("exit = %d, want %d; stderr %q", got, tt.want, stderr.String())
			}

			if !strings.Contains(stdout.String(), tt.wantStdout) || !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stdout %q, stderr %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunSelftestWriteError(t *testing.T) {
	var stderr bytes.Buffer

	if got := run([]string{"selftest"}, agentImage(selftest.AgentUID), failingWriter{}, &stderr); got != exitIO {
		t.Fatalf("exit = %d, want %d", got, exitIO)
	}

	if !strings.Contains(stderr.String(), "write selftest result") {
		t.Fatalf("stderr %q", stderr.String())
	}
}
