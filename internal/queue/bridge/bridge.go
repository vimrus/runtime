// Package bridge defines the private, versioned PHP Queue Bridge contract.
package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	SchemaVersion    = 1
	MaxRequestBytes  = 64 * 1024
	MaxResponseBytes = 64 * 1024
	MaxBatchSize     = 128
)

const (
	PathCapabilities = "/internal/runtime/queue/v1/capabilities"
	PathClaim        = "/internal/runtime/queue/v1/claim"
	PathExecute      = "/internal/runtime/queue/v1/execute"
	PathHeartbeat    = "/internal/runtime/queue/v1/heartbeat"
	PathReap         = "/internal/runtime/queue/v1/reap"
	PathStats        = "/internal/runtime/queue/v1/stats"
	PathControl      = "/internal/runtime/queue/v1/control"
)

const (
	ErrorUnauthenticated   = "unauthenticated"
	ErrorUnsupportedSchema = "unsupported_schema"
	ErrorInvalidRequest    = "invalid_request"
	ErrorRequestTooLarge   = "request_too_large"
	ErrorInvalidResponse   = "invalid_response"
	ErrorUnavailable       = "unavailable"
	ErrorConflict          = "conflict"
	ErrorInternal          = "internal"
)

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e Error) Error() string {
	return e.Code + ": " + e.Message
}

type Request interface {
	Validate() error
}

type Response interface {
	Validate() error
}

type CapabilitiesRequest struct {
	Schema     int    `json:"schema"`
	NodeID     string `json:"nodeID"`
	InstanceID string `json:"instanceID"`
}

func (r CapabilitiesRequest) Validate() error {
	return validateIdentity(r.Schema, r.NodeID, r.InstanceID)
}

type CapabilitiesResponse struct {
	Schema        int    `json:"schema"`
	QueueSchema   int    `json:"queueSchema"`
	Driver        string `json:"driver"`
	ClaimMode     string `json:"claimMode"`
	MaxClaimBatch int    `json:"maxClaimBatch"`
	Error         *Error `json:"error,omitempty"`
}

func (r CapabilitiesResponse) Validate() error {
	if err := validateResponseSchema(r.Schema, r.Error); err != nil {
		return err
	}
	if r.Error != nil {
		return nil
	}
	if r.QueueSchema < 1 || r.Driver == "" || (r.ClaimMode != "portable_cas" && r.ClaimMode != "skip_locked") || r.MaxClaimBatch < 1 || r.MaxClaimBatch > MaxBatchSize {
		return fmt.Errorf("invalid capabilities response")
	}
	return nil
}

type ClaimRequest struct {
	Schema       int      `json:"schema"`
	NodeID       string   `json:"nodeID"`
	InstanceID   string   `json:"instanceID"`
	WorkerID     string   `json:"workerID"`
	Queues       []string `json:"queues"`
	Limit        int      `json:"limit"`
	LeaseSeconds int      `json:"leaseSeconds"`
}

func (r ClaimRequest) Validate() error {
	if err := validateIdentity(r.Schema, r.NodeID, r.InstanceID); err != nil {
		return err
	}
	if r.WorkerID == "" || len(r.Queues) == 0 || len(r.Queues) > MaxBatchSize || r.Limit < 1 || r.Limit > MaxBatchSize || r.LeaseSeconds < 1 {
		return fmt.Errorf("invalid claim request")
	}
	for _, queue := range r.Queues {
		if strings.TrimSpace(queue) == "" {
			return fmt.Errorf("invalid claim queue")
		}
	}
	return nil
}

type Lease struct {
	JobUUID        string `json:"jobUUID"`
	Queue          string `json:"queue"`
	Handler        string `json:"handler"`
	Attempt        int    `json:"attempt"`
	LeaseToken     string `json:"leaseToken"`
	LeaseUntil     string `json:"leaseUntil"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	TraceID        string `json:"traceID"`
}

func (l Lease) Validate() error {
	if l.JobUUID == "" || l.Queue == "" || l.Handler == "" || l.Attempt < 1 || l.LeaseToken == "" || l.LeaseUntil == "" || l.TimeoutSeconds < 1 || l.TraceID == "" {
		return fmt.Errorf("invalid lease")
	}
	return nil
}

type ClaimResponse struct {
	Schema int     `json:"schema"`
	Leases []Lease `json:"leases"`
	Error  *Error  `json:"error,omitempty"`
}

func (r ClaimResponse) Validate() error {
	if err := validateResponseSchema(r.Schema, r.Error); err != nil {
		return err
	}
	if r.Error != nil {
		return nil
	}
	if len(r.Leases) > MaxBatchSize {
		return fmt.Errorf("claim response exceeds batch limit")
	}
	for _, lease := range r.Leases {
		if err := lease.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type ExecuteRequest struct {
	Schema     int    `json:"schema"`
	JobUUID    string `json:"jobUUID"`
	Attempt    int    `json:"attempt"`
	LeaseToken string `json:"leaseToken"`
	TraceID    string `json:"traceID"`
}

func (r ExecuteRequest) Validate() error {
	if r.Schema != SchemaVersion {
		return fmt.Errorf("unsupported queue bridge schema %d", r.Schema)
	}
	if r.JobUUID == "" || r.Attempt < 1 || r.LeaseToken == "" || r.TraceID == "" {
		return fmt.Errorf("execute requires jobUUID, attempt, leaseToken and traceID")
	}
	return nil
}

type ExecutionResult string

const (
	ExecutionSuccess  ExecutionResult = "success"
	ExecutionRetry    ExecutionResult = "retry"
	ExecutionFailed   ExecutionResult = "failed"
	ExecutionCanceled ExecutionResult = "canceled"
)

type ExecuteResponse struct {
	Schema            int             `json:"schema"`
	Result            ExecutionResult `json:"result"`
	Code              string          `json:"code,omitempty"`
	Message           string          `json:"message,omitempty"`
	RetryAfterSeconds int             `json:"retryAfterSeconds,omitempty"`
	Error             *Error          `json:"error,omitempty"`
}

func (r ExecuteResponse) Validate() error {
	if err := validateResponseSchema(r.Schema, r.Error); err != nil {
		return err
	}
	if r.Error != nil {
		return nil
	}
	if r.Result != ExecutionSuccess && r.Result != ExecutionRetry && r.Result != ExecutionFailed && r.Result != ExecutionCanceled {
		return fmt.Errorf("invalid execution result")
	}
	if r.RetryAfterSeconds < 0 || (r.Result != ExecutionRetry && r.RetryAfterSeconds != 0) {
		return fmt.Errorf("invalid retry delay")
	}
	return nil
}

type HeartbeatRequest struct {
	Schema     int        `json:"schema"`
	NodeID     string     `json:"nodeID"`
	InstanceID string     `json:"instanceID"`
	Leases     []LeaseRef `json:"leases"`
}

type LeaseRef struct {
	JobUUID    string `json:"jobUUID"`
	Attempt    int    `json:"attempt"`
	LeaseToken string `json:"leaseToken"`
}

func (l LeaseRef) Validate() error {
	if l.JobUUID == "" || l.Attempt < 1 || l.LeaseToken == "" {
		return fmt.Errorf("invalid lease reference")
	}
	return nil
}
func (r HeartbeatRequest) Validate() error {
	if err := validateIdentity(r.Schema, r.NodeID, r.InstanceID); err != nil {
		return err
	}
	if len(r.Leases) < 1 || len(r.Leases) > MaxBatchSize {
		return fmt.Errorf("invalid heartbeat batch")
	}
	for _, lease := range r.Leases {
		if err := lease.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type LeaseStatus string

const (
	LeaseExtended LeaseStatus = "extended"
	LeaseStale    LeaseStatus = "stale"
	LeaseNotFound LeaseStatus = "not_found"
	LeaseError    LeaseStatus = "error"
)

type HeartbeatResult struct {
	JobUUID    string      `json:"jobUUID"`
	Attempt    int         `json:"attempt"`
	Status     LeaseStatus `json:"status"`
	LeaseUntil string      `json:"leaseUntil,omitempty"`
	Code       string      `json:"code,omitempty"`
}
type HeartbeatResponse struct {
	Schema  int               `json:"schema"`
	Results []HeartbeatResult `json:"results"`
	Error   *Error            `json:"error,omitempty"`
}

func (r HeartbeatResponse) Validate() error {
	if err := validateResponseSchema(r.Schema, r.Error); err != nil {
		return err
	}
	if r.Error != nil {
		return nil
	}
	if len(r.Results) > MaxBatchSize {
		return fmt.Errorf("heartbeat response exceeds batch limit")
	}
	for _, result := range r.Results {
		if result.JobUUID == "" || result.Attempt < 1 || (result.Status != LeaseExtended && result.Status != LeaseStale && result.Status != LeaseNotFound && result.Status != LeaseError) || (result.Status == LeaseExtended && result.LeaseUntil == "") {
			return fmt.Errorf("invalid heartbeat result")
		}
	}
	return nil
}

type ReapRequest struct {
	Schema     int    `json:"schema"`
	NodeID     string `json:"nodeID"`
	InstanceID string `json:"instanceID"`
	Limit      int    `json:"limit"`
}

func (r ReapRequest) Validate() error {
	if err := validateIdentity(r.Schema, r.NodeID, r.InstanceID); err != nil {
		return err
	}
	if r.Limit < 1 || r.Limit > MaxBatchSize {
		return fmt.Errorf("invalid reap limit")
	}
	return nil
}

type ReapResponse struct {
	Schema  int    `json:"schema"`
	Retried int    `json:"retried"`
	Failed  int    `json:"failed"`
	Error   *Error `json:"error,omitempty"`
}

func (r ReapResponse) Validate() error {
	if err := validateResponseSchema(r.Schema, r.Error); err != nil {
		return err
	}
	if r.Error == nil && (r.Retried < 0 || r.Failed < 0) {
		return fmt.Errorf("invalid reap response")
	}
	return nil
}

type StatsRequest struct {
	Schema int      `json:"schema"`
	Queues []string `json:"queues,omitempty"`
}

func (r StatsRequest) Validate() error {
	if r.Schema != SchemaVersion {
		return fmt.Errorf("unsupported queue bridge schema %d", r.Schema)
	}
	if len(r.Queues) > MaxBatchSize {
		return fmt.Errorf("too many queues")
	}
	return nil
}

type QueueStats struct {
	Queue             string `json:"queue"`
	Queued            int64  `json:"queued"`
	Running           int64  `json:"running"`
	Retrying          int64  `json:"retrying"`
	Failed            int64  `json:"failed"`
	OldestAvailableAt string `json:"oldestAvailableAt,omitempty"`
}
type StatsResponse struct {
	Schema int          `json:"schema"`
	Queues []QueueStats `json:"queues"`
	Error  *Error       `json:"error,omitempty"`
}

func (r StatsResponse) Validate() error {
	if err := validateResponseSchema(r.Schema, r.Error); err != nil {
		return err
	}
	if r.Error == nil && len(r.Queues) > MaxBatchSize {
		return fmt.Errorf("stats response exceeds queue limit")
	}
	return nil
}

type ControlAction string

const (
	ControlPause  ControlAction = "pause"
	ControlResume ControlAction = "resume"
	ControlCancel ControlAction = "cancel"
	ControlRetry  ControlAction = "retry"
)

type ControlRequest struct {
	Schema     int           `json:"schema"`
	Action     ControlAction `json:"action"`
	JobUUID    string        `json:"jobUUID,omitempty"`
	LeaseToken string        `json:"leaseToken,omitempty"`
	Queue      string        `json:"queue,omitempty"`
	TraceID    string        `json:"traceID"`
}

func (r ControlRequest) Validate() error {
	if r.Schema != SchemaVersion || r.TraceID == "" {
		return fmt.Errorf("invalid control request")
	}
	if r.Action != ControlPause && r.Action != ControlResume && r.Action != ControlCancel && r.Action != ControlRetry {
		return fmt.Errorf("invalid control action")
	}
	if (r.Action == ControlCancel || r.Action == ControlRetry) && (r.JobUUID == "" || r.LeaseToken == "") {
		return fmt.Errorf("control action requires jobUUID and leaseToken")
	}
	return nil
}

type ControlResponse struct {
	Schema  int    `json:"schema"`
	Applied bool   `json:"applied"`
	Error   *Error `json:"error,omitempty"`
}

func (r ControlResponse) Validate() error { return validateResponseSchema(r.Schema, r.Error) }

func Decode(data []byte, target Request) error {
	if len(data) > MaxRequestBytes {
		return fmt.Errorf("%s", ErrorRequestTooLarge)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode queue bridge request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode queue bridge request: multiple JSON values")
	}
	return target.Validate()
}

func Encode(value Response) ([]byte, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func DecodeResponse(data []byte, target Response) error {
	if len(data) > MaxResponseBytes {
		return fmt.Errorf("%s", ErrorInvalidResponse)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode queue bridge response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode queue bridge response: multiple JSON values")
	}
	return target.Validate()
}

func validateIdentity(schema int, nodeID, instanceID string) error {
	if schema != SchemaVersion {
		return fmt.Errorf("unsupported queue bridge schema %d", schema)
	}
	if nodeID == "" || instanceID == "" {
		return fmt.Errorf("nodeID and instanceID are required")
	}
	return nil
}
func validateResponseSchema(schema int, bridgeError *Error) error {
	if schema != SchemaVersion {
		return fmt.Errorf("unsupported queue bridge schema %d", schema)
	}
	if bridgeError != nil && (bridgeError.Code == "" || bridgeError.Message == "") {
		return fmt.Errorf("invalid bridge error")
	}
	return nil
}
