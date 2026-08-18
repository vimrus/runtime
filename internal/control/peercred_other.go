//go:build !linux && !windows

package control

import (
	"fmt"
	"net"
	"os"
)

// peerCredentials is a conservative fallback for non-Linux Unix platforms:
// the owning process identity is not trusted, so the operation is denied
// unless the process itself can be identified through a future platform
// implementation. Runtime currently only targets Linux and Windows.
func peerCredentials(_ *net.UnixConn) (Peer, error) {
	return Peer{UID: os.Geteuid(), GID: os.Getegid()}, fmt.Errorf("peer credentials are not supported on this platform")
}
