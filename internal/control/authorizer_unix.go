//go:build !windows

package control

import (
	"fmt"
	"os"
)

// SameUserAuthorizer accepts the effective user that owns the Runtime process
// and root (used by service managers and management tools).
type SameUserAuthorizer struct{}

func (SameUserAuthorizer) Authorize(peer Peer) error {
	euid := os.Geteuid()
	if peer.UID == euid || peer.UID == 0 {
		return nil
	}
	return fmt.Errorf("peer uid %d is not allowed", peer.UID)
}
