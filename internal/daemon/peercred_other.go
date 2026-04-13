//go:build !linux

package daemon

import "net"

// verifyPeer is a no-op on non-Linux platforms. SO_PEERCRED is Linux-specific;
// macOS uses LOCAL_PEERCRED with a different struct, and Windows has no Unix
// socket credential passing at all. The daemon still binds the socket with
// 0600-equivalent permissions via the directory, so this is not a regression.
func verifyPeer(conn net.Conn) error {
	return nil
}
