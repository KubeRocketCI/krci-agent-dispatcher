package proctest

import "golang.org/x/sys/unix"

// sZomb is SZOMB of <sys/proc.h>.
const sZomb = 5

// zombie reports whether the kern.proc.pid sysctl shows pid in state SZOMB.
func zombie(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)

	return err == nil && kp.Proc.P_stat == sZomb
}
