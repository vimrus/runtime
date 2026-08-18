package control

import (
	"context"
)

// Peer identifies the caller of a local control-plane operation.
type Peer struct {
	PID  int    `json:"pid,omitempty"`
	UID  int    `json:"uid,omitempty"`
	GID  int    `json:"gid,omitempty"`
	User string `json:"user,omitempty"`
}

// Authorizer decides whether a peer may invoke control-plane operations.
type Authorizer interface {
	Authorize(Peer) error
}

type peerContextKey struct{}

func withPeer(ctx context.Context, peer Peer) context.Context {
	return context.WithValue(ctx, peerContextKey{}, peer)
}

// PeerFrom returns the peer attached to the request context, if any.
func PeerFrom(ctx context.Context) (Peer, bool) {
	peer, ok := ctx.Value(peerContextKey{}).(Peer)
	return peer, ok
}
