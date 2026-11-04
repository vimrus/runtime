// Package appprocess supervises application-owned PHP CLI processes and
// exposes a private, authenticated request/reply bridge to application PHP.
package appprocess

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const (
	SchemaVersion = 1
	BasePath      = "/internal/runtime/processes/v1"

	CallTokenHeader    = "X-Zentao-Process-Token"
	SchemaHeader       = "X-Zentao-Process-Schema"
	MaxRequestBytes    = 1024 * 1024
	defaultPollTimeout = 20_000
)

type CallRequest struct {
	Schema    int             `json:"schema"`
	RequestID string          `json:"requestId"`
	Method    string          `json:"method"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	TimeoutMS int             `json:"timeoutMs,omitempty"`
}

type CallResponse struct {
	Schema    int             `json:"schema"`
	RequestID string          `json:"requestId"`
	Result    json.RawMessage `json:"result,omitempty"`
}

type ReadyRequest struct {
	Schema     int    `json:"schema"`
	InstanceID string `json:"instanceId"`
}

type NextRequest struct {
	Schema     int    `json:"schema"`
	InstanceID string `json:"instanceId"`
}

type NextResponse struct {
	Schema  int          `json:"schema"`
	Type    string       `json:"type"`
	Request *WorkRequest `json:"request,omitempty"`
}

type WorkRequest struct {
	Schema    int             `json:"schema"`
	RequestID string          `json:"requestId"`
	Method    string          `json:"method"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Deadline  string          `json:"deadline"`
}

type ReplyRequest struct {
	Schema     int             `json:"schema"`
	InstanceID string          `json:"instanceId"`
	RequestID  string          `json:"requestId"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      *ReplyError     `json:"error,omitempty"`
}

type ReplyError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type WireError struct {
	Schema    int    `json:"schema"`
	RequestID string `json:"requestId,omitempty"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Delivery  string `json:"delivery,omitempty"`
}

type callResult struct {
	response CallResponse
	err      error
}

type protocolError struct {
	code     string
	message  string
	delivery string
	status   int
}

func (e *protocolError) Error() string { return e.message }

func newProtocolError(code, message, delivery string, status int) error {
	return &protocolError{code: code, message: message, delivery: delivery, status: status}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, requestID string, err error) {
	status := http.StatusBadRequest
	code := "invalid_request"
	message := "invalid process request"
	delivery := ""
	var protocolErr *protocolError
	if errors.As(err, &protocolErr) {
		status = protocolErr.status
		code = protocolErr.code
		message = protocolErr.message
		delivery = protocolErr.delivery
	}
	writeJSON(writer, status, WireError{
		Schema:    SchemaVersion,
		RequestID: requestID,
		Code:      code,
		Message:   message,
		Delivery:  delivery,
	})
}

func decodeStrict(writer http.ResponseWriter, request *http.Request, target any) error {
	if request.Header.Get(SchemaHeader) != "1" {
		return newProtocolError("unsupported_schema", "unsupported process bridge schema", "", http.StatusBadRequest)
	}
	if contentType := strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]); !strings.EqualFold(contentType, "application/json") {
		return newProtocolError("invalid_request", "process bridge requests must use application/json", "", http.StatusUnsupportedMediaType)
	}
	request.Body = http.MaxBytesReader(writer, request.Body, MaxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return newProtocolError("request_too_large", "process request exceeds 1 MiB", "", http.StatusRequestEntityTooLarge)
		}
		return newProtocolError("invalid_request", "invalid process bridge request", "", http.StatusBadRequest)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return newProtocolError("invalid_request", "process request must contain exactly one JSON object", "", http.StatusBadRequest)
	} else if !errors.Is(err, io.EOF) {
		return newProtocolError("invalid_request", "process request must contain exactly one JSON object", "", http.StatusBadRequest)
	}
	return nil
}
