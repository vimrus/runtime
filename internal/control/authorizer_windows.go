//go:build windows

package control

// WindowsPipeAuthorizer relies on the named pipe ACL to restrict callers to
// SYSTEM and Administrators; every connection that reaches the handler is
// therefore treated as authorized.
type WindowsPipeAuthorizer struct{}

func (WindowsPipeAuthorizer) Authorize(Peer) error { return nil }
