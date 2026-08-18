//go:build windows

package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
)

const maxRequestBytes = 64 * 1024

// pipeSecurity grants access only to SYSTEM and the local Administrators
// group, so the control plane is never reachable from the public network or
// from ordinary user processes.
const pipeSecurity = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"

type Server struct {
	path       string
	handler    Handler
	authorizer Authorizer
	auditor    Auditor
	listener   net.Listener
	done       chan struct{}
	once       sync.Once
}

func Listen(path string, handler Handler) (*Server, error) {
	return ListenWithOptions(path, handler, Options{
		Authorizer: WindowsPipeAuthorizer{},
		Auditor:    DiscardAuditor{},
	})
}

func ListenWithOptions(path string, handler Handler, options Options) (*Server, error) {
	if path == "" {
		path = `\\.\pipe\zentao-runtime`
	}
	listener, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: pipeSecurity,
		MessageMode:        false,
		InputBufferSize:    maxRequestBytes + 1,
		OutputBufferSize:   maxRequestBytes + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("listen control pipe: %w", err)
	}
	authorizer := options.Authorizer
	if authorizer == nil {
		authorizer = WindowsPipeAuthorizer{}
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
	})
	return closeErr
}

func (s *Server) accept() {
	for {
		connection, err := s.listener.Accept()
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

func (s *Server) handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	started := time.Now()
	peer := Peer{}
	if err := s.authorizer.Authorize(peer); err != nil {
		s.write(connection, Failure("forbidden", "control plane caller is not authorized"))
		s.audit(AuditEntry{Time: started.UTC(), Operation: "authenticate", Peer: peer, OK: false, ErrorCode: "forbidden", DurationMs: time.Since(started).Milliseconds()})
		return
	}
	reader := bufio.NewReader(io.LimitReader(connection, maxRequestBytes+1))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		s.write(connection, Failure("invalid_request", "request must be a single newline-delimited JSON object"))
		s.audit(AuditEntry{Time: started.UTC(), Operation: "invalid_request", Peer: peer, OK: false, ErrorCode: "invalid_request", DurationMs: time.Since(started).Milliseconds()})
		return
	}
	if len(line) > maxRequestBytes {
		s.write(connection, Failure("request_too_large", "request exceeds 65536 bytes"))
		return
	}
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		s.write(connection, Failure("invalid_request", "invalid control request"))
		s.audit(AuditEntry{Time: started.UTC(), Operation: "invalid_request", Peer: peer, OK: false, ErrorCode: "invalid_request", DurationMs: time.Since(started).Milliseconds()})
		return
	}
	if request.Version != ProtocolVersion {
		s.write(connection, Failure("unsupported_version", "unsupported control protocol version"))
		return
	}
	response := s.handler.HandleControl(context.Background(), request)
	errorCode := ""
	if !response.OK && response.Error != nil {
		errorCode = response.Error.Code
	}
	s.audit(AuditEntry{Time: started.UTC(), Operation: request.Operation, Peer: peer, OK: response.OK, ErrorCode: errorCode, DurationMs: time.Since(started).Milliseconds()})
	s.write(connection, response)
}

func (s *Server) write(connection net.Conn, response Response) {
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
	if path == "" {
		path = `\\.\pipe\zentao-runtime`
	}
	connection, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return Response{}, fmt.Errorf("connect control pipe: %w", err)
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
