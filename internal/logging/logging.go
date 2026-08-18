// Package logging provides the Runtime structured log pipeline with
// redaction, size-limited rotation and source tagging.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Options struct {
	Level      slog.Level
	OutputPath string // empty means stderr
	MaxBytes   int64  // per-file rotation threshold, 0 disables rotation
	MaxBackups int    // retained rotated files
	NodeID     string
	InstanceID string
	Component  string
}

// Logger is a source-tagged structured logger.
type Logger struct {
	inner *slog.Logger
	close sync.Once
	file  *RotatingFile
}

func New(options Options) (*Logger, error) {
	level := options.Level
	if level == 0 {
		level = slog.LevelInfo
	}
	var output io.Writer = os.Stderr
	var rotating *RotatingFile
	if options.OutputPath != "" {
		if err := os.MkdirAll(filepath.Dir(options.OutputPath), 0o700); err != nil {
			return nil, err
		}
		var err error
		rotating, err = NewRotatingFile(options.OutputPath, options.MaxBytes, options.MaxBackups)
		if err != nil {
			return nil, err
		}
		output = rotating
	}
	handler := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level})
	base := slog.New(newRedactingHandler(handler))
	if options.NodeID != "" || options.InstanceID != "" || options.Component != "" {
		attrs := make([]any, 0, 6)
		if options.NodeID != "" {
			attrs = append(attrs, slog.String("node_id", options.NodeID))
		}
		if options.InstanceID != "" {
			attrs = append(attrs, slog.String("instance_id", options.InstanceID))
		}
		if options.Component != "" {
			attrs = append(attrs, slog.String("component", options.Component))
		}
		base = base.With(attrs...)
	}
	return &Logger{inner: base, file: rotating}, nil
}

func (l *Logger) Debug(message string, args ...any) { l.inner.Debug(message, args...) }
func (l *Logger) Info(message string, args ...any)  { l.inner.Info(message, args...) }
func (l *Logger) Warn(message string, args ...any)  { l.inner.Warn(message, args...) }
func (l *Logger) Error(message string, args ...any) { l.inner.Error(message, args...) }

func (l *Logger) With(args ...any) *Logger {
	return &Logger{inner: l.inner.With(args...), file: l.file}
}

func (l *Logger) Log(ctx context.Context, level slog.Level, message string, args ...any) {
	l.inner.Log(ctx, level, message, args...)
}

// Slog returns the underlying standard library logger for libraries that
// require *slog.Logger.
func (l *Logger) Slog() *slog.Logger {
	return l.inner
}

func (l *Logger) Close() error {
	var err error
	l.close.Do(func() {
		if l.file != nil {
			err = l.file.Close()
		}
	})
	return err
}

// redactingHandler filters sensitive attribute keys and values before the
// underlying handler serializes them.
type redactingHandler struct {
	next slog.Handler
}

func newRedactingHandler(next slog.Handler) *redactingHandler {
	return &redactingHandler{next: next}
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	record.Time = record.Time.UTC()
	var attrs []slog.Attr
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, redactAttr(attr))
		return true
	})
	copy := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	copy.AddAttrs(attrs...)
	return h.next.Handle(ctx, copy)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		redacted = append(redacted, redactAttr(attr))
	}
	return &redactingHandler{next: h.next.WithAttrs(redacted)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name)}
}

func redactAttr(attr slog.Attr) slog.Attr {
	if sensitiveKey(attr.Key) {
		return slog.String(attr.Key, "[REDACTED]")
	}
	value := attr.Value
	if value.Kind() == slog.KindString {
		if sensitiveValue(value.String()) {
			return slog.String(attr.Key, "[REDACTED]")
		}
	}
	if value.Kind() == slog.KindGroup {
		group := value.Group()
		redacted := make([]slog.Attr, 0, len(group))
		for _, child := range group {
			redacted = append(redacted, redactAttr(child))
		}
		attr.Value = slog.GroupValue(redacted...)
	}
	return attr
}

func sensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range []string{"password", "passwd", "secret", "token", "cookie", "session_id", "authorization", "private_key", "credential", "apikey", "api_key", "access_key", "bearer"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func sensitiveValue(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "basic ")
}
