package mysql

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRealMySQLInitializeStartStop validates the supervisor against the real
// locked MySQL 8.4 binary. Set ZENTAO_MYSQL_BINARY to the mysqld executable
// to enable it.
func TestRealMySQLInitializeStartStop(t *testing.T) {
	binary := os.Getenv("ZENTAO_MYSQL_BINARY")
	if binary == "" {
		t.Skip("ZENTAO_MYSQL_BINARY is not set")
	}
	root := t.TempDir()
	supervisor, err := New(Config{
		Binary:         binary,
		DataDir:        filepath.Join(root, "data"),
		SocketPath:     filepath.Join(root, "mysql.sock"),
		Port:           13307,
		LogPath:        filepath.Join(root, "logs", "mysql.log"),
		SecretsFile:    filepath.Join(root, "secrets", "root"),
		StartupTimeout: 90 * time.Second,
		StopTimeout:    30 * time.Second,
		RuntimeArgs:    []string{"--user=root"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := supervisor.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := supervisor.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if supervisor.Status().State != StateRunning {
		t.Fatalf("state = %s", supervisor.Status().State)
	}
	if err := supervisor.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
