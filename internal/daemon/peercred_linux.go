//go:build linux

package daemon

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// verifyPeer enforces that a Unix-socket peer runs under the same UID as
// the daemon. On shared hosts this stops another local user from connecting
// to the agent socket and injecting tasks.
//
// As of v0.1.0 the daemon refuses to bind anything other than a Unix
// socket (see listener.go Listen). A non-UnixConn reaching this point is
// an invariant violation — we fail closed instead of the previous
// "pass through" behavior, which silently accepted TCP connections
// with no credential check.
func verifyPeer(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("peercred: refusing non-unix connection (%T)", conn)
	}
	f, err := uc.File()
	if err != nil {
		return fmt.Errorf("peercred: file: %w", err)
	}
	defer f.Close()
	cred, err := syscall.GetsockoptUcred(int(f.Fd()), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	if err != nil {
		return fmt.Errorf("peercred: getsockopt: %w", err)
	}
	if cred.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("peercred: uid %d not allowed", cred.Uid)
	}
	return nil
}
