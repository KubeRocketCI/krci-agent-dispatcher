// Package harness holds the in-actor side of the dispatcher: the harness command and its checks.
package harness

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// MountPath is where the dispatcher mounts the harness image, read-only.
	MountPath = "/opt/krci-agent"
	// Command is the actor command; the harness image holds the binary at /krci-agent-harness.
	Command = MountPath + "/krci-agent-harness"
	// WorkDir is the agent image's work directory.
	WorkDir = "/work"
	// AgentUID is the user the agent image runs as.
	AgentUID = 1000
)

// Check is one result of a harness check.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// AllOK reports whether every check passed.
func AllOK(checks []Check) bool {
	for _, c := range checks {
		if !c.OK {
			return false
		}
	}

	return true
}

// System is the part of the operating system the checks read; tests replace it.
type System struct {
	UID        func() int
	Getenv     func(key string) string
	Executable func() (string, error)
	LookPath   func(name string) (string, error)
	ReadDir    func(dir string) ([]fs.DirEntry, error)
	WriteProbe func(dir string) error
	MountInfo  func() (io.ReadCloser, error)
}

// OS returns the System of the running process.
func OS() System {
	return System{
		UID:        os.Getuid,
		Getenv:     os.Getenv,
		Executable: os.Executable,
		LookPath:   exec.LookPath,
		ReadDir:    os.ReadDir,
		WriteProbe: writeProbeFile,
		MountInfo:  func() (io.ReadCloser, error) { return os.Open("/proc/self/mountinfo") },
	}
}

// writeProbeFile uses a unique name: checks can run concurrently.
func writeProbeFile(dir string) error {
	f, err := os.CreateTemp(dir, ".write-probe-*")
	if err != nil {
		return err
	}

	name := f.Name()

	if err := f.Close(); err != nil {
		return err
	}

	return os.Remove(name)
}

func result(name string, err error) Check {
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}

	return Check{Name: name, OK: true}
}

// ContractChecks checks the agent image contract; selftest runs them.
func ContractChecks(sys System) []Check {
	out := make([]Check, 0, 7)

	for _, bin := range []string{"claude", "git", "sh"} {
		_, err := sys.LookPath(bin)
		out = append(out, result("path_"+bin, err))
	}

	uid := sys.UID()
	out = append(out, Check{Name: "uid_1000", OK: uid == AgentUID, Detail: fmt.Sprintf("uid=%d", uid)})

	home := sys.Getenv("HOME")
	if home == "" {
		out = append(out, Check{Name: "home_writable", Detail: "HOME is not set"})
	} else {
		out = append(out, result("home_writable", sys.WriteProbe(home)))
	}

	out = append(out, result("work_writable", sys.WriteProbe(WorkDir)))
	out = append(out, mountPathFree(sys))

	return out
}

// mountPathFree passes on an absent or empty mount path, or when the harness runs from it.
func mountPathFree(sys System) Check {
	const name = "mount_path_free"

	if exe, err := sys.Executable(); err == nil && underMount(exe) {
		return Check{Name: name, OK: true, Detail: "harness mounted"}
	}

	entries, err := sys.ReadDir(MountPath)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Check{Name: name, OK: true}
	case err != nil:
		return result(name, err)
	case len(entries) > 0:
		return Check{Name: name, Detail: MountPath + " is not empty"}
	default:
		return Check{Name: name, OK: true}
	}
}

func underMount(path string) bool {
	return strings.HasPrefix(filepath.Clean(path), MountPath+"/")
}

// MountChecks checks the harness as the dispatcher mounts it: read-only at MountPath, running from it.
func MountChecks(sys System) []Check {
	exe, err := sys.Executable()
	if err == nil && !underMount(exe) {
		err = fmt.Errorf("%s is not under %s", exe, MountPath)
	}

	return []Check{readOnly(sys), result("runs_from_mount", err)}
}

// readOnly reads the mount options: as uid 1000, a write into the root-owned mount fails with EACCES before EROFS.
func readOnly(sys System) Check {
	const name = "mount_read_only"

	rc, err := sys.MountInfo()
	if err != nil {
		return result(name, err)
	}

	defer func() { _ = rc.Close() }()

	opts, err := mountOptions(rc, MountPath)
	if err != nil {
		return result(name, err)
	}

	detail := strings.Join(opts, ",")

	if !slices.Contains(opts, "ro") {
		return Check{Name: name, Detail: "mounted " + detail}
	}

	if sys.WriteProbe(MountPath) == nil {
		return Check{Name: name, Detail: "mounted " + detail + ", write succeeded"}
	}

	return Check{Name: name, OK: true, Detail: detail}
}

// mountOptions returns the per-mount options of the mount at dir; the last entry wins, as with stacked mounts.
func mountOptions(r io.Reader, dir string) ([]string, error) {
	var (
		opts  []string
		found bool
	)

	sc := bufio.NewScanner(r)
	for sc.Scan() {
		// mountinfo(5): id parent major:minor root mount-point options ...
		f := strings.Fields(sc.Text())
		if len(f) >= 6 && f[4] == dir {
			opts, found = strings.Split(f[5], ","), true
		}
	}

	if err := sc.Err(); err != nil {
		return nil, err
	}

	if !found {
		return nil, fmt.Errorf("no mount at %s", dir)
	}

	return opts, nil
}
