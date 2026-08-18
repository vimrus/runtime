// Package observability implements the bounded, node-owned metrics/log
// pipeline: event normalization, batch lifecycle, spooling, Parquet
// publishing, controlled queries and retention.
package observability

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const SchemaVersion = 1

type Kind string

const (
	KindMetrics Kind = "metrics"
	KindLogs    Kind = "logs"
)

// Event is the normalized telemetry envelope written to Parquet.
type Event struct {
	SchemaVersion int               `json:"schemaVersion"`
	EventTime     time.Time         `json:"eventTime"`
	IngestTime    time.Time         `json:"ingestTime"`
	ClusterID     string            `json:"clusterID"`
	NodeID        string            `json:"nodeID"`
	BootID        string            `json:"bootID"`
	EventID       string            `json:"eventID"`
	Source        string            `json:"source"`
	Kind          Kind              `json:"kind"`
	Level         string            `json:"level,omitempty"`
	Message       string            `json:"message,omitempty"`
	MetricName    string            `json:"metricName,omitempty"`
	MetricKind    string            `json:"metricKind,omitempty"`
	Value         *float64          `json:"value,omitempty"`
	Count         *uint64           `json:"count,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	TraceID       string            `json:"traceID,omitempty"`
	DurationMs    *float64          `json:"durationMs,omitempty"`
	StatusCode    *int              `json:"statusCode,omitempty"`
	Fields        map[string]string `json:"fields,omitempty"`
}

const (
	maxMessageLength = 4096
	maxFields        = 32
	maxLabels        = 16
)

// Normalize fills defaults, enforces bounds and applies final redaction.
func Normalize(event Event) (Event, error) {
	if event.SchemaVersion == 0 {
		event.SchemaVersion = SchemaVersion
	}
	if event.SchemaVersion != SchemaVersion {
		return Event{}, fmt.Errorf("unsupported observability schema %d", event.SchemaVersion)
	}
	if event.ClusterID == "" || event.NodeID == "" || event.BootID == "" || event.Source == "" {
		return Event{}, errors.New("clusterID, nodeID, bootID and source are required")
	}
	if event.Kind != KindMetrics && event.Kind != KindLogs {
		return Event{}, errors.New("event kind must be metrics or logs")
	}
	if event.EventID == "" {
		event.EventID = randomID()
	}
	if event.EventTime.IsZero() {
		event.EventTime = time.Now().UTC()
	}
	event.EventTime = event.EventTime.UTC()
	event.IngestTime = time.Now().UTC()
	if len(event.Message) > maxMessageLength {
		event.Message = event.Message[:maxMessageLength]
	}
	if len(event.Fields) > maxFields {
		event.Fields = firstKeys(event.Fields, maxFields)
	}
	if len(event.Labels) > maxLabels {
		event.Labels = firstKeys(event.Labels, maxLabels)
	}
	event.Fields = redactMap(event.Fields)
	event.Labels = redactMap(event.Labels)
	return event, nil
}

func redactMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	redacted := make(map[string]string, len(values))
	for key, value := range values {
		if sensitiveKey(key) {
			redacted[key] = "[REDACTED]"
			continue
		}
		lower := strings.ToLower(value)
		if strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "basic ") {
			redacted[key] = "[REDACTED]"
			continue
		}
		redacted[key] = value
	}
	return redacted
}

func sensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range []string{"password", "passwd", "secret", "token", "cookie", "session", "authorization", "private_key", "credential", "apikey", "api_key", "access_key"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func firstKeys(values map[string]string, limit int) map[string]string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	result := make(map[string]string, limit)
	for _, key := range keys {
		if len(result) >= limit {
			break
		}
		result[key] = values[key]
	}
	return result
}

func randomID() string {
	data := make([]byte, 12)
	_, _ = rand.Read(data)
	return hex.EncodeToString(data)
}
