//go:build duckdb

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vimrus/runtime/internal/bi"
)

func TestBIPHPToParquetToQuery(t *testing.T) {
	phpBinary, err := exec.LookPath("php")
	if err != nil {
		t.Skip("PHP CLI is not available")
	}
	script := resolveBIPHPBridgeScript(t)

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

	token := "bi-php-integration-token"
	SetBIHandlersFor(token, publisher.Publish, func(ctx context.Context, request bi.QueryRequest) (bi.QueryResult, error) {
		return engine.Query(ctx, datasetRoot, request)
	})
	t.Cleanup(func() { SetBIHandlersFor(token, nil, nil) })

	mux := http.NewServeMux()
	publishHandler := &BIPublish{Tokens: []string{token}}
	queryHandler := &BIQuery{Tokens: []string{token}}
	mux.Handle("/internal/runtime/bi/v1/publish", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = publishHandler.ServeHTTP(writer, request, nil)
	}))
	mux.Handle("/internal/runtime/bi/v1/query", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = queryHandler.ServeHTTP(writer, request, nil)
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	command := exec.Command(phpBinary, script)
	command.Env = append(os.Environ(),
		"ZENTAO_BI_PUBLISH_URL="+server.URL+"/internal/runtime/bi/v1/publish",
		"ZENTAO_BI_QUERY_URL="+server.URL+"/internal/runtime/bi/v1/query",
		"ZENTAO_BI_TOKEN="+token,
		"ZENTAO_BI_STAGING_DIR="+stagingRoot,
		"ZENTAO_BI_SCHEMA="+"1",
		"ZENTAO_BI_MAX_ROWS=100",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("PHP BI bridge failed: %v\n%s", err, output)
	}

	var response struct {
		Published bi.PublishResult `json:"published"`
		Queried   bi.QueryResult   `json:"queried"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode PHP BI bridge output: %v\n%s", err, output)
	}
	if response.Published.Manifest.Generation != 1 || response.Published.Rows != 2 {
		t.Fatalf("unexpected publish result: %#v", response.Published)
	}
	if response.Queried.Total != 2 || len(response.Queried.Rows) != 2 {
		t.Fatalf("unexpected query result: %#v", response.Queried)
	}
	if response.Queried.Rows[1]["title"] != "two" {
		t.Fatalf("unexpected second row: %#v", response.Queried.Rows[1])
	}
	if response.Queried.Generations["zt_bug"] != response.Published.Manifest.Generation {
		t.Fatalf("generation mismatch: published=%d queried=%d", response.Published.Manifest.Generation, response.Queried.Generations["zt_bug"])
	}
	if response.Queried.CompatibilityVersion != bi.SQLCompatibilityVersion {
		t.Fatalf("compatibility version = %d, want %d", response.Queried.CompatibilityVersion, bi.SQLCompatibilityVersion)
	}
}

func resolveBIPHPBridgeScript(t *testing.T) string {
	t.Helper()
	candidates := make([]string, 0, 2)
	if configured := strings.TrimSpace(os.Getenv("ZENTAO_BI_PHP_SCRIPT")); configured != "" {
		if !filepath.IsAbs(configured) {
			// go test runs with the package directory as its working directory.
			configured = filepath.Join("..", "..", configured)
		}
		candidates = append(candidates, configured)
	}
	candidates = append(candidates, filepath.Join("..", "..", "..", "zentaopms", "test", "bi", "runtime-bridge.php"))

	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if info, err := os.Stat(absolute); err == nil && !info.IsDir() {
			return absolute
		}
	}
	t.Skip("ZenTao PHP BI bridge script is not available; set ZENTAO_BI_PHP_SCRIPT")
	return ""
}
