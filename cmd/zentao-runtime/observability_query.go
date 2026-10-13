package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vimrus/runtime/internal/observability"
	"github.com/vimrus/runtime/internal/observability/jsonl"
	"github.com/vimrus/runtime/internal/observability/query"
	"github.com/vimrus/runtime/internal/web"
)

// observabilityQueryBridge serves the loopback read-only query used by the
// PHP log viewer in flush mode. It queries Parquet (historical) and merges
// recent local JSONL (access/runtime), applies the same bounded filters, and
// returns rows shaped like JSONL events.
func (h *host) observabilityQueryBridge(ctx context.Context, request web.QueryRequest) (web.QueryResponse, error) {
	if h.queryEngine == nil {
		return web.QueryResponse{}, errors.New("observability query engine is not available")
	}

	filters := request.Filters
	now := time.Now().UTC()
	start := now.Add(-7 * 24 * time.Hour)
	end := now
	if filters.Start != "" {
		parsed, err := time.Parse(time.RFC3339, filters.Start)
		if err != nil {
			return web.QueryResponse{}, fmt.Errorf("invalid start timestamp")
		}
		start = parsed.UTC()
	}
	if filters.End != "" {
		parsed, err := time.Parse(time.RFC3339, filters.End)
		if err != nil {
			return web.QueryResponse{}, fmt.Errorf("invalid end timestamp")
		}
		end = parsed.UTC()
	}
	if filters.FromDate != "" {
		parsed, err := time.Parse("20060102", filters.FromDate)
		if err == nil {
			start = parsed.UTC()
		}
	}
	if filters.ToDate != "" {
		parsed, err := time.Parse("20060102", filters.ToDate)
		if err == nil {
			end = parsed.UTC().Add(24 * time.Hour)
		}
	}

	fieldValues := map[string]string{}
	for key, value := range filters.Fields {
		if value != "" {
			fieldValues[key] = value
		}
	}
	for key, value := range map[string]string{
		"topic":     filters.Topic,
		"module":    filters.Module,
		"method":    filters.Method,
		"account":   filters.Account,
		"requestID": filters.RequestID,
	} {
		if value != "" {
			fieldValues[key] = value
		}
	}

	h.mu.RLock()
	datasetRoot := h.config.Observability.DatasetRoot
	maxDays := h.config.Observability.LogDays
	if maxDays <= 0 {
		maxDays = 30
	}
	logsDir := h.logsDir
	sources := h.jsonlSources
	clusterID := h.config.Runtime.ClusterID
	if clusterID == "" {
		clusterID = "standalone"
	}
	nodeID := h.config.Runtime.NodeID
	h.mu.RUnlock()

	qconfig := query.Config{
		DatasetRoot: datasetRoot,
		MaxDays:     maxDays,
		MaxRows:     1000,
		Threads:     2,
		MemoryLimit: "256MB",
		Timeout:     10 * time.Second,
	}
	pageLimit := filters.Limit
	if pageLimit <= 0 {
		pageLimit = 200
	}
	options := query.Options{
		Kind:            observability.KindLogs,
		Start:           start,
		End:             end,
		Node:            filters.Node,
		Level:           filters.Level,
		EventID:         filters.EventID,
		MessageContains: filters.Message,
		FieldValues:     fieldValues,
		Limit:           pageLimit,
	}

	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	countTemplate, countParams, err := query.BuildCount(qconfig, options)
	if err != nil {
		return web.QueryResponse{}, err
	}
	countRows, err := h.queryEngine.Query(queryCtx, countTemplate, 1, countParams)
	if err != nil {
		return web.QueryResponse{}, fmt.Errorf("observability count failed")
	}
	parquetTotal := 0
	if len(countRows) > 0 {
		parquetTotal = rowCountValue(countRows[0])
	}

	// Recent local JSONL (not yet flushed) occupies the newest positions.
	recent := h.recentEventsForQuery(queryCtx, logsDir, nodeID, clusterID, sources, start, end, filters, fieldValues)

	parquetOffset := filters.Offset
	parquetLimit := pageLimit
	if filters.Offset < len(recent) {
		parquetOffset = 0
		parquetLimit = pageLimit
		if len(recent)-filters.Offset < pageLimit {
			parquetLimit = pageLimit - (len(recent) - filters.Offset)
		}
	} else {
		parquetOffset = filters.Offset - len(recent)
	}
	if parquetLimit <= 0 {
		parquetLimit = 0
	}
	options.Offset = parquetOffset
	options.Limit = parquetLimit

	var page []map[string]any
	if parquetLimit > 0 {
		template, params, limit, buildErr := query.Build(qconfig, options)
		if buildErr != nil {
			return web.QueryResponse{}, buildErr
		}
		rows, queryErr := h.queryEngine.Query(queryCtx, template, limit, params)
		if queryErr != nil {
			return web.QueryResponse{}, fmt.Errorf("observability query failed")
		}
		for _, row := range rows {
			page = append(page, parquetRowToEvent(row))
		}
	}

	// Merge recent rows that belong to the requested page.
	recentStart := filters.Offset
	if recentStart >= len(recent) {
		recentStart = len(recent)
	}
	recentEnd := recentStart + pageLimit
	if recentEnd > len(recent) {
		recentEnd = len(recent)
	}
	for _, event := range recent[recentStart:recentEnd] {
		page = append(page, eventToRow(event))
	}
	sort.SliceStable(page, func(i, j int) bool {
		return eventRowNanos(page[i]) > eventRowNanos(page[j])
	})
	if len(page) > pageLimit {
		page = page[:pageLimit]
	}

	return web.QueryResponse{
		List:      page,
		Total:     parquetTotal + len(recent),
		Truncated: parquetTotal+len(recent) > pageLimit,
	}, nil
}

func (h *host) recentEventsForQuery(ctx context.Context, logsDir, nodeID, clusterID string, sources []string, start, end time.Time, filters web.QueryFilters, fieldValues map[string]string) []observability.Event {
	if logsDir == "" || nodeID == "" {
		return nil
	}
	events, _, err := jsonl.ReadRecent(jsonl.Config{
		LogsDir:   logsDir,
		NodeID:    nodeID,
		ClusterID: clusterID,
		BootID:    instanceID(),
		Sources:   sources,
	})
	if err != nil {
		h.logger.Warn("observability query recent jsonl failed", slog.Any("error", err))
		return nil
	}
	matched := make([]observability.Event, 0, len(events))
	for _, event := range events {
		if event.EventTime.Before(start) || !event.EventTime.Before(end) {
			continue
		}
		if filters.Node != "" && event.NodeID != filters.Node {
			continue
		}
		if filters.Level != "" && !strings.EqualFold(event.Level, filters.Level) {
			continue
		}
		if filters.EventID != "" && event.EventID != filters.EventID {
			continue
		}
		if filters.Message != "" && !strings.Contains(strings.ToLower(event.Message), strings.ToLower(filters.Message)) {
			continue
		}
		ok := true
		for key, value := range fieldValues {
			if event.Fields[key] != value {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		matched = append(matched, event)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].EventTime.After(matched[j].EventTime)
	})
	return matched
}

func rowCountValue(row query.Row) int {
	for _, key := range []string{"total_count", "count(*)", "count_star()"} {
		if value, ok := row[key]; ok {
			switch typed := value.(type) {
			case int64:
				return int(typed)
			case int:
				return typed
			case float64:
				return int(typed)
			case string:
				if parsed, err := strconv.Atoi(typed); err == nil {
					return parsed
				}
			}
		}
	}
	return 0
}

func parquetRowToEvent(row query.Row) map[string]any {
	fields := map[string]any{}
	if raw, ok := row["fields"]; ok && raw != nil {
		text, okText := jsonText(raw)
		if okText {
			_ = json.Unmarshal([]byte(text), &fields)
		}
	}
	return map[string]any{
		"eventTime": row["eventTime"],
		"eventID":   row["eventID"],
		"node":      row["node"],
		"source":    row["source"],
		"level":     row["level"],
		"message":   row["message"],
		"traceID":   row["traceID"],
		"fields":    fields,
	}
}

func eventToRow(event observability.Event) map[string]any {
	fields := make(map[string]any, len(event.Fields))
	for key, value := range event.Fields {
		fields[key] = value
	}
	return map[string]any{
		"eventTime": event.EventTime,
		"eventID":   event.EventID,
		"node":      event.NodeID,
		"source":    event.Source,
		"level":     event.Level,
		"message":   event.Message,
		"traceID":   event.TraceID,
		"fields":    fields,
	}
}

func jsonText(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	case json.RawMessage:
		return string(typed), true
	}
	return "", false
}

func eventRowNanos(row map[string]any) int64 {
	switch typed := row["eventTime"].(type) {
	case time.Time:
		return typed.UnixNano()
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if parsed, err := time.Parse(layout, typed); err == nil {
				return parsed.UnixNano()
			}
		}
	case float64:
		return int64(typed * float64(time.Second))
	}
	return 0
}
