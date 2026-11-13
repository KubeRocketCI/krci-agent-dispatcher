// Package selftest checks the agent image contract for krci-agent-harness selftest.
package selftest

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

const (
	// MountPath is where the dispatcher mounts the harness image, read-only.
	MountPath = "/opt/krci-agent"
	// WorkDir is the agent image's work directory.
	WorkDir = "/work"
	// AgentUID is the user the agent image runs as.
	AgentUID = 1000
)

const (
	nameAgentUID      = "agent_uid"
	nameHomeWritable  = "home_writable"
	nameWorkWritable  = "work_writable"
	nameMountPathFree = "mount_path_free"
)

// tools must be on PATH; each has the check path_<tool>.
var tools = [...]string{"claude", "git", "sh"}

// Check is one result of an agent image contract check.
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

func result(name string, err error) Check {
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}

	return Check{Name: name, OK: true}
}

// AgentImageChecks checks the agent image contract, except the profile file, which the handshake checks.
// Run it with the binary outside MountPath: a harness mounted at MountPath hides what the image holds there.
func AgentImageChecks(sys System) []Check {
	out := make([]Check, 0, len(tools))

	for _, tool := range tools {
		_, err := sys.LookPath(tool)
		out = append(out, result("path_"+tool, err))
	}

	homeErr := errHomeUnset
	if home := sys.Getenv("HOME"); home != "" {
		homeErr = sys.WriteProbe(home)
	}

	return append(out,
		agentUID(sys),
		result(nameHomeWritable, homeErr),
		result(nameWorkWritable, sys.WriteProbe(WorkDir)),
		mountPathFree(sys),
	)
}

var errHomeUnset = errors.New("HOME is not set")

func agentUID(sys System) Check {
	if uid := sys.UID(); uid != AgentUID {
		return Check{Name: nameAgentUID, Detail: fmt.Sprintf("uid %d, want %d", uid, AgentUID)}
	}

	return Check{Name: nameAgentUID, OK: true}
}

// mountPathFree passes on an absent or empty MountPath.
func mountPathFree(sys System) Check {
	entries, err := sys.ReadDir(MountPath)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Check{Name: nameMountPathFree, OK: true}
	case err != nil:
		return result(nameMountPathFree, err)
	case len(entries) > 0:
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}

		return Check{Name: nameMountPathFree, Detail: MountPath + " holds " + strings.Join(names, ", ")}
	default:
		return Check{Name: nameMountPathFree, OK: true}
	}
}
