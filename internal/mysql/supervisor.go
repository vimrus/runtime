// Package mysql supervises the bundled MySQL 8.4 process for Full packages.
// It never connects to the database, never runs SQL, and never stores
// business database credentials.
package mysql

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

type Config struct {
	Binary         string
	DataDir        string
	SocketPath     string
	Port           int
	LogPath        string
	SecretsFile    string
	StartupTimeout time.Duration
	StopTimeout    time.Duration
	InitializeArgs []string
	RuntimeArgs    []string
}

type Status struct {
	State     State  `json:"state"`
	PID       int    `json:"pid,omitempty"`
	DataDir   string `json:"dataDir"`
	LastError string `json:"lastError,omitempty"`
}

type Supervisor struct {
	config          Config
	mu              sync.Mutex
	state           State
	cmd             *exec.Cmd
	initFile        string
	processDone     chan struct{}
	processErr      error
	log             *os.File
	lastError       string
	platformCleanup func()
}

func New(config Config) (*Supervisor, error) {
	if config.Binary == "" || config.DataDir == "" || config.SocketPath == "" || config.Port <= 0 {
		return nil, errors.New("mysql binary, dataDir, socketPath and port are required")
	}
	if config.StartupTimeout <= 0 {
		config.StartupTimeout = 60 * time.Second
	}
	if config.StopTimeout <= 0 {
		config.StopTimeout = 30 * time.Second
	}
	if config.SecretsFile == "" {
		config.SecretsFile = filepath.Join(config.DataDir, "..", "secrets", "mysql-root")
	}
	if err := os.MkdirAll(filepath.Dir(config.SecretsFile), 0o700); err != nil {
		return nil, err
	}
	return &Supervisor{config: config, state: StateStopped}, nil
}

// Initialize prepares a fresh data directory and generates a random root
// password file. It refuses to overwrite existing initialized data.
func (s *Supervisor) Initialize(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	marker := filepath.Join(s.config.DataDir, ".zentao-mysql-initialized")
	if _, err := os.Stat(marker); err == nil {
		return errors.New("mysql data directory is already initialized")
	}
	if err := os.MkdirAll(s.config.DataDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.config.LogPath), 0o700); err != nil {
		return err
	}
	if s.config.SecretsFile != "" {
		if _, err := os.Stat(s.config.SecretsFile); err == nil {
			return errors.New("mysql secrets file already exists")
		}
		password := randomPassword()
		if err := os.WriteFile(s.config.SecretsFile, []byte(password+"\n"), 0o600); err != nil {
			return err
		}
		s.initFile = filepath.Join(s.config.DataDir, ".zentao-init.sql")
		statement := fmt.Sprintf("ALTER USER 'root'@'localhost' IDENTIFIED BY '%s';\n", strings.ReplaceAll(password, "'", "''"))
		if err := os.WriteFile(s.initFile, []byte(statement), 0o600); err != nil {
			_ = os.Remove(s.config.SecretsFile)
			_ = os.Remove(s.initFile)
			s.initFile = ""
			return err
		}
	}
	args := append([]string{
		"--no-defaults",
		"--initialize-insecure",
		"--datadir=" + s.config.DataDir,
		"--log-error=" + s.config.LogPath,
	}, s.config.InitializeArgs...)
	command := exec.CommandContext(ctx, s.config.Binary, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		_ = os.Remove(marker)
		if s.config.SecretsFile != "" {
			_ = os.Remove(s.config.SecretsFile)
		}
		if s.initFile != "" {
			_ = os.Remove(s.initFile)
			s.initFile = ""
		}
		return fmt.Errorf("mysql initialize failed: %w: %s", err, truncate(string(output), 512))
	}
	if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		if s.config.SecretsFile != "" {
			_ = os.Remove(s.config.SecretsFile)
		}
		if s.initFile != "" {
			_ = os.Remove(s.initFile)
			s.initFile = ""
		}
		return err
	}
	return nil
}

// Start launches mysqld and waits for its socket to appear.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.state == StateRunning || s.state == StateStarting {
		s.mu.Unlock()
		return nil
	}
	if err := s.checkPortFree(); err != nil {
		s.state = StateFailed
		s.mu.Unlock()
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.config.LogPath), 0o700); err != nil {
		s.state = StateFailed
		s.mu.Unlock()
		return err
	}
	logFile, err := os.OpenFile(s.config.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		s.state = StateFailed
		s.mu.Unlock()
		return err
	}
	args := []string{
		"--no-defaults",
		"--datadir=" + s.config.DataDir,
		"--socket=" + s.config.SocketPath,
		"--port=" + fmt.Sprintf("%d", s.config.Port),
		"--bind-address=127.0.0.1",
		"--log-error=" + s.config.LogPath,
	}
	if s.initFile != "" {
		args = append(args, "--init-file="+s.initFile)
	}
	args = append(args, s.config.RuntimeArgs...)
	command := exec.Command(s.config.Binary, args...)
	command.Stdout = logFile
	command.Stderr = logFile
	setProcessGroup(command)
	if err := command.Start(); err != nil {
		logFile.Close()
		s.state = StateFailed
		s.mu.Unlock()
		return fmt.Errorf("start mysql: %w", err)
	}
	processDone := make(chan struct{})
	go func() {
		err := command.Wait()
		s.mu.Lock()
		s.processErr = err
		close(processDone)
		if s.cmd == command && (s.state == StateRunning || s.state == StateStarting) {
			wasRunning := s.state == StateRunning
			s.state = StateFailed
			if err != nil {
				s.lastError = err.Error()
			}
			if wasRunning {
				if s.log != nil {
					_ = s.log.Close()
					s.log = nil
				}
				if s.platformCleanup != nil {
					s.platformCleanup()
					s.platformCleanup = nil
				}
				s.cmd = nil
				s.processDone = nil
			}
		}
		s.mu.Unlock()
	}()
	cleanup, err := afterStart(command)
	if err != nil {
		_ = stopProcess(command)
		<-processDone
		logFile.Close()
		s.state = StateFailed
		s.mu.Unlock()
		return fmt.Errorf("assign mysql to job object: %w", err)
	}
	s.cmd = command
	s.processDone = processDone
	s.processErr = nil
	s.log = logFile
	s.platformCleanup = cleanup
	s.state = StateStarting
	s.mu.Unlock()

	deadline := time.Now().Add(s.config.StartupTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			_ = stopProcess(command)
			<-processDone
			logFile.Close()
			if cleanup != nil {
				cleanup()
			}
			s.mu.Lock()
			s.cmd = nil
			s.state = StateFailed
			s.mu.Unlock()
			return ctx.Err()
		default:
		}
		// Check process completion before trusting a socket path, which may
		// be stale from an earlier run or created just before a crash.
		select {
		case <-processDone:
			logFile.Close()
			if cleanup != nil {
				cleanup()
			}
			s.mu.Lock()
			s.cmd = nil
			s.processDone = nil
			s.state = StateFailed
			err := s.processErr
			s.mu.Unlock()
			return fmt.Errorf("mysql exited during startup: %w", err)
		default:
		}
		if _, err := os.Stat(s.config.SocketPath); err == nil {
			select {
			case <-processDone:
				continue
			default:
			}
			if s.initFile != "" {
				_ = os.Remove(s.initFile)
				s.initFile = ""
			}
			s.mu.Lock()
			s.state = StateRunning
			s.mu.Unlock()
			return nil
		}
		select {
		case <-processDone:
			logFile.Close()
			if cleanup != nil {
				cleanup()
			}
			s.mu.Lock()
			s.cmd = nil
			s.state = StateFailed
			s.mu.Unlock()
			s.mu.Lock()
			err := s.processErr
			s.mu.Unlock()
			return fmt.Errorf("mysql exited during startup: %w", err)
		default:
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.mu.Lock()
	s.state = StateFailed
	s.mu.Unlock()
	_ = stopProcess(command)
	<-processDone
	logFile.Close()
	if cleanup != nil {
		cleanup()
	}
	s.mu.Lock()
	s.cmd = nil
	s.mu.Unlock()
	return fmt.Errorf("mysql startup exceeded %s", s.config.StartupTimeout)
}

// Stop asks mysqld to shut down gracefully and waits within the configured
// timeout before killing the process group.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	command := s.cmd
	done := s.processDone
	if s.state != StateRunning && s.state != StateStarting {
		s.state = StateStopped
		s.mu.Unlock()
		return nil
	}
	s.state = StateStopping
	s.mu.Unlock()
	if command == nil || command.Process == nil {
		s.setState(StateStopped, "")
		return nil
	}
	if err := stopProcess(command); err != nil {
		return err
	}
	if done == nil {
		return errors.New("mysql process wait handle is unavailable")
	}
	select {
	case <-done:
	case <-time.After(s.config.StopTimeout):
		killProcessGroup(command)
		<-done
		s.mu.Lock()
		s.state = StateFailed
		if s.log != nil {
			_ = s.log.Close()
			s.log = nil
		}
		if s.platformCleanup != nil {
			s.platformCleanup()
			s.platformCleanup = nil
		}
		s.cmd = nil
		s.processDone = nil
		s.mu.Unlock()
		return errors.New("mysql stop timed out and was killed")
	case <-ctx.Done():
		killProcessGroup(command)
		<-done
		s.mu.Lock()
		s.state = StateFailed
		if s.log != nil {
			_ = s.log.Close()
			s.log = nil
		}
		if s.platformCleanup != nil {
			s.platformCleanup()
			s.platformCleanup = nil
		}
		s.cmd = nil
		s.processDone = nil
		s.mu.Unlock()
		return ctx.Err()
	}
	s.mu.Lock()
	s.state = StateStopped
	if s.log != nil {
		_ = s.log.Close()
		s.log = nil
	}
	if s.platformCleanup != nil {
		s.platformCleanup()
		s.platformCleanup = nil
	}
	s.cmd = nil
	s.processDone = nil
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := Status{State: s.state, DataDir: s.config.DataDir, LastError: s.lastError}
	if s.cmd != nil && s.cmd.Process != nil {
		status.PID = s.cmd.Process.Pid
	}
	return status
}

func (s *Supervisor) checkPortFree() error {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.config.Port))
	if err != nil {
		return fmt.Errorf("mysql port %d is already in use", s.config.Port)
	}
	return listener.Close()
}

func (s *Supervisor) setState(state State, lastError string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
	if lastError != "" {
		s.lastError = lastError
	}
}

func randomPassword() string {
	data := make([]byte, 24)
	_, _ = rand.Read(data)
	return hex.EncodeToString(data)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
