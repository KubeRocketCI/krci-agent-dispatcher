package procgroup

import (
	"errors"
	"syscall"
	"unsafe"
)

// pPID is P_PID of waitid(2).
const pPID = 1

// awaitExit blocks until pid has exited and leaves it unreaped. It reports false when it cannot tell.
func awaitExit(pid int) bool {
	var info [128]byte

	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid),
			uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if errno == 0 {
			return true
		}

		if !errors.Is(errno, syscall.EINTR) {
			return false
		}
	}
}
