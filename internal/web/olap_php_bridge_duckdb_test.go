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

	"github.com/vimrus/runtime/internal/olap"
)

func TestOLAPPHPToParquetToQuery(t *testing.T) {
	phpBinary, err := exec.LookPath("php")
	if err != nil {
		t.Skip("PHP CLI is not available")
	}
	script := resolveOLAPPHPBridgeScript(t)

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

	token := "olap-php-integration-token"
	SetOLAPHandlersFor(token, publisher.Publish, func(ctx context.Context, request olap.QueryRequest) (olap.QueryResult, error) {
		return engine.Query(ctx, datasetRoot, request)
	})
	t.Cleanup(func() { SetOLAPHandlersFor(token, nil, nil) })

	mux := http.NewServeMux()
	publishHandler := &OLAPPublish{Tokens: []string{token}}
	queryHandler := &OLAPQuery{Tokens: []string{token}}
	mux.Handle("/internal/runtime/olap/v1/publish", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = publishHandler.ServeHTTP(writer, request, nil)
	}))
	mux.Handle("/internal/runtime/olap/v1/query", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = queryHandler.ServeHTTP(writer, request, nil)
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	command := exec.Command(phpBinary, script)
	command.Env = append(os.Environ(),
		"ZENTAO_OLAP_PUBLISH_URL="+server.URL+"/internal/runtime/olap/v1/publish",
		"ZENTAO_OLAP_QUERY_URL="+server.URL+"/internal/runtime/olap/v1/query",
		"ZENTAO_OLAP_TOKEN="+token,
		"ZENTAO_OLAP_STAGING_DIR="+stagingRoot,
		"ZENTAO_OLAP_SCHEMA="+"1",
		"ZENTAO_OLAP_MAX_ROWS=100",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("PHP OLAP bridge failed: %v\n%s", err, output)
	}

	var response struct {
		Published olap.PublishResult `json:"published"`
		Queried   olap.QueryResult   `json:"queried"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode PHP OLAP bridge output: %v\n%s", err, output)
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
	if response.Queried.CompatibilityVersion != olap.SQLCompatibilityVersion {
		t.Fatalf("compatibility version = %d, want %d", response.Queried.CompatibilityVersion, olap.SQLCompatibilityVersion)
	}
}

func resolveOLAPPHPBridgeScript(t *testing.T) string {
	t.Helper()
	candidates := make([]string, 0, 2)
	if configured := strings.TrimSpace(os.Getenv("ZENTAO_OLAP_PHP_SCRIPT")); configured != "" {
		if !filepath.IsAbs(configured) {
			// go test runs with the package directory as its working directory.
			configured = filepath.Join("..", "..", configured)
		}
		candidates = append(candidates, configured)
	}
	candidates = append(candidates, filepath.Join("..", "..", "..", "zentaopms", "test", "olap", "runtime-bridge.php"))

	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if info, err := os.Stat(absolute); err == nil && !info.IsDir() {
			return absolute
		}
	}
	t.Skip("ZenTao PHP OLAP bridge script is not available; set ZENTAO_OLAP_PHP_SCRIPT")
	return ""
}
