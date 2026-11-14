package procgroup

import (
	"errors"
	"syscall"
)

// awaitExit blocks until pid has exited and leaves it unreaped. It reports false when it cannot tell.
func awaitExit(pid int) bool {
	kq, err := syscall.Kqueue()
	if err != nil {
		return false
	}

	defer func() { _ = syscall.Close(kq) }()

	changes := []syscall.Kevent_t{{
		Ident:  uint64(pid),
		Filter: syscall.EVFILT_PROC,
		Flags:  syscall.EV_ADD | syscall.EV_ONESHOT,
		Fflags: syscall.NOTE_EXIT,
	}}
	events := make([]syscall.Kevent_t, 1)

	for {
		n, err := syscall.Kevent(kq, changes, events, nil)

		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.ESRCH):
			return true
		case err != nil || n < 1:
			return false
		}

		ev := events[0]
		if ev.Flags&syscall.EV_ERROR == 0 {
			return ev.Fflags&syscall.NOTE_EXIT != 0
		}

		// An unreaped pid that is already gone fails the registration with ESRCH.
		return syscall.Errno(ev.Data) == syscall.ESRCH
	}
}
