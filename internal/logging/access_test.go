package logging

import (
	"encoding/json"
	"testing"
)

func TestAccessEncoderRedactsSensitiveFields(t *testing.T) {
	var encoder struct {
		Format string                     `json:"format"`
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(AccessEncoderRaw(), &encoder); err != nil {
		t.Fatal(err)
	}
	if encoder.Format != "filter" {
		t.Fatalf("encoder format = %q, want filter", encoder.Format)
	}

	var query struct {
		Filter  string `json:"filter"`
		Actions []struct {
			Type      string `json:"type"`
			Parameter string `json:"parameter"`
			Value     string `json:"value"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(encoder.Fields["request>uri"], &query); err != nil {
		t.Fatalf("decode URI filter: %v", err)
	}
	if query.Filter != "query" {
		t.Fatalf("URI filter = %q, want query", query.Filter)
	}
	found := false
	for _, action := range query.Actions {
		if action.Parameter == "token" && action.Type == "replace" && action.Value == "[REDACTED]" {
			found = true
		}
	}
	if !found {
		t.Fatalf("token query action missing: %#v", query.Actions)
	}

	for _, header := range sensitiveRequestHeaders {
		field, ok := encoder.Fields["request>headers>"+header]
		if !ok {
			t.Fatalf("%s header filter is missing", header)
		}
		var action struct {
			Filter string `json:"filter"`
		}
		if err := json.Unmarshal(field, &action); err != nil {
			t.Fatalf("decode %s header filter: %v", header, err)
		}
		if action.Filter != "delete" {
			t.Fatalf("%s header filter = %q, want delete", header, action.Filter)
		}
	}
}
