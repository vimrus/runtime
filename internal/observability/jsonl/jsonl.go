// Package jsonl converts completed per-node JSONL log segments into the
// observability Parquet dataset via the existing publisher.
package jsonl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vimrus/runtime/internal/observability"
)

type Config struct {
	LogsDir   string
	NodeID    string
	ClusterID string
	BootID    string
	Sources   []string
	Publisher *observability.Publisher
	Interval  time.Duration
}

type SegmentState struct {
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

type Result struct {
	Segments  int `json:"segments"`
	Events    int `json:"events"`
	Skipped   int `json:"skipped"`
	Malformed int `json:"malformed"`
}

type Converter struct {
	config    Config
	statePath string
	mu        sync.Mutex
}

var (
	accessSegmentPattern  = regexp.MustCompile(`^access-(.+)-(\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}\.\d{3})-(time|size)\.jsonl$`)
	runtimeSegmentPattern = regexp.MustCompile(`^runtime-(.+)\.jsonl-\d{8}T\d{6}\.\d{9}Z\.log$`)
)

func New(config Config) (*Converter, error) {
	if config.LogsDir == "" || config.NodeID == "" || config.ClusterID == "" || config.BootID == "" || config.Publisher == nil {
		return nil, errors.New("logsDir, nodeID, clusterID, bootID and publisher are required")
	}
	if len(config.Sources) == 0 {
		config.Sources = []string{"access", "runtime"}
	}
	if config.Interval <= 0 {
		config.Interval = time.Hour
	}
	state := filepath.Join(config.LogsDir, ".jsonl-state-"+config.NodeID+".json")
	return &Converter{config: config, statePath: state}, nil
}

func (c *Converter) Run(ctx context.Context) {
	ticker := time.NewTicker(c.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = c.ConvertOnce(ctx)
		}
	}
}

func (c *Converter) ConvertOnce(ctx context.Context) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result Result
	segments, err := c.scanSegments()
	if err != nil {
		return result, err
	}
	state, err := c.loadState()
	if err != nil {
		return result, err
	}
	for _, segment := range segments {
		info, err := os.Stat(segment)
		if err != nil {
			continue
		}
		previous, seen := state[segment]
		if seen && previous.Size == info.Size() && previous.ModTime.Equal(info.ModTime().UTC()) {
			continue
		}
		events, malformed, err := c.parseSegment(segment)
		if err != nil {
			return result, err
		}
		for _, event := range events {
			if err := c.config.Publisher.Add(ctx, event); err != nil {
				return result, fmt.Errorf("add event from %s: %w", segment, err)
			}
		}
		if err := c.config.Publisher.Flush(ctx, observability.KindLogs); err != nil {
			return result, fmt.Errorf("flush events from %s: %w", segment, err)
		}
		state[segment] = SegmentState{Size: info.Size(), ModTime: info.ModTime().UTC()}
		result.Segments++
		result.Events += len(events)
		result.Malformed += malformed
	}
	if err := c.saveState(state); err != nil {
		return result, err
	}
	result.Skipped = len(segments) - result.Segments
	return result, nil
}

func (c *Converter) scanSegments() ([]string, error) {
	entries, err := os.ReadDir(c.config.LogsDir)
	if err != nil {
		return nil, err
	}
	var segments []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		for _, source := range c.config.Sources {
			var node string
			switch source {
			case "access":
				matches := accessSegmentPattern.FindStringSubmatch(name)
				if matches == nil {
					continue
				}
				node = matches[1]
			case "runtime":
				matches := runtimeSegmentPattern.FindStringSubmatch(name)
				if matches == nil {
					continue
				}
				node = matches[1]
			default:
				continue
			}
			if node == c.config.NodeID {
				segments = append(segments, filepath.Join(c.config.LogsDir, name))
				break
			}
		}
	}
	sort.Strings(segments)
	return segments, nil
}

func (c *Converter) parseSegment(path string) ([]observability.Event, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	source := "caddy.access"
	base := filepath.Base(path)
	if strings.HasPrefix(base, "runtime-") {
		source = "runtime"
	}
	var events []observability.Event
	malformed := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event, ok, err := parseLine(line, source, c.config.ClusterID, c.config.NodeID, c.config.BootID)
		if err != nil {
			malformed++
			continue
		}
		if ok {
			events = append(events, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, malformed, err
	}
	return events, malformed, nil
}

func parseLine(line, source, clusterID, nodeID, bootID string) (observability.Event, bool, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return observability.Event{}, false, err
	}
	event := observability.Event{
		SchemaVersion: observability.SchemaVersion,
		ClusterID:     clusterID,
		NodeID:        nodeID,
		BootID:        bootID,
		Source:        source,
		Kind:          observability.KindLogs,
		EventTime:     timestamp(raw),
		Fields:        make(map[string]string),
	}
	event.Level = stringValue(raw["level"])
	event.Message = stringValue(raw["msg"])
	if source == "caddy.access" {
		logger := stringValue(raw["logger"])
		if !strings.Contains(logger, "http.log.access") && !strings.Contains(logger, "http.log.error") {
			return observability.Event{}, false, nil
		}
		if status, ok := intValue(raw["status"]); ok {
			event.StatusCode = &status
		}
		if duration, ok := floatValue(raw["duration"]); ok {
			ms := duration * 1000
			event.DurationMs = &ms
		}
		if request, ok := raw["request"].(map[string]any); ok {
			event.Fields["method"] = stringValue(request["method"])
			event.Fields["uri"] = stringValue(request["uri"])
			event.Fields["host"] = stringValue(request["host"])
			event.Fields["remote_ip"] = stringValue(request["remote_ip"])
		}
		if size, ok := intValue(raw["size"]); ok {
			event.Fields["size"] = fmt.Sprintf("%d", size)
		}
		if errMsg := stringValue(raw["error"]); errMsg != "" {
			event.Fields["error"] = errMsg
			if event.Level == "" {
				event.Level = "error"
			}
		}
		event.Fields["logger"] = stringValue(raw["logger"])
	} else {
		for key, value := range raw {
			switch key {
			case "ts", "time", "level", "msg", "logger":
				continue
			}
			event.Fields[key] = stringify(value)
		}
	}
	normalized, err := observability.Normalize(event)
	if err != nil {
		return observability.Event{}, false, err
	}
	return normalized, true, nil
}

func timestamp(raw map[string]any) time.Time {
	for _, key := range []string{"ts", "time"} {
		if number, ok := floatValue(raw[key]); ok {
			return time.Unix(0, int64(number*1e9)).UTC()
		}
		if text, ok := raw[key].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
				return parsed.UTC()
			}
		}
	}
	return time.Now().UTC()
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return stringify(value)
}

func floatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	}
	return 0, false
}

func intValue(value any) (int, bool) {
	if number, ok := floatValue(value); ok {
		return int(number), true
	}
	return 0, false
}

func stringify(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return fmt.Sprintf("%v", typed)
	case map[string]any, []any:
		data, _ := json.Marshal(typed)
		return string(data)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func (c *Converter) loadState() (map[string]SegmentState, error) {
	data, err := os.ReadFile(c.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]SegmentState), nil
		}
		return nil, err
	}
	state := make(map[string]SegmentState)
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return state, nil
}

func (c *Converter) saveState(state map[string]SegmentState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.statePath, data, 0o600)
}
