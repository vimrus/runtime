package fake

import (
	"context"
	"testing"

	"github.com/vimrus/runtime/internal/queue/bridge"
)

func TestBridgeRecordsRequestsAndReturnsProgrammedResult(t *testing.T) {
	b := New()
	b.HeartbeatResponse.Results = []bridge.HeartbeatResult{{JobUUID: "job-1", Attempt: 1, Status: bridge.LeaseStale, Code: "lease_lost"}}
	response, err := b.Heartbeat(context.Background(), bridge.HeartbeatRequest{Schema: bridge.SchemaVersion, NodeID: "node-a", InstanceID: "instance-a", Leases: []bridge.LeaseRef{{JobUUID: "job-1", Attempt: 1, LeaseToken: "token-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.HeartbeatRequests) != 1 || response.Results[0].Status != bridge.LeaseStale {
		t.Fatalf("unexpected fake bridge state: %#v", b)
	}
}
