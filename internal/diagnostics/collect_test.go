package diagnostics

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectBundlesLogsAndSummary(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "logs", "runtime.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("runtime log line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := &Collector{
		LogPaths:  []string{logPath, filepath.Join(root, "logs", "missing.log")},
		OutputDir: filepath.Join(root, "out"),
		Summary:   func() map[string]any { return map[string]any{"state": "ready"} },
	}
	output, err := collector.Collect()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	names := make(map[string]bool)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[header.Name] = true
		if strings.HasSuffix(header.Name, "runtime.log") {
			data, err := io.ReadAll(tarReader)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "runtime log line\n" {
				t.Fatalf("unexpected log content: %q", data)
			}
		}
	}
	if !names["summary.json"] || !strings.HasSuffix(namesKey(names, "runtime.log"), "runtime.log") {
		t.Fatalf("bundle is missing expected entries: %#v", names)
	}
}

func namesKey(names map[string]bool, suffix string) string {
	for name := range names {
		if strings.HasSuffix(name, suffix) {
			return name
		}
	}
	return ""
}
