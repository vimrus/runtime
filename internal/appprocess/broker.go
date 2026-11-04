package appprocess

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vimrus/runtime/internal/config"
)

type State string

const (
	StateStarting    State = "starting"
	StateHandshaking State = "handshaking"
	StateReady       State = "ready"
	StateBusy        State = "busy"
	StateBackoff     State = "backoff"
	StateFailed      State = "failed"
	StateDraining    State = "draining"
	StateStopped     State = "stopped"
)

// Status is the local control-plane view of one resident process.
type Status struct {
	AppID        string    `json:"appID"`
	Name         string    `json:"name"`
	State        State     `json:"state"`
	PID          int       `json:"pid,omitempty"`
	InstanceID   string    `json:"instanceId,omitempty"`
	Required     bool      `json:"required"`
	Pending      int       `json:"pending"`
	Overdue      bool      `json:"overdue,omitempty"`
	Restarts     int       `json:"restarts"`
	LastStarted  time.Time `json:"lastStarted,omitempty"`
	LastReady    time.Time `json:"lastReady,omitempty"`
	LastExit     time.Time `json:"lastExit,omitempty"`
	LastExitCode int       `json:"lastExitCode,omitempty"`
	LastError    string    `json:"lastError,omitempty"`
}

type attempt struct {
	instanceID string
	token      string
	ready      chan struct{}
	readyOnce  sync.Once
}

type pendingCall struct {
	request   CallRequest
	deadline  time.Time
	delivered bool
	result    chan callResult
}

type broker struct {
	config config.AppProcess
	appID  string

	mu           sync.Mutex
	state        State
	attempt      *attempt
	pending      []*pendingCall
	delivered    *pendingCall
	wake         chan struct{}
	draining     bool
	pid          int
	restarts     int
	lastStarted  time.Time
	lastReady    time.Time
	lastExit     time.Time
	lastExitCode int
	lastError    string
}

func newBroker(appID string, cfg config.AppProcess) *broker {
	return &broker{
		config: cfg,
		appID:  appID,
		state:  StateStopped,
		wake:   make(chan struct{}, 1),
	}
}

func (b *broker) beginAttempt(instanceID, token string, pid int, at time.Time) <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.attempt = &attempt{
		instanceID: instanceID,
		token:      token,
		ready:      make(chan struct{}),
	}
	b.state = StateHandshaking
	b.pid = pid
	b.lastStarted = at
	b.draining = false
	signal(b.wake)
	return b.attempt.ready
}

func (b *broker) ready(instanceID, token string, at time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	current := b.attempt
	if current == nil || current.instanceID != instanceID ||
		!tokenMatches(current.token, token) {
		return newProtocolError("stale_instance", "process instance is not current", "", http.StatusConflict)
	}
	if b.draining {
		return newProtocolError("draining", "process is shutting down", "", http.StatusServiceUnavailable)
	}
	current.readyOnce.Do(func() { close(current.ready) })
	b.state = StateReady
	b.lastReady = at
	b.lastError = ""
	signal(b.wake)
	return nil
}

func (b *broker) call(ctx context.Context, request CallRequest) (CallResponse, error) {
	if request.TimeoutMS <= 0 || time.Duration(request.TimeoutMS)*time.Millisecond > b.config.RequestTimeout {
		request.TimeoutMS = int(b.config.RequestTimeout / time.Millisecond)
	}
	timeout := time.Duration(request.TimeoutMS) * time.Millisecond
	deadline := time.Now().Add(timeout)
	waiter := &pendingCall{
		request:  request,
		deadline: deadline,
		result:   make(chan callResult, 1),
	}

	b.mu.Lock()
	if b.draining {
		b.mu.Unlock()
		return CallResponse{}, newProtocolError("process_draining", "process is shutting down", "not_delivered", http.StatusServiceUnavailable)
	}
	if b.attempt == nil || (b.state != StateReady && b.state != StateBusy) {
		b.mu.Unlock()
		return CallResponse{}, newProtocolError("process_unavailable", "process is not ready", "not_delivered", http.StatusServiceUnavailable)
	}
	if len(b.pending) >= b.config.MaxPending {
		b.mu.Unlock()
		return CallResponse{}, newProtocolError("process_busy", "process request queue is full", "not_delivered", http.StatusTooManyRequests)
	}
	b.pending = append(b.pending, waiter)
	signal(b.wake)
	b.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-waiter.result:
		return result.response, result.err
	case <-ctx.Done():
		return b.abandon(waiter, "request canceled")
	case <-timer.C:
		return b.abandon(waiter, "process request deadline exceeded")
	}
}

func (b *broker) abandon(waiter *pendingCall, message string) (CallResponse, error) {
	b.mu.Lock()
	if waiter.delivered || b.delivered == waiter {
		b.mu.Unlock()
		return CallResponse{}, newProtocolError("outcome_unknown", message, "unknown", http.StatusGatewayTimeout)
	}
	for index, current := range b.pending {
		if current == waiter {
			b.pending = append(b.pending[:index], b.pending[index+1:]...)
			b.mu.Unlock()
			return CallResponse{}, newProtocolError("deadline_exceeded", message, "not_delivered", http.StatusGatewayTimeout)
		}
	}
	b.mu.Unlock()
	// A reply may have completed concurrently with cancellation. Preserve the
	// completed result instead of reporting an unknown outcome.
	select {
	case result := <-waiter.result:
		return result.response, result.err
	default:
		return CallResponse{}, newProtocolError("outcome_unknown", message, "unknown", http.StatusGatewayTimeout)
	}
}

func (b *broker) next(ctx context.Context, instanceID, token string, wait time.Duration) (*NextResponse, error) {
	if wait <= 0 {
		wait = defaultPollTimeout * time.Millisecond
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		b.mu.Lock()
		current := b.attempt
		if current == nil || current.instanceID != instanceID ||
			!tokenMatches(current.token, token) {
			b.mu.Unlock()
			return nil, newProtocolError("stale_instance", "process instance is not current", "", http.StatusConflict)
		}
		if b.draining {
			b.state = StateDraining
			b.mu.Unlock()
			return &NextResponse{Schema: SchemaVersion, Type: "shutdown"}, nil
		}
		if b.delivered == nil && len(b.pending) > 0 {
			waiter := b.pending[0]
			b.pending = b.pending[1:]
			waiter.delivered = true
			b.delivered = waiter
			b.state = StateBusy
			request := WorkRequest{
				Schema:    SchemaVersion,
				RequestID: waiter.request.RequestID,
				Method:    waiter.request.Method,
				Payload:   waiter.request.Payload,
				Deadline:  waiter.deadline.UTC().Format(time.RFC3339Nano),
			}
			b.mu.Unlock()
			return &NextResponse{Schema: SchemaVersion, Type: "request", Request: &request}, nil
		}
		wake := b.wake
		b.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, nil
		case <-timer.C:
			return nil, nil
		case <-wake:
		}
	}
}

func (b *broker) reply(instanceID, token string, reply ReplyRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	current := b.attempt
	if current == nil || current.instanceID != instanceID ||
		!tokenMatches(current.token, token) {
		return newProtocolError("stale_instance", "process instance is not current", "", http.StatusConflict)
	}
	waiter := b.delivered
	if waiter == nil || waiter.request.RequestID != reply.RequestID {
		return newProtocolError("unknown_request", "request is not awaiting a reply", "", http.StatusConflict)
	}
	b.delivered = nil
	b.state = StateReady
	if reply.Error != nil {
		waiter.result <- callResult{err: newProtocolError(reply.Error.Code, sanitizeMessage(reply.Error.Message), "delivered", http.StatusBadGateway)}
	} else {
		waiter.result <- callResult{response: CallResponse{
			Schema:    SchemaVersion,
			RequestID: reply.RequestID,
			Result:    reply.Result,
		}}
	}
	signal(b.wake)
	return nil
}

func (b *broker) beginDrain() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.draining = true
	b.state = StateDraining
	for _, waiter := range b.pending {
		waiter.result <- callResult{err: newProtocolError("process_draining", "process is shutting down", "not_delivered", http.StatusServiceUnavailable)}
	}
	b.pending = nil
	signal(b.wake)
}

func (b *broker) failAttempt(instanceID string, state State, exitCode int, errText string, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.attempt != nil && b.attempt.instanceID != instanceID {
		return
	}
	for _, waiter := range b.pending {
		waiter.result <- callResult{err: newProtocolError("process_restarted", "process exited before dispatch", "not_delivered", http.StatusServiceUnavailable)}
	}
	if b.delivered != nil {
		b.delivered.result <- callResult{err: newProtocolError("outcome_unknown", "process exited after dispatch", "unknown", http.StatusGatewayTimeout)}
	}
	b.pending = nil
	b.delivered = nil
	b.attempt = nil
	b.pid = 0
	b.state = state
	b.lastExit = at
	b.lastExitCode = exitCode
	b.lastError = errText
	signal(b.wake)
}

func (b *broker) setState(state State) {
	b.mu.Lock()
	b.state = state
	if state == StateBackoff || state == StateFailed || state == StateStopped {
		b.pid = 0
	}
	b.mu.Unlock()
}

func (b *broker) incrementRestarts() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.restarts++
	return b.restarts
}

func (b *broker) status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	overdue := b.delivered != nil && time.Now().After(b.delivered.deadline)
	status := Status{
		AppID:        b.appID,
		Name:         b.config.Name,
		State:        b.state,
		PID:          b.pid,
		Required:     b.config.Required,
		Pending:      len(b.pending),
		Overdue:      overdue,
		Restarts:     b.restarts,
		LastStarted:  b.lastStarted,
		LastReady:    b.lastReady,
		LastExit:     b.lastExit,
		LastExitCode: b.lastExitCode,
		LastError:    b.lastError,
	}
	if b.attempt != nil {
		status.InstanceID = b.attempt.instanceID
	}
	return status
}

func (b *broker) readyState() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func tokenMatches(expected, actual string) bool {
	if expected == "" || actual == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func signal(channel chan struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func sanitizeMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= 1024 {
		return message
	}
	cut := 1024
	for cut > 0 && !utf8.ValidString(message[:cut]) {
		cut--
	}
	return message[:cut]
}
