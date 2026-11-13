package harness

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
)

const (
	mountRO = "40 30 0:41 / /opt/krci-agent ro,nodev,relatime - overlay overlay ro\n"
	mountRW = "40 30 0:41 / /opt/krci-agent rw,nodev,relatime - overlay overlay rw\n"
	rootFS  = "30 1 0:40 / / rw,relatime - overlay overlay rw\n"
)

// fakeSystem is an agent image with the harness mounted and every check passing.
type fakeSystem struct {
	uid        int
	home       string
	exe        string
	missing    string
	writeErr   map[string]error
	mountDir   []fs.DirEntry
	mountDirEr error
	mountInfo  string
	mountInfoE error
}

func passing() *fakeSystem {
	return &fakeSystem{
		uid:       AgentUID,
		home:      "/home/agent",
		exe:       Command,
		writeErr:  map[string]error{MountPath: syscall.EACCES},
		mountInfo: rootFS + mountRO,
	}
}

func (f *fakeSystem) system() System {
	return System{
		UID:        func() int { return f.uid },
		Getenv:     func(key string) string { return map[string]string{"HOME": f.home}[key] },
		Executable: func() (string, error) { return f.exe, nil },
		LookPath: func(name string) (string, error) {
			if name == f.missing {
				return "", os.ErrNotExist
			}

			return "/usr/bin/" + name, nil
		},
		ReadDir:    func(string) ([]fs.DirEntry, error) { return f.mountDir, f.mountDirEr },
		WriteProbe: func(dir string) error { return f.writeErr[dir] },
		MountInfo: func() (io.ReadCloser, error) {
			if f.mountInfoE != nil {
				return nil, f.mountInfoE
			}

			return io.NopCloser(strings.NewReader(f.mountInfo)), nil
		},
	}
}

func failed(checks []Check) []string {
	var names []string

	for _, c := range checks {
		if !c.OK {
			names = append(names, c.Name)
		}
	}

	return names
}

func dirEntries(t *testing.T, names ...string) []fs.DirEntry {
	t.Helper()

	m := fstest.MapFS{}
	for _, n := range names {
		m[n] = &fstest.MapFile{}
	}

	entries, err := fs.ReadDir(m, ".")
	if err != nil {
		t.Fatal(err)
	}

	return entries
}

func TestContractChecks(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*fakeSystem)
		want   []string
	}{
		{name: "harness mounted", modify: func(*fakeSystem) {}},
		{name: "image without harness, mount path absent", modify: func(f *fakeSystem) {
			f.exe, f.mountDirEr = "/usr/local/bin/krci-agent-harness", fs.ErrNotExist
		}},
		{name: "image without harness, mount path empty", modify: func(f *fakeSystem) {
			f.exe, f.mountDir = "/usr/local/bin/krci-agent-harness", dirEntries(t)
		}},
		{name: "image fills the mount path", want: []string{"mount_path_free"}, modify: func(f *fakeSystem) {
			f.exe, f.mountDir = "/usr/local/bin/krci-agent-harness", dirEntries(t, "tool")
		}},
		{name: "mount path unreadable", want: []string{"mount_path_free"}, modify: func(f *fakeSystem) {
			f.exe, f.mountDirEr = "/usr/local/bin/krci-agent-harness", fs.ErrPermission
		}},
		{name: "git missing", want: []string{"path_git"}, modify: func(f *fakeSystem) { f.missing = "git" }},
		{name: "root user", want: []string{"uid_1000"}, modify: func(f *fakeSystem) { f.uid = 0 }},
		{name: "HOME unset", want: []string{"home_writable"}, modify: func(f *fakeSystem) { f.home = "" }},
		{name: "HOME read-only", want: []string{"home_writable"}, modify: func(f *fakeSystem) {
			f.writeErr["/home/agent"] = syscall.EROFS
		}},
		{name: "work read-only", want: []string{"work_writable"}, modify: func(f *fakeSystem) {
			f.writeErr[WorkDir] = syscall.EROFS
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := passing()
			tt.modify(f)

			if got := failed(ContractChecks(f.system())); !slices.Equal(got, tt.want) {
				t.Fatalf("failed checks = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMountChecks(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*fakeSystem)
		want   []string
	}{
		{name: "mounted read-only, running from it", modify: func(*fakeSystem) {}},
		{name: "mounted rw", want: []string{"mount_read_only"}, modify: func(f *fakeSystem) {
			f.mountInfo = rootFS + mountRW
		}},
		{name: "last mount at the path wins", want: []string{"mount_read_only"}, modify: func(f *fakeSystem) {
			f.mountInfo = rootFS + mountRO + mountRW
		}},
		{name: "no mount at the path", want: []string{"mount_read_only"}, modify: func(f *fakeSystem) {
			f.mountInfo = rootFS
		}},
		{name: "mountinfo unreadable", want: []string{"mount_read_only"}, modify: func(f *fakeSystem) {
			f.mountInfoE = errors.New("no /proc")
		}},
		{name: "ro but write succeeded", want: []string{"mount_read_only"}, modify: func(f *fakeSystem) {
			f.writeErr[MountPath] = nil
		}},
		{name: "binary outside the mount", want: []string{"runs_from_mount"}, modify: func(f *fakeSystem) {
			f.exe = "/usr/local/bin/krci-agent-harness"
		}},
		{name: "mount path prefix only", want: []string{"runs_from_mount"}, modify: func(f *fakeSystem) {
			f.exe = "/opt/krci-agent-old/krci-agent-harness"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := passing()
			tt.modify(f)

			if got := failed(MountChecks(f.system())); !slices.Equal(got, tt.want) {
				t.Fatalf("failed checks = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWriteProbeFile(t *testing.T) {
	dir := t.TempDir()

	if err := writeProbeFile(dir); err != nil {
		t.Fatalf("writable dir: %v", err)
	}

	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("probe file left behind: %v", entries)
	}

	if err := writeProbeFile(dir + "/absent"); err == nil {
		t.Fatal("missing dir: want error")
	}
}

func TestAllOK(t *testing.T) {
	if !AllOK([]Check{{Name: "a", OK: true}}) {
		t.Fatal("want true")
	}

	if AllOK([]Check{{Name: "a", OK: true}, {Name: "b"}}) {
		t.Fatal("want false")
	}
}

func TestOS(t *testing.T) {
	sys := OS()
	if sys.UID() != os.Getuid() {
		t.Fatal("UID is not the process uid")
	}

	if rc, err := sys.MountInfo(); err == nil {
		_ = rc.Close()
	}
}
