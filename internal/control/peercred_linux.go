//go:build linux

package control

import (
	"fmt"
	"net"
	"os/user"
	"syscall"
)

func peerCredentials(connection *net.UnixConn) (Peer, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var credentials *syscall.Ucred
	var credentialErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, credentialErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if credentialErr != nil {
		return Peer{}, credentialErr
	}
	peer := Peer{PID: int(credentials.Pid), UID: int(credentials.Uid), GID: int(credentials.Gid)}
	if account, err := user.LookupId(fmt.Sprintf("%d", credentials.Uid)); err == nil {
		peer.User = account.Username
	}
	return peer, nil
}
