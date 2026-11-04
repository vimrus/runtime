//go:build duckdb

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vimrus/runtime/internal/olap"
)

func TestOLAPBridgePHPToParquetToQuery(t *testing.T) {
	root := t.TempDir()
	datasetRoot := filepath.Join(root, "dataset")
	stagingRoot := filepath.Join(root, "staging")
	writer, err := olap.NewDuckDBWriter(2, "256MB")
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := olap.NewPublisher(olap.PublisherConfig{
		DatasetRoot: datasetRoot,
		StagingRoot: stagingRoot,
	}, writer)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := olap.NewDuckDBQueryEngine(2, "256MB", 10*time.Second, 100, 2)
	if err != nil {
		t.Fatal(err)
	}

	token := "olap-integration-token"
	SetOLAPHandlersFor(token, publisher.Publish, func(ctx context.Context, request olap.QueryRequest) (olap.QueryResult, error) {
		return engine.Query(ctx, datasetRoot, request)
	})
	t.Cleanup(func() { SetOLAPHandlersFor(token, nil, nil) })

	stagingDir := filepath.Join(stagingRoot, "php-sync-1")
	if err := os.MkdirAll(stagingDir, 0o750); err != nil {
		t.Fatal(err)
	}
	data := "{\"id\":1,\"title\":\"one\",\"deleted\":0}\n" +
		"{\"id\":2,\"title\":\"two\",\"deleted\":0}\n"
	if err := os.WriteFile(filepath.Join(stagingDir, "data.jsonl"), []byte(data), 0o640); err != nil {
		t.Fatal(err)
	}

	schema := olap.Schema{
		Version:    olap.DatasetSchemaVersion,
		Table:      "zt_bug",
		PrimaryKey: "id",
		Columns: []olap.Column{
			{Name: "id", Type: "BIGINT"},
			{Name: "title", Type: "VARCHAR"},
			{Name: "deleted", Type: "BIGINT"},
			{Name: "execution", Type: "BIGINT"},
			{Name: "product", Type: "BIGINT"},
			{Name: "resolution", Type: "VARCHAR"},
			{Name: "severity", Type: "BIGINT"},
		},
	}
	publishBody, err := json.Marshal(olap.PublishRequest{
		SchemaVersion:      olap.DatasetSchemaVersion,
		Table:              "zt_bug",
		Mode:               olap.ModeSnapshot,
		SyncID:             "php-sync-1",
		ExpectedGeneration: 0,
		Schema:             schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishRequest := httptest.NewRequest(http.MethodPost, "/internal/runtime/olap/v1/publish", bytes.NewReader(publishBody))
	publishRequest.Header.Set(olapTokenHeader, token)
	publishRequest.Header.Set(olapSchemaHeader, "1")
	publishRecorder := httptest.NewRecorder()
	if err := (&OLAPPublish{Tokens: []string{token}}).ServeHTTP(publishRecorder, publishRequest, nil); err != nil {
		t.Fatal(err)
	}
	if publishRecorder.Code != http.StatusOK {
		t.Fatalf("publish status = %d, body = %s", publishRecorder.Code, publishRecorder.Body.String())
	}

	queryBody, err := json.Marshal(olap.QueryRequest{
		SchemaVersion: olap.QuerySchemaVersion,
		SQL:           `SELECT "id", "title" FROM "zt_bug" ORDER BY "id"`,
		MaxRows:       10,
	})
	if err != nil {
		t.Fatal(err)
	}
	queryRequest := httptest.NewRequest(http.MethodPost, "/internal/runtime/olap/v1/query", bytes.NewReader(queryBody))
	queryRequest.Header.Set(olapTokenHeader, token)
	queryRequest.Header.Set(olapSchemaHeader, "1")
	queryRecorder := httptest.NewRecorder()
	if err := (&OLAPQuery{Tokens: []string{token}}).ServeHTTP(queryRecorder, queryRequest, nil); err != nil {
		t.Fatal(err)
	}
	if queryRecorder.Code != http.StatusOK {
		t.Fatalf("query status = %d, body = %s", queryRecorder.Code, queryRecorder.Body.String())
	}
	var result olap.QueryResult
	if err := json.Unmarshal(queryRecorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Rows) != 2 || result.Rows[1]["title"] != "two" {
		t.Fatalf("unexpected query result: %#v", result)
	}
	if result.Generations["zt_bug"] != 1 || result.CompatibilityVersion != olap.SQLCompatibilityVersion {
		t.Fatalf("unexpected versions: %#v", result)
	}
}
