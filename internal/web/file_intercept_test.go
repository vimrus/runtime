package web

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

type fileInterceptHandlerFunc func(http.ResponseWriter, *http.Request) error

func (f fileInterceptHandlerFunc) ServeHTTP(writer http.ResponseWriter, request *http.Request) error {
	return f(writer, request)
}

func TestFileInterceptServesAllowedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "download.bin")
	content := []byte{0xef, 0xbb, 0xbf, 0x00, 0x01, 0xff}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	handler := &FileIntercept{Roots: []string{root}}
	next := fileInterceptHandlerFunc(func(writer http.ResponseWriter, request *http.Request) error {
		writer.Header().Set(runtimeFileHeader, base64.StdEncoding.EncodeToString([]byte(path)))
		writer.Header().Set("Content-Disposition", `attachment; filename="download.bin"`)
		_, _ = writer.Write([]byte("must-not-leak"))
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "http://example.test/download", nil)
	recorder := httptest.NewRecorder()
	if err := handler.ServeHTTP(recorder, request, next); err != nil {
		t.Fatal(err)
	}

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Body.Bytes(); string(got) != string(content) {
		t.Fatalf("body = %v, want %v", got, content)
	}
	if got := recorder.Header().Get(runtimeFileHeader); got != "" {
		t.Fatalf("private file header leaked: %q", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); got == "" {
		t.Fatal("content disposition was not preserved")
	}
}

func TestFileInterceptRejectsPathOutsideRoots(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := &FileIntercept{Roots: []string{root}}
	next := fileInterceptHandlerFunc(func(writer http.ResponseWriter, request *http.Request) error {
		writer.Header().Set(runtimeFileHeader, base64.StdEncoding.EncodeToString([]byte(outside)))
		return nil
	})

	recorder := httptest.NewRecorder()
	if err := handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://example.test/", nil), next); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if recorder.Header().Get(runtimeFileHeader) != "" {
		t.Fatal("private file header leaked on rejection")
	}
}

func TestFileInterceptPassesNormalResponsesThrough(t *testing.T) {
	handler := &FileIntercept{}
	next := fileInterceptHandlerFunc(func(writer http.ResponseWriter, request *http.Request) error {
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("normal"))
		return nil
	})

	recorder := httptest.NewRecorder()
	if err := handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://example.test/", nil), next); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusCreated || recorder.Body.String() != "normal" {
		t.Fatalf("unexpected passthrough response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

var _ caddyhttp.Handler = fileInterceptHandlerFunc(nil)
