//go:build !linux && !darwin

package procgroup

// awaitExit reports false: the platform has no wait for an exit that leaves the pid unreaped.
func awaitExit(int) bool { return false }
