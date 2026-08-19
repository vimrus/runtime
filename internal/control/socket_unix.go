//go:build !windows

package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxRequestBytes = 64 * 1024

type Server struct {
	path       string
	handler    Handler
	authorizer Authorizer
	auditor    Auditor
	listener   *net.UnixListener
	done       chan struct{}
	once       sync.Once
}

func Listen(path string, handler Handler) (*Server, error) {
	return ListenWithOptions(path, handler, Options{
		Authorizer: SameUserAuthorizer{},
		Auditor:    DiscardAuditor{},
	})
}

func ListenWithOptions(path string, handler Handler, options Options) (*Server, error) {
	if err := prepareSocket(path); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen control socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("set control socket permissions: %w", err)
	}
	authorizer := options.Authorizer
	if authorizer == nil {
		authorizer = SameUserAuthorizer{}
	}
	auditor := options.Auditor
	if auditor == nil {
		auditor = DiscardAuditor{}
	}
	server := &Server{path: path, handler: handler, authorizer: authorizer, auditor: auditor, listener: listener, done: make(chan struct{})}
	go server.accept()
	return server, nil
}

func (s *Server) Close() error {
	var closeErr error
	s.once.Do(func() {
		close(s.done)
		closeErr = s.listener.Close()
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			closeErr = errors.Join(closeErr, fmt.Errorf("remove control socket: %w", err))
		}
	})
	return closeErr
}

func (s *Server) accept() {
	for {
		connection, err := s.listener.AcceptUnix()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				continue
			}
		}
		go s.handle(connection)
	}
}

func (s *Server) handle(connection *net.UnixConn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	started := time.Now()
	peer, err := peerCredentials(connection)
	if err != nil {
		s.respond(connection, peer, "authenticate", started, Failure("forbidden", "unable to identify control plane caller"))
		return
	}
	reader := bufio.NewReader(io.LimitReader(connection, maxRequestBytes+1))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		s.respond(connection, peer, "invalid_request", started, Failure("invalid_request", "request must be a single newline-delimited JSON object"))
		return
	}
	if len(line) > maxRequestBytes {
		s.respond(connection, peer, "request_too_large", started, Failure("request_too_large", "request exceeds 65536 bytes"))
		return
	}

	var request Request
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		s.respond(connection, peer, "invalid_request", started, Failure("invalid_request", "invalid control request"))
		return
	}
	if request.Version != ProtocolVersion {
		s.respond(connection, peer, request.Operation, started, Failure("unsupported_version", "unsupported control protocol version"))
		return
	}
	if err := s.authorizer.Authorize(peer); err != nil {
		s.respond(connection, peer, request.Operation, started, Failure("forbidden", "control plane caller is not authorized"))
		return
	}
	ctx := withPeer(context.Background(), peer)
	response := s.handler.HandleControl(ctx, request)
	s.respond(connection, peer, request.Operation, started, response)
}

// respond persists the audit entry before writing the response so callers
// can never observe a reply before its audit record is durable.
func (s *Server) respond(connection *net.UnixConn, peer Peer, operation string, started time.Time, response Response) {
	errorCode := ""
	if !response.OK && response.Error != nil {
		errorCode = response.Error.Code
	}
	s.audit(AuditEntry{Time: started.UTC(), Operation: operation, Peer: peer, OK: response.OK, ErrorCode: errorCode, DurationMs: time.Since(started).Milliseconds()})
	s.write(connection, response)
}

func (s *Server) write(connection *net.UnixConn, response Response) {
	response.Version = ProtocolVersion
	data, err := json.Marshal(response)
	if err != nil {
		return
	}
	_, _ = connection.Write(append(data, '\n'))
}

func (s *Server) audit(entry AuditEntry) {
	s.auditor.Record(entry)
}

func Call(ctx context.Context, path string, request Request) (Response, error) {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return Response{}, fmt.Errorf("connect control socket: %w", err)
	}
	defer connection.Close()
	request.Version = ProtocolVersion
	data, err := json.Marshal(request)
	if err != nil {
		return Response{}, fmt.Errorf("encode control request: %w", err)
	}
	if _, err := connection.Write(append(data, '\n')); err != nil {
		return Response{}, fmt.Errorf("write control request: %w", err)
	}
	line, err := bufio.NewReader(io.LimitReader(connection, maxRequestBytes+1)).ReadBytes('\n')
	if err != nil {
		return Response{}, fmt.Errorf("read control response: %w", err)
	}
	var response Response
	if err := json.Unmarshal(line, &response); err != nil {
		return Response{}, fmt.Errorf("decode control response: %w", err)
	}
	return response, nil
}

func prepareSocket(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("control socket path exists and is not a socket: %s", path)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale control socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect control socket path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create control socket directory: %w", err)
	}
	return nil
}
