package selftest_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/selftest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
)

const profilePath = "/etc/krci-agent/profile.yaml"

// agentImage is an agent image that meets the contract, without the harness mounted.
func agentImage() *systemtest.Fake {
	return &systemtest.Fake{
		User:   selftest.AgentUID,
		Env:    map[string]string{"HOME": "/home/agent"},
		OnPath: []string{"claude", "git", "sh"},
		Dirs:   map[string][]string{"/home/agent": nil, selftest.WorkDir: nil},
	}
}

func failed(checks []selftest.Check) []string {
	var names []string

	for _, c := range checks {
		if !c.OK {
			names = append(names, c.Name)
		}
	}

	return names
}

func names(checks []selftest.Check) []string {
	out := make([]string, 0, len(checks))

	for _, c := range checks {
		out = append(out, c.Name)
	}

	return out
}

func TestAgentImageChecksNames(t *testing.T) {
	want := []string{
		"path_claude", "path_git", "path_sh", "agent_uid", "home_writable", "work_writable", "mount_path_free",
		"profile_valid",
	}

	checks := selftest.AgentImageChecks(agentImage())
	if got := names(checks); !slices.Equal(got, want) {
		t.Fatalf("checks = %v, want %v", got, want)
	}

	for _, c := range checks {
		if !c.OK || c.Detail != "" {
			t.Fatalf("passing check %+v: want ok with no detail", c)
		}
	}
}

func TestMountPathFreeNamesEntries(t *testing.T) {
	f := agentImage()
	f.Dirs[selftest.MountPath] = []string{"bin", "tool"}

	if got := check(t, selftest.AgentImageChecks(f), "mount_path_free").Detail; got != "/opt/krci-agent holds bin, tool" {
		t.Fatalf("detail = %q", got)
	}
}

func check(t *testing.T, checks []selftest.Check, name string) selftest.Check {
	t.Helper()

	i := slices.IndexFunc(checks, func(c selftest.Check) bool { return c.Name == name })
	if i < 0 {
		t.Fatalf("no check %s in %v", name, names(checks))
	}

	return checks[i]
}

func TestProfileValidDetailHoldsNoContent(t *testing.T) {
	const secret = "s3cr3t-profile-content"

	tests := map[string]func(*systemtest.Fake){
		"unreadable": func(f *systemtest.Fake) {
			f.Files = map[string]string{profilePath: "setup: " + secret + "\n"}
			f.Unreadable = []string{profilePath}
		},
		"not YAML": func(f *systemtest.Fake) { f.Files = map[string]string{profilePath: secret + ": [\n"} },
	}
	for name, modify := range tests {
		t.Run(name, func(t *testing.T) {
			f := agentImage()
			modify(f)

			c := check(t, selftest.AgentImageChecks(f), "profile_valid")
			if c.OK || c.Detail == "" || strings.Contains(c.Detail, secret) {
				t.Fatalf("check %+v: want failed, a detail without the file content", c)
			}
		})
	}
}

func TestAgentImageChecks(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*systemtest.Fake)
		want   []string
	}{
		{name: "mount path empty", modify: func(f *systemtest.Fake) { f.Dirs[selftest.MountPath] = nil }},
		{name: "image fills the mount path", want: []string{"mount_path_free"}, modify: func(f *systemtest.Fake) {
			f.Dirs[selftest.MountPath] = []string{"tool"}
		}},
		{name: "mount path unreadable", want: []string{"mount_path_free"}, modify: func(f *systemtest.Fake) {
			f.Dirs[selftest.MountPath] = nil
			f.Unreadable = []string{selftest.MountPath}
		}},
		{name: "claude missing", want: []string{"path_claude"}, modify: func(f *systemtest.Fake) {
			f.OnPath = []string{"git", "sh"}
		}},
		{name: "git missing", want: []string{"path_git"}, modify: func(f *systemtest.Fake) {
			f.OnPath = []string{"claude", "sh"}
		}},
		{name: "sh missing", want: []string{"path_sh"}, modify: func(f *systemtest.Fake) {
			f.OnPath = []string{"claude", "git"}
		}},
		{name: "root user", want: []string{"agent_uid"}, modify: func(f *systemtest.Fake) { f.User = 0 }},
		{name: "HOME unset", modify: func(f *systemtest.Fake) { f.Env = nil }},
		{name: "HOME is another directory", modify: func(f *systemtest.Fake) {
			f.Env = map[string]string{"HOME": "/home/node"}
			f.Dirs["/home/node"] = nil
		}},
		{name: "/home/agent absent", want: []string{"home_writable"}, modify: func(f *systemtest.Fake) {
			delete(f.Dirs, "/home/agent")
			f.Env = map[string]string{"HOME": "/home/node"}
			f.Dirs["/home/node"] = nil
		}},
		{name: "/home/agent read-only", want: []string{"home_writable"}, modify: func(f *systemtest.Fake) {
			f.ReadOnly = []string{"/home/agent"}
		}},
		{name: "work dir absent", want: []string{"work_writable"}, modify: func(f *systemtest.Fake) {
			delete(f.Dirs, selftest.WorkDir)
		}},
		{name: "profile valid", modify: func(f *systemtest.Fake) {
			f.Files = map[string]string{profilePath: "setup: make deps\nverify: make test\nprompt: Use Go.\n"}
		}},
		{name: "profile empty", modify: func(f *systemtest.Fake) { f.Files = map[string]string{profilePath: ""} }},
		{name: "profile unreadable", want: []string{"profile_valid"}, modify: func(f *systemtest.Fake) {
			f.Files = map[string]string{profilePath: "setup: make deps\n"}
			f.Unreadable = []string{profilePath}
		}},
		{name: "profile not YAML", want: []string{"profile_valid"}, modify: func(f *systemtest.Fake) {
			f.Files = map[string]string{profilePath: "setup: [\n"}
		}},
		{name: "profile unknown field", want: []string{"profile_valid"}, modify: func(f *systemtest.Fake) {
			f.Files = map[string]string{profilePath: "setup: make deps\nimage: golang\n"}
		}},
		{name: "profile non-string value", want: []string{"profile_valid"}, modify: func(f *systemtest.Fake) {
			f.Files = map[string]string{profilePath: "verify: [make, test]\n"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := agentImage()
			tt.modify(f)

			if got := failed(selftest.AgentImageChecks(f)); !slices.Equal(got, tt.want) {
				t.Fatalf("failed checks = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAllOK(t *testing.T) {
	if !selftest.AllOK([]selftest.Check{{Name: "a", OK: true}}) {
		t.Fatal("want true")
	}

	if selftest.AllOK([]selftest.Check{{Name: "a", OK: true}, {Name: "b"}}) {
		t.Fatal("want false")
	}
}
