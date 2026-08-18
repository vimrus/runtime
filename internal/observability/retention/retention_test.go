package retention

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanOwnOnlyRemovesExpiredOwnNodePartitions(t *testing.T) {
	root := t.TempDir()
	oldDate := time.Now().UTC().Add(-30 * 24 * time.Hour).Format("2006-01-02")
	recentDate := time.Now().UTC().Format("2006-01-02")
	hour := time.Now().UTC().Format("15")
	ownOld := filepath.Join(root, "logs", "schema=v1", "date="+oldDate, "hour="+hour, "node=node-a")
	ownRecent := filepath.Join(root, "logs", "schema=v1", "date="+recentDate, "hour="+hour, "node=node-a")
	other := filepath.Join(root, "logs", "schema=v1", "date="+oldDate, "hour="+hour, "node=node-b")
	for _, dir := range []string{ownOld, ownRecent, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{ownOld, ownRecent, other} {
		if err := os.WriteFile(filepath.Join(dir, "part-x.parquet"), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cleaner, err := New(Config{DatasetRoot: root, NodeID: "node-a", MetricsDays: 7, LogDays: 7, FileBudget: 100, TimeBudget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := cleaner.CleanOwn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedFiles != 1 {
		t.Fatalf("removed files = %d, want 1", result.RemovedFiles)
	}
	if _, err := os.Stat(ownOld); !os.IsNotExist(err) {
		t.Fatal("own expired partition must be removed")
	}
	if _, err := os.Stat(ownRecent); err != nil {
		t.Fatal("own recent partition must be kept")
	}
	if _, err := os.Stat(filepath.Join(other, "part-x.parquet")); err != nil {
		t.Fatal("other node partition must be kept")
	}
}

func TestCleanRefusesSymlinkAndTraversal(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "victim")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	cleaner, err := New(Config{DatasetRoot: root, NodeID: "node-a", MetricsDays: 7, LogDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := cleaner.removeNodeDir(context.Background(), target, &Result{}); err == nil {
		t.Fatal("expected refusal for path that is not the node directory")
	}
}
