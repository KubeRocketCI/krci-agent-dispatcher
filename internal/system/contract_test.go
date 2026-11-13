package system_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/services/selftest"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system"
	"github.com/KubeRocketCI/krci-agent-dispatcher/internal/system/systemtest"
)

// world is the state a contract case needs; each subject builds it its own way.
type world struct {
	env        map[string]string
	onPath     []string
	dirs       map[string][]string
	readOnly   []string
	unreadable []string
}

// subject is an adapter over a built world; dir maps a world directory name to the adapter's path.
type subject struct {
	sys selftest.System
	uid int
	dir func(name string) string
}

func subjects() map[string]func(t *testing.T, w world) subject {
	return map[string]func(t *testing.T, w world) subject{
		"fake": func(_ *testing.T, w world) subject {
			dir := func(name string) string { return "/" + name }
			f := &systemtest.Fake{User: 1000, Env: w.env, OnPath: w.onPath, Dirs: map[string][]string{}}

			for name, entries := range w.dirs {
				f.Dirs[dir(name)] = entries
			}

			for _, name := range w.readOnly {
				f.ReadOnly = append(f.ReadOnly, dir(name))
			}

			for _, name := range w.unreadable {
				f.Unreadable = append(f.Unreadable, dir(name))
			}

			return subject{sys: f, uid: 1000, dir: dir}
		},
		"os": func(t *testing.T, w world) subject {
			root := t.TempDir()
			dir := func(name string) string { return filepath.Join(root, name) }

			bin := t.TempDir()
			for _, tool := range w.onPath {
				if err := os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			t.Setenv("PATH", bin)

			for k, v := range w.env {
				t.Setenv(k, v)
			}

			for name, entries := range w.dirs {
				if err := os.Mkdir(dir(name), 0o755); err != nil {
					t.Fatal(err)
				}

				for _, e := range entries {
					if err := os.WriteFile(filepath.Join(dir(name), e), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}

			chmod(t, dir, w.readOnly, 0o555)
			chmod(t, dir, w.unreadable, 0o000)

			return subject{sys: system.OS{}, uid: os.Getuid(), dir: dir}
		},
	}
}

// chmod restricts dirs and restores them at cleanup; root ignores modes, so the case is skipped.
func chmod(t *testing.T, dir func(string) string, names []string, mode os.FileMode) {
	t.Helper()

	for _, name := range names {
		if os.Getuid() == 0 {
			t.Skip("root ignores directory modes")
		}

		if err := os.Chmod(dir(name), mode); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _ = os.Chmod(dir(name), 0o755) })
	}
}

func TestContract(t *testing.T) {
	cases := map[string]func(t *testing.T, build func(world) subject){
		"UIDIsTheUser":              uidIsTheUser,
		"GetenvReadsSetAndUnset":    getenvReadsSetAndUnset,
		"LookPathFindsTools":        lookPathFindsTools,
		"LookPathMissingTool":       lookPathMissingTool,
		"ReadDirListsEntries":       readDirListsEntries,
		"ReadDirAbsentIsNotExist":   readDirAbsentIsNotExist,
		"ReadDirUnreadable":         readDirUnreadable,
		"WriteProbeErrorNamesDir":   writeProbeErrorNamesDir,
		"WriteProbeLeavesNoFile":    writeProbeLeavesNoFile,
		"WriteProbeRefusesReadOnly": writeProbeRefusesReadOnly,
		"WriteProbeAbsentDir":       writeProbeAbsentDir,
	}
	for name, newSubject := range subjects() {
		for c, run := range cases {
			t.Run(name+"/"+c, func(t *testing.T) {
				run(t, func(w world) subject { return newSubject(t, w) })
			})
		}
	}
}

func uidIsTheUser(t *testing.T, build func(world) subject) {
	s := build(world{})
	if got := s.sys.UID(); got != s.uid {
		t.Fatalf("UID = %d, want %d", got, s.uid)
	}
}

func getenvReadsSetAndUnset(t *testing.T, build func(world) subject) {
	s := build(world{env: map[string]string{"KRCI_CONTRACT_SET": "v"}})
	if got := s.sys.Getenv("KRCI_CONTRACT_SET"); got != "v" {
		t.Fatalf("set = %q", got)
	}

	if got := s.sys.Getenv("KRCI_CONTRACT_UNSET"); got != "" {
		t.Fatalf("unset = %q", got)
	}
}

func lookPathFindsTools(t *testing.T, build func(world) subject) {
	s := build(world{onPath: []string{"git"}})
	if p, err := s.sys.LookPath("git"); err != nil || filepath.Base(p) != "git" {
		t.Fatalf("LookPath(git) = %q, %v", p, err)
	}
}

func lookPathMissingTool(t *testing.T, build func(world) subject) {
	s := build(world{onPath: []string{"git"}})
	if _, err := s.sys.LookPath("claude"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("LookPath(claude): %v, want exec.ErrNotFound", err)
	}
}

func readDirListsEntries(t *testing.T, build func(world) subject) {
	s := build(world{dirs: map[string][]string{"full": {"a", "b"}, "empty": nil}})

	full, err := s.sys.ReadDir(s.dir("full"))
	if err != nil || len(full) != 2 {
		t.Fatalf("full: %d entries, %v", len(full), err)
	}

	empty, err := s.sys.ReadDir(s.dir("empty"))
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty: %d entries, %v", len(empty), err)
	}
}

func readDirAbsentIsNotExist(t *testing.T, build func(world) subject) {
	s := build(world{})
	if _, err := s.sys.ReadDir(s.dir("absent")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("absent: %v, want fs.ErrNotExist", err)
	}
}

func writeProbeLeavesNoFile(t *testing.T, build func(world) subject) {
	s := build(world{dirs: map[string][]string{"work": nil}})
	if err := s.sys.WriteProbe(s.dir("work")); err != nil {
		t.Fatalf("WriteProbe: %v", err)
	}

	if entries, err := s.sys.ReadDir(s.dir("work")); err != nil || len(entries) != 0 {
		t.Fatalf("after probe: %d entries, %v", len(entries), err)
	}
}

func writeProbeRefusesReadOnly(t *testing.T, build func(world) subject) {
	s := build(world{dirs: map[string][]string{"ro": nil}, readOnly: []string{"ro"}})
	if err := s.sys.WriteProbe(s.dir("ro")); err == nil {
		t.Fatal("read-only dir: want error")
	}
}

func writeProbeAbsentDir(t *testing.T, build func(world) subject) {
	s := build(world{})
	if err := s.sys.WriteProbe(s.dir("absent")); err == nil {
		t.Fatal("absent dir: want error")
	}
}

func readDirUnreadable(t *testing.T, build func(world) subject) {
	s := build(world{dirs: map[string][]string{"locked": {"a"}}, unreadable: []string{"locked"}})
	if _, err := s.sys.ReadDir(s.dir("locked")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("locked: %v, want fs.ErrPermission", err)
	}
}

func writeProbeErrorNamesDir(t *testing.T, build func(world) subject) {
	s := build(world{dirs: map[string][]string{"ro": nil}, readOnly: []string{"ro"}})

	pe, ok := errors.AsType[*fs.PathError](s.sys.WriteProbe(s.dir("ro")))
	if !ok || pe.Path != s.dir("ro") || !errors.Is(pe, fs.ErrPermission) {
		t.Fatalf("WriteProbe(ro) = %v, want a PathError on %s wrapping fs.ErrPermission", pe, s.dir("ro"))
	}
}
