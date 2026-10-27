package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vimrus/runtime/internal/bi"
)

func registerTestBIHandlers(token string) {
	SetBIHandlersFor(token,
		func(_ context.Context, request bi.PublishRequest) (bi.PublishResult, error) {
			return bi.PublishResult{Rows: request.ExpectedGeneration}, nil
		},
		func(_ context.Context, request bi.QueryRequest) (bi.QueryResult, error) {
			return bi.QueryResult{
				SchemaVersion: 1,
				Columns:       []string{"value", "offset"},
				Rows:          []map[string]any{{"value": request.MaxRows, "offset": request.Offset}},
			}, nil
		},
	)
}

func TestBIPublishAuthenticationAndResponse(t *testing.T) {
	token := "bi-test-token"
	registerTestBIHandlers(token)
	t.Cleanup(func() { SetBIHandlersFor(token, nil, nil) })

	body := []byte(`{"schemaVersion":1,"table":"zt_bug","mode":"snapshot","syncID":"sync-1","expectedGeneration":2,"schema":{"version":1,"table":"zt_bug","primaryKey":"id","columns":[{"name":"id","type":"BIGINT"}]}}`)
	request := httptest.NewRequest(http.MethodPost, "/internal/runtime/bi/v1/publish", bytes.NewReader(body))
	request.Header.Set(biTokenHeader, token)
	request.Header.Set(biSchemaHeader, "1")
	recorder := httptest.NewRecorder()
	if err := (&BIPublish{Tokens: []string{token}}).ServeHTTP(recorder, request, nil); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response bi.PublishResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Rows != 2 {
		t.Fatalf("rows = %d, want 2", response.Rows)
	}

	unauthorized := httptest.NewRequest(http.MethodPost, "/internal/runtime/bi/v1/publish", bytes.NewReader(body))
	unauthorizedRecorder := httptest.NewRecorder()
	if err := (&BIPublish{Tokens: []string{token}}).ServeHTTP(unauthorizedRecorder, unauthorized, nil); err != nil {
		t.Fatal(err)
	}
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorizedRecorder.Code)
	}
}

func TestBIQueryAuthenticationAndResponse(t *testing.T) {
	token := "bi-test-token"
	registerTestBIHandlers(token)
	t.Cleanup(func() { SetBIHandlersFor(token, nil, nil) })

	body := []byte(`{"schemaVersion":1,"sql":"SELECT 1","maxRows":50,"offset":7}`)
	request := httptest.NewRequest(http.MethodPost, "/internal/runtime/bi/v1/query", bytes.NewReader(body))
	request.Header.Set(biTokenHeader, token)
	request.Header.Set(biSchemaHeader, "1")
	recorder := httptest.NewRecorder()
	if err := (&BIQuery{Tokens: []string{token}}).ServeHTTP(recorder, request, nil); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response bi.QueryResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Rows) != 1 || response.Rows[0]["value"] != float64(50) || response.Rows[0]["offset"] != float64(7) {
		t.Fatalf("unexpected query response: %#v", response)
	}
}

func TestBIQueryDatasetUnavailable(t *testing.T) {
	token := "bi-test-token"
	SetBIHandlersFor(token,
		func(context.Context, bi.PublishRequest) (bi.PublishResult, error) { return bi.PublishResult{}, nil },
		func(context.Context, bi.QueryRequest) (bi.QueryResult, error) {
			return bi.QueryResult{}, bi.ErrDatasetUnavailable
		},
	)
	t.Cleanup(func() { SetBIHandlersFor(token, nil, nil) })

	body := []byte(`{"schemaVersion":1,"sql":"SELECT 1"}`)
	request := httptest.NewRequest(http.MethodPost, "/internal/runtime/bi/v1/query", bytes.NewReader(body))
	request.Header.Set(biTokenHeader, token)
	request.Header.Set(biSchemaHeader, "1")
	recorder := httptest.NewRecorder()
	if err := (&BIQuery{Tokens: []string{token}}).ServeHTTP(recorder, request, nil); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}
