package mysql

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func writeFakeMysqld(t *testing.T, path string) {
	t.Helper()
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    --initialize-insecure) exit 0 ;;
    --socket=*) socket="${arg#--socket=}" ;;
  esac
done
sleep 0.2
touch "$socket"
cleanup() { rm -f "$socket"; exit 0; }
trap cleanup TERM INT
while true; do sleep 1; done
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFailingInitializer(t *testing.T, path string) {
	t.Helper()
	script := "#!/bin/sh\nfor arg in \"$@\"; do [ \"$arg\" = \"--initialize-insecure\" ] && exit 1; done\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeExitingMysqld(t *testing.T, path string) {
	t.Helper()
	script := "#!/bin/sh\nfor arg in \"$@\"; do\n  case \"$arg\" in --socket=*) socket=\"${arg#--socket=}\" ;; esac\ndone\ntouch \"$socket\"\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeCreatesSecretsAndRefusesReinit(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "mysqld")
	writeFakeMysqld(t, binary)
	supervisor, err := New(Config{
		Binary: binary, DataDir: filepath.Join(root, "data"), SocketPath: filepath.Join(root, "mysql.sock"),
		Port: 3306, LogPath: filepath.Join(root, "logs", "mysql.log"), SecretsFile: filepath.Join(root, "secrets", "mysql-root"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	secret, err := os.ReadFile(supervisor.config.SecretsFile)
	if err != nil || len(strings.TrimSpace(string(secret))) < 32 {
		t.Fatalf("secrets file missing or weak: %q %v", secret, err)
	}
	if err := supervisor.Initialize(context.Background()); err == nil {
		t.Fatal("second initialize must be refused")
	}
}

func TestInitializeFailureCleansGeneratedCredentials(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "mysqld")
	writeFailingInitializer(t, binary)
	supervisor, err := New(Config{Binary: binary, DataDir: filepath.Join(root, "data"), SocketPath: filepath.Join(root, "mysql.sock"), Port: 3306, LogPath: filepath.Join(root, "logs", "mysql.log"), SecretsFile: filepath.Join(root, "secrets", "mysql-root")})
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Initialize(context.Background()); err == nil {
		t.Fatal("initialize must fail")
	}
	if _, err := os.Stat(supervisor.config.SecretsFile); !os.IsNotExist(err) {
		t.Fatalf("secret remains after failed initialization: %v", err)
	}
	if _, err := os.Stat(filepath.Join(supervisor.config.DataDir, ".zentao-init.sql")); !os.IsNotExist(err) {
		t.Fatalf("init file remains after failed initialization: %v", err)
	}
}

func TestStartStopLifecycle(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "mysqld")
	writeFakeMysqld(t, binary)
	socket := filepath.Join(root, "mysql.sock")
	supervisor, err := New(Config{
		Binary: binary, DataDir: filepath.Join(root, "data"), SocketPath: socket,
		Port: freeTCPPort(t), LogPath: filepath.Join(root, "logs", "mysql.log"), SecretsFile: filepath.Join(root, "secrets", "root"),
		StartupTimeout: 5 * time.Second, StopTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := supervisor.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if supervisor.Status().State != StateRunning {
		t.Fatalf("state = %s, want running", supervisor.Status().State)
	}
	if err := supervisor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if supervisor.Status().State != StateStopped {
		t.Fatalf("state = %s, want stopped", supervisor.Status().State)
	}
}

func TestStartRejectsProcessThatExitsAfterSocketCreation(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "mysqld")
	writeExitingMysqld(t, binary)
	supervisor, err := New(Config{
		Binary: binary, DataDir: filepath.Join(root, "data"), SocketPath: filepath.Join(root, "mysql.sock"),
		Port: 13307, LogPath: filepath.Join(root, "logs", "mysql.log"), StartupTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background()); err == nil {
		t.Fatal("start must fail when mysqld exits during startup")
	}
	if state := supervisor.Status().State; state != StateFailed {
		t.Fatalf("state = %s, want failed", state)
	}
}

func TestStartRejectsPortConflict(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "mysqld")
	writeFakeMysqld(t, binary)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	supervisor, err := New(Config{
		Binary: binary, DataDir: filepath.Join(root, "data"), SocketPath: filepath.Join(root, "mysql.sock"),
		Port: port, LogPath: filepath.Join(root, "logs", "mysql.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("expected port conflict error, got %v", err)
	}
}
