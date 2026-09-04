package cache

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBridgeCapabilities(t *testing.T) {
	c, _ := New(1<<20, 4096)
	defer c.Close()
	response, err := Process(c, "capabilities", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(response, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["schema"].(float64) != SchemaVersion || parsed["backend"] != "memory" {
		t.Fatalf("unexpected capabilities: %s", response)
	}
}

func TestBridgeSetGetDelete(t *testing.T) {
	c, _ := New(1<<20, 4096)
	defer c.Close()

	setBody := `{"schema":1,"items":[{"namespace":"product","key":"p1","valueType":"json","value":"{\"id\":1}","ttl":60}]}`
	if _, err := Process(c, "set", []byte(setBody)); err != nil {
		t.Fatal(err)
	}
	getBody := `{"schema":1,"items":[{"namespace":"product","key":"p1"}]}`
	response, err := Process(c, "get", []byte(getBody))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Items []itemValue `json:"items"`
	}
	if err := json.Unmarshal(response, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Items) != 1 || !parsed.Items[0].Hit || parsed.Items[0].Value != `{"id":1}` {
		t.Fatalf("unexpected get response: %s", response)
	}

	delBody := `{"schema":1,"items":[{"namespace":"product","key":"p1"}]}`
	if _, err := Process(c, "delete", []byte(delBody)); err != nil {
		t.Fatal(err)
	}
	response, _ = Process(c, "get", []byte(getBody))
	_ = json.Unmarshal(response, &parsed)
	if parsed.Items[0].Hit {
		t.Fatal("delete did not remove value")
	}
}

func TestBridgeRejectsUnknownFieldsAndOversized(t *testing.T) {
	c, _ := New(1<<20, 16)
	defer c.Close()
	response, err := Process(c, "get", []byte(`{"schema":1,"items":[],"extra":true}`))
	if err != nil || bytes.Contains(response, []byte("invalid_request")) == false {
		t.Fatalf("unknown field must be rejected: %s %v", response, err)
	}
	body := bytes.Repeat([]byte("x"), MaxRequestBytes+1)
	response, err = Process(c, "get", body)
	if err != nil || bytes.Contains(response, []byte("request_too_large")) == false {
		t.Fatalf("oversized body must be rejected: %s %v", response, err)
	}
}

func TestBridgeRejectsValueTooLarge(t *testing.T) {
	c, _ := New(1<<20, 8)
	defer c.Close()
	response, err := Process(c, "set", []byte(`{"schema":1,"items":[{"namespace":"default","key":"k","valueType":"string","value":"123456789","ttl":60}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(response, []byte("value_too_large")) == false {
		t.Fatalf("expected value_too_large response: %s", response)
	}
}
