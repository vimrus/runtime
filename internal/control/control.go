// Package control implements the private Runtime administration protocol.
package control

import (
	"context"
	"encoding/json"
)

const ProtocolVersion = 1

// Options configures control-plane hardening.
type Options struct {
	Authorizer Authorizer
	Auditor    Auditor
}

type Request struct {
	Version   int             `json:"version"`
	Operation string          `json:"operation"`
	Action    string          `json:"action,omitempty"`
	Deep      bool            `json:"deep,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type Response struct {
	Version int             `json:"version"`
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Handler interface {
	HandleControl(context.Context, Request) Response
}

func Success(value any) Response {
	data, err := json.Marshal(value)
	if err != nil {
		return Failure("internal", "encode control response")
	}
	return Response{Version: ProtocolVersion, OK: true, Result: data}
}

func Failure(code, message string) Response {
	return Response{Version: ProtocolVersion, Error: &Error{Code: code, Message: message}}
}
