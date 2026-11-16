package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The test binary re-runs itself as the dispatcher process when processEnv is set.
const (
	processEnv  = "KRCI_DISPATCHER_TEST_PROCESS"
	processArgs = "KRCI_DISPATCHER_TEST_ARGS"
)

func TestMain(m *testing.M) {
	if os.Getenv(processEnv) == "main" {
		os.Args = append([]string{"krci-agent-dispatcher"}, strings.Fields(os.Getenv(processArgs))...)

		main()
		os.Exit(0)
	}

	os.Exit(m.Run())
}

// runDispatcher runs krci-agent-dispatcher with args and env as a process; it returns the exit code and output.
func runDispatcher(t *testing.T, args string, env ...string) (int, string, string) {
	t.Helper()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(append(os.Environ(), processEnv+"=main", processArgs+"="+args, "GITLAB_URL="), env...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || !exitErr.Exited() {
		t.Fatalf("run: %v", err)
	}

	return exitErr.ExitCode(), stdout.String(), stderr.String()
}

func profileFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

const validProfiles = `
profiles:
  go:
    image: epamedp/krci-agent-go:0.1.1@sha256:c7b5f305bb3eb3d1628d97df031b0a83b44a42f1ec243cf0d2ac1fe715923e2a
    egress: [proxy.golang.org, gitlab.example.com]
    env:
      GOPROXY: https://proxy.golang.org
projects:
  krci/test-go-app: go
defaultProfile: go
`

func TestCheckProfilesValidFileExitsZero(t *testing.T) {
	code, stdout, stderr := runDispatcher(t, "check-profiles "+profileFile(t, validProfiles))

	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and no output", code, stdout, stderr)
	}
}

func TestCheckProfilesListsEveryProblem(t *testing.T) {
	content := strings.NewReplacer(
		"@sha256:c7b5f305bb3eb3d1628d97df031b0a83b44a42f1ec243cf0d2ac1fe715923e2a", "",
		"GOPROXY: https://proxy.golang.org", "HOME: /secret-looking-home",
		"defaultProfile: go", "defaultProfile: node",
	).Replace(validProfiles)

	code, stdout, _ := runDispatcher(t, "check-profiles "+profileFile(t, content))

	want := "go: image must be pinned by digest: <repo>[:<tag>]@sha256:<digest>\n" +
		"go: env name \"HOME\" is set by the actor template\n" +
		"node: defaultProfile is not defined\n"
	if code != 1 || stdout != want {
		t.Fatalf("exit %d, stdout %q; want 1 and %q", code, stdout, want)
	}

	if strings.Contains(stdout, "secret-looking-home") {
		t.Fatalf("stdout %q holds an env value", stdout)
	}
}

func TestCheckProfilesChecksTheGitLabHostOfGitLabURL(t *testing.T) {
	path := profileFile(t, validProfiles)

	code, stdout, _ := runDispatcher(t, "check-profiles "+path, "GITLAB_URL=https://gitlab.example.com")

	want := `go: egress host "gitlab.example.com" has a fixed egress rule` + "\n"
	if code != 1 || stdout != want {
		t.Fatalf("exit %d, stdout %q; want 1 and %q", code, stdout, want)
	}
}

func TestCheckProfilesFileProblems(t *testing.T) {
	cases := map[string]struct {
		path func(t *testing.T) string
		want string
	}{
		"missing": {
			func(t *testing.T) string { return filepath.Join(t.TempDir(), "profiles.yaml") },
			"profiles file does not exist\n",
		},
		"not YAML": {
			func(t *testing.T) string { return profileFile(t, "profiles: [go\n") },
			"profiles file is not valid: yaml: ",
		},
		"a directory": {
			func(t *testing.T) string { return t.TempDir() },
			"profiles file cannot be read: ",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			code, stdout, _ := runDispatcher(t, "check-profiles "+c.path(t))

			if code != 1 || !strings.HasPrefix(stdout, c.want) {
				t.Fatalf("exit %d, stdout %q; want 1 and %q", code, stdout, c.want)
			}
		})
	}
}

func TestCheckProfilesUsage(t *testing.T) {
	for _, args := range []string{"check-profiles", "check-profiles a b", "check"} {
		t.Run(args, func(t *testing.T) {
			code, _, stderr := runDispatcher(t, args)

			if code != 64 || !strings.Contains(stderr, "usage: krci-agent-dispatcher [check-profiles <file>]") {
				t.Fatalf("exit %d, stderr %q; want 64 and the usage", code, stderr)
			}
		})
	}

	code, _, stderr := runDispatcher(t, "check-profiles "+profileFile(t, validProfiles), "GITLAB_URL=http://x")
	if code != 64 || !strings.Contains(stderr, "GITLAB_URL must be") {
		t.Fatalf("exit %d, stderr %q; want 64 and the GITLAB_URL rule", code, stderr)
	}
}
