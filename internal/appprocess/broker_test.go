package appprocess

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/vimrus/runtime/internal/config"
)

func testProcessConfig() config.AppProcess {
	process := config.DefaultAppProcess()
	process.Name = "worker"
	process.Script = "worker.php"
	process.RequestTimeout = time.Second
	process.MaxPending = 2
	return process
}

func TestBrokerCallReply(t *testing.T) {
	process := testProcessConfig()
	broker := newBroker("zentao", process)
	ready := broker.beginAttempt("instance-a", "worker-token", 123, time.Now())
	if err := broker.ready("instance-a", "worker-token", time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("ready channel was not closed")
	}

	callDone := make(chan struct {
		response CallResponse
		err      error
	}, 1)
	go func() {
		response, err := broker.call(context.Background(), CallRequest{
			Schema:    SchemaVersion,
			RequestID: "request-a",
			Method:    "echo",
			Payload:   json.RawMessage(`{"value":1}`),
			TimeoutMS: 500,
		})
		callDone <- struct {
			response CallResponse
			err      error
		}{response: response, err: err}
	}()

	next, err := broker.next(context.Background(), "instance-a", "worker-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.Type != "request" || next.Request == nil || next.Request.RequestID != "request-a" {
		t.Fatalf("unexpected next response: %#v", next)
	}
	if err := broker.reply("instance-a", "worker-token", ReplyRequest{
		Schema:     SchemaVersion,
		InstanceID: "instance-a",
		RequestID:  "request-a",
		Result:     json.RawMessage(`{"accepted":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	result := <-callDone
	if result.err != nil {
		t.Fatal(result.err)
	}
	if string(result.response.Result) != `{"accepted":true}` {
		t.Fatalf("result = %s", result.response.Result)
	}
	if state := broker.readyState(); state != StateReady {
		t.Fatalf("state = %s", state)
	}
}

func TestBrokerTimeoutBeforeDeliveryIsNotDelivered(t *testing.T) {
	broker := newBroker("zentao", testProcessConfig())
	broker.beginAttempt("instance-a", "worker-token", 123, time.Now())
	if err := broker.ready("instance-a", "worker-token", time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := broker.call(context.Background(), CallRequest{
		Schema:    SchemaVersion,
		RequestID: "request-a",
		Method:    "slow",
		TimeoutMS: 20,
	})
	assertProtocolError(t, err, "deadline_exceeded", "not_delivered")
}

func TestBrokerTimeoutAfterDeliveryIsUnknown(t *testing.T) {
	broker := newBroker("zentao", testProcessConfig())
	broker.beginAttempt("instance-a", "worker-token", 123, time.Now())
	if err := broker.ready("instance-a", "worker-token", time.Now()); err != nil {
		t.Fatal(err)
	}
	callDone := make(chan error, 1)
	go func() {
		_, err := broker.call(context.Background(), CallRequest{
			Schema:    SchemaVersion,
			RequestID: "request-a",
			Method:    "slow",
			TimeoutMS: 20,
		})
		callDone <- err
	}()
	next, err := broker.next(context.Background(), "instance-a", "worker-token", time.Second)
	if err != nil || next == nil || next.Type != "request" {
		t.Fatalf("request was not delivered: %#v, %v", next, err)
	}
	assertProtocolError(t, <-callDone, "outcome_unknown", "unknown")
	if status := broker.status(); !status.Overdue {
		t.Fatal("delivered request past its deadline must be marked overdue")
	}
}

func TestBrokerRejectsStaleInstanceAndCrossProcessToken(t *testing.T) {
	broker := newBroker("zentao", testProcessConfig())
	broker.beginAttempt("instance-a", "worker-token", 123, time.Now())
	if err := broker.ready("instance-b", "worker-token", time.Now()); err == nil {
		t.Fatal("stale instance must be rejected")
	}
	if err := broker.ready("instance-a", "different-token", time.Now()); err == nil {
		t.Fatal("worker token mismatch must be rejected")
	}
}

func TestBrokerDrainFailsQueuedCalls(t *testing.T) {
	broker := newBroker("zentao", testProcessConfig())
	broker.beginAttempt("instance-a", "worker-token", 123, time.Now())
	if err := broker.ready("instance-a", "worker-token", time.Now()); err != nil {
		t.Fatal(err)
	}
	callDone := make(chan error, 1)
	go func() {
		_, err := broker.call(context.Background(), CallRequest{
			Schema:    SchemaVersion,
			RequestID: "request-a",
			Method:    "queued",
			TimeoutMS: 500,
		})
		callDone <- err
	}()
	waitForPendingCall(t, broker)
	broker.beginDrain()
	assertProtocolError(t, <-callDone, "process_draining", "not_delivered")
	next, err := broker.next(context.Background(), "instance-a", "worker-token", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.Type != "shutdown" {
		t.Fatalf("expected shutdown control frame, got %#v", next)
	}
}

func TestBrokerRejectsCallsWhenPendingQueueIsFull(t *testing.T) {
	process := testProcessConfig()
	process.MaxPending = 1
	broker := newBroker("zentao", process)
	broker.beginAttempt("instance-a", "worker-token", 123, time.Now())
	if err := broker.ready("instance-a", "worker-token", time.Now()); err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := broker.call(context.Background(), CallRequest{
			Schema:    SchemaVersion,
			RequestID: "request-a",
			Method:    "queued",
			TimeoutMS: 500,
		})
		firstDone <- err
	}()
	waitForPendingCall(t, broker)
	_, err := broker.call(context.Background(), CallRequest{
		Schema:    SchemaVersion,
		RequestID: "request-b",
		Method:    "queued",
		TimeoutMS: 500,
	})
	assertProtocolError(t, err, "process_busy", "not_delivered")
	broker.beginDrain()
	assertProtocolError(t, <-firstDone, "process_draining", "not_delivered")
}

func waitForPendingCall(t *testing.T, broker *broker) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for broker.status().Pending == 0 {
		if time.Now().After(deadline) {
			t.Fatal("call was not queued")
		}
		time.Sleep(time.Millisecond)
	}
}

func assertProtocolError(t *testing.T, err error, code, delivery string) {
	t.Helper()
	var protocolErr *protocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("expected protocol error, got %T %v", err, err)
	}
	if protocolErr.code != code || protocolErr.delivery != delivery {
		t.Fatalf("error = %#v, want code=%s delivery=%s", protocolErr, code, delivery)
	}
	if protocolErr.status < http.StatusBadRequest {
		t.Fatalf("unexpected HTTP status: %d", protocolErr.status)
	}
}
