//go:build !linux && !darwin

package proctest

// zombie reports false: the platform has no zombie check.
func zombie(int) bool { return false }
