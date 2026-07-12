//go:build !linux && !darwin

package server

import "net"

// peerUID is unavailable on this platform; access control falls back to the socket's
// filesystem permissions (0600 in a 0700 dir).
func peerUID(_ *net.UnixConn) (uid int, ok bool) { return 0, false }
