package server

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
)

// listenUnix creates the Unix domain socket listener with defense-in-depth access
// control: the parent directory is 0700 and the socket is 0600, so only the owning
// user can reach it. The returned listener additionally verifies each peer's uid via
// peercred. A stale socket file from a previous run is removed first.
func listenUnix(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	// Tighten perms in case the directory pre-existed with looser bits.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod socket dir: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", path, err)
	}
	// The 0700 parent dir already blocks non-owners during the brief window before
	// this chmod, so there is no world-accessible exposure.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return &peercredListener{Listener: l, allowedUID: os.Getuid()}, nil
}

// peercredListener rejects connections whose peer uid differs from the owner's. When
// the peer credential can't be read (unsupported platform), it defers to the socket's
// filesystem permissions rather than failing the connection.
type peercredListener struct {
	net.Listener
	allowedUID int
}

func (l *peercredListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uc, ok := c.(*net.UnixConn)
		if !ok {
			return c, nil
		}
		uid, ok := peerUID(uc)
		if ok && uid != l.allowedUID {
			// Reject this peer but keep serving — do not tear down the listener.
			log.Printf("peercred: rejecting connection from uid %d (want %d)", uid, l.allowedUID)
			_ = c.Close()
			continue
		}
		return c, nil
	}
}
