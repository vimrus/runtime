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

	"github.com/vimrus/runtime/internal/bi"
)

func TestBIBridgePHPToParquetToQuery(t *testing.T) {
	root := t.TempDir()
	datasetRoot := filepath.Join(root, "dataset")
	stagingRoot := filepath.Join(root, "staging")
	writer, err := bi.NewDuckDBWriter(2, "256MB")
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := bi.NewPublisher(bi.PublisherConfig{
		DatasetRoot: datasetRoot,
		StagingRoot: stagingRoot,
	}, writer)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := bi.NewDuckDBQueryEngine(2, "256MB", 10*time.Second, 100, 2)
	if err != nil {
		t.Fatal(err)
	}

	token := "bi-integration-token"
	SetBIHandlersFor(token, publisher.Publish, func(ctx context.Context, request bi.QueryRequest) (bi.QueryResult, error) {
		return engine.Query(ctx, datasetRoot, request)
	})
	t.Cleanup(func() { SetBIHandlersFor(token, nil, nil) })

	stagingDir := filepath.Join(stagingRoot, "php-sync-1")
	if err := os.MkdirAll(stagingDir, 0o750); err != nil {
		t.Fatal(err)
	}
	data := "{\"id\":1,\"title\":\"one\",\"deleted\":0}\n" +
		"{\"id\":2,\"title\":\"two\",\"deleted\":0}\n"
	if err := os.WriteFile(filepath.Join(stagingDir, "data.jsonl"), []byte(data), 0o640); err != nil {
		t.Fatal(err)
	}

	schema := bi.Schema{
		Version:    bi.DatasetSchemaVersion,
		Table:      "zt_bug",
		PrimaryKey: "id",
		Columns: []bi.Column{
			{Name: "id", Type: "BIGINT"},
			{Name: "title", Type: "VARCHAR"},
			{Name: "deleted", Type: "BIGINT"},
			{Name: "execution", Type: "BIGINT"},
			{Name: "product", Type: "BIGINT"},
			{Name: "resolution", Type: "VARCHAR"},
			{Name: "severity", Type: "BIGINT"},
		},
	}
	publishBody, err := json.Marshal(bi.PublishRequest{
		SchemaVersion:      bi.DatasetSchemaVersion,
		Table:              "zt_bug",
		Mode:               bi.ModeSnapshot,
		SyncID:             "php-sync-1",
		ExpectedGeneration: 0,
		Schema:             schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishRequest := httptest.NewRequest(http.MethodPost, "/internal/runtime/bi/v1/publish", bytes.NewReader(publishBody))
	publishRequest.Header.Set(biTokenHeader, token)
	publishRequest.Header.Set(biSchemaHeader, "1")
	publishRecorder := httptest.NewRecorder()
	if err := (&BIPublish{Tokens: []string{token}}).ServeHTTP(publishRecorder, publishRequest, nil); err != nil {
		t.Fatal(err)
	}
	if publishRecorder.Code != http.StatusOK {
		t.Fatalf("publish status = %d, body = %s", publishRecorder.Code, publishRecorder.Body.String())
	}

	queryBody, err := json.Marshal(bi.QueryRequest{
		SchemaVersion: bi.QuerySchemaVersion,
		SQL:           `SELECT "id", "title" FROM "zt_bug" ORDER BY "id"`,
		MaxRows:       10,
	})
	if err != nil {
		t.Fatal(err)
	}
	queryRequest := httptest.NewRequest(http.MethodPost, "/internal/runtime/bi/v1/query", bytes.NewReader(queryBody))
	queryRequest.Header.Set(biTokenHeader, token)
	queryRequest.Header.Set(biSchemaHeader, "1")
	queryRecorder := httptest.NewRecorder()
	if err := (&BIQuery{Tokens: []string{token}}).ServeHTTP(queryRecorder, queryRequest, nil); err != nil {
		t.Fatal(err)
	}
	if queryRecorder.Code != http.StatusOK {
		t.Fatalf("query status = %d, body = %s", queryRecorder.Code, queryRecorder.Body.String())
	}
	var result bi.QueryResult
	if err := json.Unmarshal(queryRecorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Rows) != 2 || result.Rows[1]["title"] != "two" {
		t.Fatalf("unexpected query result: %#v", result)
	}
	if result.Generations["zt_bug"] != 1 || result.CompatibilityVersion != bi.SQLCompatibilityVersion {
		t.Fatalf("unexpected versions: %#v", result)
	}
}
