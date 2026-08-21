package bridge

import (
	"strings"
	"testing"
)

func TestDecodeRejectsUnknownFields(t *testing.T) {
	request := ClaimRequest{}
	err := Decode([]byte(`{"schema":1,"nodeID":"node-a","instanceID":"i-a","workerID":"w-a","channels":["mail"],"limit":1,"leaseSeconds":60,"payload":"forbidden"}`), &request)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want unknown field", err)
	}
}

func TestDecodeRejectsUnsupportedSchema(t *testing.T) {
	request := StatsRequest{}
	err := Decode([]byte(`{"schema":2}`), &request)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error = %v, want schema rejection", err)
	}
}

func TestExecuteRequiresFencingToken(t *testing.T) {
	err := (ExecuteRequest{Schema: SchemaVersion, UUID: "job-1", Attempt: 1, TraceID: "trace-1"}).Validate()
	if err == nil || !strings.Contains(err.Error(), "leaseToken") {
		t.Fatalf("error = %v, want leaseToken requirement", err)
	}
}

func TestHeartbeatAllowsPartialFailures(t *testing.T) {
	response := HeartbeatResponse{Schema: SchemaVersion, Results: []HeartbeatResult{
		{UUID: "job-1", Attempt: 1, Status: LeaseExtended, LeaseEnd: "2026-08-18T00:01:00Z"},
		{UUID: "job-2", Attempt: 3, Status: LeaseStale, Code: "lease_lost"},
	}}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRejectsOversizedRequest(t *testing.T) {
	request := StatsRequest{}
	err := Decode(make([]byte, MaxRequestBytes+1), &request)
	if err == nil || err.Error() != ErrorRequestTooLarge {
		t.Fatalf("error = %v, want request_too_large", err)
	}
}

func TestDecodeResponseRejectsInvalidJSON(t *testing.T) {
	response := ExecuteResponse{}
	err := DecodeResponse([]byte(`{"schema":1,"result":`), &response)
	if err == nil || !strings.Contains(err.Error(), "decode queue bridge response") {
		t.Fatalf("error = %v, want invalid response JSON", err)
	}
}

func TestDecodeResponseRejectsUnknownFields(t *testing.T) {
	response := StatsResponse{}
	err := DecodeResponse([]byte(`{"schema":1,"channels":[],"payload":"forbidden"}`), &response)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want unknown field", err)
	}
}

func TestClaimLeaseDoesNotContainPayload(t *testing.T) {
	data, err := Encode(ClaimResponse{Schema: SchemaVersion, Leases: []Lease{{UUID: "job-1", Channel: "mail", Handler: "mail.send", Attempt: 1, LeaseToken: "token-1", LeaseEnd: "2026-08-18T00:01:00Z", TimeoutSeconds: 60, TraceID: "trace-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "payload") {
		t.Fatalf("claim response exposes payload: %s", data)
	}
}
