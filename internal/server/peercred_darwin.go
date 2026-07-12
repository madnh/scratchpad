//go:build darwin

package server

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID reads the connecting peer's user id via LOCAL_PEERCRED. ok=false when the
// credential can't be read (the caller then falls back to filesystem permissions).
func peerUID(uc *net.UnixConn) (uid int, ok bool) {
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	ctlErr := raw.Control(func(fd uintptr) {
		xu, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e == nil {
			uid, ok = int(xu.Uid), true
		}
	})
	if ctlErr != nil {
		return 0, false
	}
	return uid, ok
}
