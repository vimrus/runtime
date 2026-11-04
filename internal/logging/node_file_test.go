package logging

import "testing"

func TestJSONLFileNameOmitsEmptyNodeID(t *testing.T) {
	if got := JSONLFileName("runtime", ""); got != "runtime.jsonl" {
		t.Fatalf("single-node file name = %q", got)
	}
	if got := JSONLFileName("access", "node-a"); got != "access-node-a.jsonl" {
		t.Fatalf("multi-node file name = %q", got)
	}
}
