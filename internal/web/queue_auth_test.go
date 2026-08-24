package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

type caddyHandlerFunc func(http.ResponseWriter, *http.Request) error

func (f caddyHandlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) error { return f(w, r) }

var _ caddyhttp.Handler = caddyHandlerFunc(nil)

func TestQueueAuthRejectsMissingOrWrongToken(t *testing.T) {
	for _, token := range []string{"", "wrong"} {
		req := httptest.NewRequest(http.MethodPost, "/internal/runtime/queue/v1/claim", nil)
		if token != "" {
			req.Header.Set("X-Zentao-Queue-Token", token)
		}
		rec := httptest.NewRecorder()
		called := false
		next := caddyHandlerFunc(func(http.ResponseWriter, *http.Request) error { called = true; return nil })
		(&QueueAuth{Token: "expected"}).ServeHTTP(rec, req, next)
		if rec.Code != http.StatusUnauthorized || called {
			t.Fatalf("token %q was accepted", token)
		}
	}
}

func TestQueueAuthPassesValidToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Zentao-Queue-Token", "expected")
	rec := httptest.NewRecorder()
	called := false
	next := caddyHandlerFunc(func(http.ResponseWriter, *http.Request) error { called = true; return nil })
	if err := (&QueueAuth{Token: "expected"}).ServeHTTP(rec, req, next); err != nil || !called {
		t.Fatalf("valid token was rejected: %v", err)
	}
}

func TestQueueAuthRejectsNonPost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Zentao-Queue-Token", "expected")
	rec := httptest.NewRecorder()
	called := false
	next := caddyHandlerFunc(func(http.ResponseWriter, *http.Request) error { called = true; return nil })
	(&QueueAuth{Token: "expected"}).ServeHTTP(rec, req, next)
	if rec.Code != http.StatusMethodNotAllowed || called {
		t.Fatalf("GET was not rejected")
	}
}
