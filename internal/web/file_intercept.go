package web

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

const runtimeFileHeader = "X-Zentao-Runtime-File"

// FileIntercept lets the PHP response boundary hand a trusted file response
// to Caddy. PHP declares the path in a private response header; this handler
// validates it against the application roots and serves it without occupying
// a FrankenPHP worker for the duration of the file transfer.
type FileIntercept struct {
	Roots []string `json:"roots,omitempty"`
}

func (FileIntercept) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.zentao_file_intercept",
		New: func() caddy.Module { return new(FileIntercept) },
	}
}

func (f *FileIntercept) ServeHTTP(writer http.ResponseWriter, request *http.Request, next caddyhttp.Handler) error {
	capture := &fileInterceptWriter{ResponseWriter: writer}
	if err := next.ServeHTTP(capture, request); err != nil {
		return err
	}

	encodedPath := writer.Header().Get(runtimeFileHeader)
	if encodedPath == "" {
		capture.finish()
		return nil
	}

	writer.Header().Del(runtimeFileHeader)
	rawPath, err := base64.StdEncoding.DecodeString(encodedPath)
	if err != nil {
		http.Error(writer, "invalid runtime file response", http.StatusInternalServerError)
		return nil
	}

	path, err := f.resolvePath(string(rawPath))
	if err != nil {
		http.Error(writer, "runtime file response is not allowed", http.StatusInternalServerError)
		return nil
	}

	file, err := os.Open(path)
	if err != nil {
		http.Error(writer, "runtime file response is not readable", http.StatusInternalServerError)
		return nil
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(writer, "runtime file response is not a regular file", http.StatusInternalServerError)
		return nil
	}

	http.ServeContent(writer, request, filepath.Base(path), info.ModTime(), file)
	return nil
}

func (f *FileIntercept) resolvePath(path string) (string, error) {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	realPath, err = filepath.Abs(realPath)
	if err != nil {
		return "", err
	}

	for _, root := range f.Roots {
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		realRoot, err = filepath.Abs(realRoot)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(realRoot, realPath)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return realPath, nil
		}
	}
	return "", os.ErrPermission
}

type fileInterceptWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *fileInterceptWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
}

func (w *fileInterceptWriter) Write(body []byte) (int, error) {
	if w.ResponseWriter.Header().Get(runtimeFileHeader) != "" {
		return len(body), nil
	}
	w.commitHeader()
	return w.ResponseWriter.Write(body)
}

func (w *fileInterceptWriter) Flush() {
	if w.ResponseWriter.Header().Get(runtimeFileHeader) != "" {
		return
	}
	w.commitHeader()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *fileInterceptWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *fileInterceptWriter) finish() {
	if w.wroteHeader {
		return
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(w.status)
	w.wroteHeader = true
}

func (w *fileInterceptWriter) commitHeader() {
	if w.wroteHeader {
		return
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(w.status)
	w.wroteHeader = true
}

var (
	_ caddy.Module                = (*FileIntercept)(nil)
	_ caddyhttp.MiddlewareHandler = (*FileIntercept)(nil)
)

func init() { caddy.RegisterModule(FileIntercept{}) }
