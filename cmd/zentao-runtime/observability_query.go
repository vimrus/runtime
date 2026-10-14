package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/vimrus/runtime/internal/observability"
	"github.com/vimrus/runtime/internal/observability/query"
	"github.com/vimrus/runtime/internal/web"
)

// appQueryBridge serves the loopback read-only query used by one application's
// PHP log viewer in flush mode. The query is scoped to the application's own
// Parquet dataset, so applications never read each other's telemetry.
func (h *host) appQueryBridge(datasetRoot string) web.ObservabilityQueryHandler {
	return func(ctx context.Context, request web.QueryRequest) (web.QueryResponse, error) {
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
		maxDays := h.config.Observability.LogDays
		h.mu.RUnlock()
		if maxDays <= 0 {
			maxDays = 30
		}

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
			Offset:          filters.Offset,
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
		total := 0
		if len(countRows) > 0 {
			total = rowCountValue(countRows[0])
		}

		template, params, limit, err := query.Build(qconfig, options)
		if err != nil {
			return web.QueryResponse{}, err
		}
		rows, err := h.queryEngine.Query(queryCtx, template, limit, params)
		if err != nil {
			return web.QueryResponse{}, fmt.Errorf("observability query failed")
		}
		page := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			page = append(page, parquetRowToEvent(row))
		}
		return web.QueryResponse{
			List:      page,
			Total:     total,
			Truncated: total > filters.Offset+len(page),
		}, nil
	}
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
