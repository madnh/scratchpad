//go:build linux

package server

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID reads the connecting peer's user id via SO_PEERCRED. ok=false when the
// credential can't be read (the caller then falls back to filesystem permissions).
func peerUID(uc *net.UnixConn) (uid int, ok bool) {
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	ctlErr := raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e == nil {
			uid, ok = int(cred.Uid), true
		}
	})
	if ctlErr != nil {
		return 0, false
	}
	return uid, ok
}
