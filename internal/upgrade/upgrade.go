// Package upgrade implements the Runtime/Application/Config/Data directory
// separation contract and an atomic upgrade transaction with rollback.
package upgrade

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Layout describes the four storage domains and auxiliary directories. Data
// and user configuration are never overwritten by an upgrade.
type Layout struct {
	RuntimeDir string // immutable Runtime installation
	AppsDir    string // versioned application releases
	AppCurrent string // pointer to the active application release
	ConfigDir  string // user configuration
	DataDir    string // attachments and persistent data
	BackupsDir string // upgrade backups
	LogsDir    string // log output
}

func (l Layout) Validate() error {
	paths := map[string]string{
		"runtimeDir": l.RuntimeDir,
		"appsDir":    l.AppsDir,
		"appCurrent": l.AppCurrent,
		"configDir":  l.ConfigDir,
		"dataDir":    l.DataDir,
		"backupsDir": l.BackupsDir,
		"logsDir":    l.LogsDir,
	}
	for name, path := range paths {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be an absolute path", name)
		}
	}
	if l.DataDir == l.ConfigDir || l.DataDir == l.RuntimeDir || l.ConfigDir == l.RuntimeDir {
		return errors.New("runtime, config and data directories must be separated")
	}
	for name, path := range paths {
		for otherName, other := range paths {
			if name == otherName {
				continue
			}
			if isWithin(path, other) {
				return fmt.Errorf("%s must not be nested inside %s", name, otherName)
			}
		}
	}
	return nil
}

func isWithin(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return relative != "." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != ".."
}

type State string

const (
	StateIdle      State = "idle"
	StatePrepared  State = "prepared"
	StateStaged    State = "staged"
	StateApplied   State = "applied"
	StateVerified  State = "verified"
	StateCommitted State = "committed"
)

// Transaction swaps the application pointer atomically and keeps enough
// state to roll back to the previous release.
type Transaction struct {
	layout    Layout
	state     State
	backupDir string
	previous  string
	release   string
	startedAt time.Time
	auditPath string
}

func Begin(layout Layout) (*Transaction, error) {
	if err := layout.Validate(); err != nil {
		return nil, err
	}
	for _, path := range []string{layout.AppsDir, layout.ConfigDir, layout.BackupsDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(layout.AppCurrent), 0o755); err != nil {
		return nil, fmt.Errorf("create application pointer directory: %w", err)
	}
	previous, err := readPointer(layout.AppCurrent)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		previous = ""
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	backupDir := filepath.Join(layout.BackupsDir, "upgrade-"+stamp)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(backupDir, "previous-pointer.txt"), []byte(previous+"\n")); err != nil {
		return nil, err
	}
	transaction := &Transaction{
		layout:    layout,
		state:     StatePrepared,
		backupDir: backupDir,
		previous:  previous,
		startedAt: time.Now().UTC(),
		auditPath: filepath.Join(backupDir, "transaction.json"),
	}
	if err := transaction.writeAudit(); err != nil {
		return nil, err
	}
	return transaction, nil
}

// Stage validates that the target release exists and records it as the next
// active release. It does not modify the active pointer.
func (t *Transaction) Stage(release string) error {
	if t.state != StatePrepared {
		return fmt.Errorf("cannot stage in state %s", t.state)
	}
	release = filepath.Clean(release)
	if !filepath.IsAbs(release) {
		return errors.New("release must be an absolute path")
	}
	if release == t.previous {
		return errors.New("release is already active")
	}
	if !isWithin(release, t.layout.AppsDir) {
		return errors.New("release must live inside the applications directory")
	}
	info, err := os.Stat(filepath.Join(release, "www"))
	if err != nil || !info.IsDir() {
		return fmt.Errorf("release %s does not contain a valid www directory: %w", release, err)
	}
	t.release = release
	t.state = StateStaged
	return t.writeAudit()
}

// Apply switches the active pointer to the staged release.
func (t *Transaction) Apply() error {
	if t.state != StateStaged {
		return fmt.Errorf("cannot apply in state %s", t.state)
	}
	if err := swapPointer(t.layout.AppCurrent, t.release); err != nil {
		return err
	}
	t.state = StateApplied
	return t.writeAudit()
}

// Verify checks that the active pointer resolves to the staged release and
// that the Runtime configuration was not overwritten.
func (t *Transaction) Verify() error {
	if t.state != StateApplied {
		return fmt.Errorf("cannot verify in state %s", t.state)
	}
	current, err := readPointer(t.layout.AppCurrent)
	if err != nil {
		return err
	}
	if current != t.release {
		return fmt.Errorf("active pointer is %q, want %q", current, t.release)
	}
	t.state = StateVerified
	return t.writeAudit()
}

// Commit marks the upgrade durable. The backup is retained until the next
// explicit cleanup so rollback remains possible after a delayed failure.
func (t *Transaction) Commit() error {
	if t.state != StateVerified {
		return fmt.Errorf("cannot commit in state %s", t.state)
	}
	t.state = StateCommitted
	return t.writeAudit()
}

// Rollback restores the previous pointer when the transaction has not been
// committed or when a post-commit verification fails.
func (t *Transaction) Rollback() error {
	if t.state == StateIdle {
		return fmt.Errorf("cannot roll back from state %s", t.state)
	}
	if t.previous == "" {
		if err := os.Remove(t.layout.AppCurrent); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove active pointer: %w", err)
		}
	} else {
		if err := swapPointer(t.layout.AppCurrent, t.previous); err != nil {
			return err
		}
	}
	t.state = StateIdle
	return t.writeAudit()
}

func (t *Transaction) State() State { return t.state }

func (t *Transaction) BackupDir() string { return t.backupDir }

func (t *Transaction) writeAudit() error {
	audit := struct {
		State     State     `json:"state"`
		StartedAt time.Time `json:"startedAt"`
		Previous  string    `json:"previous"`
		Release   string    `json:"release,omitempty"`
		BackupDir string    `json:"backupDir"`
	}{
		State: t.state, StartedAt: t.startedAt, Previous: t.previous, Release: t.release, BackupDir: t.backupDir,
	}
	data, err := json.MarshalIndent(audit, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(t.auditPath, data)
}

func readPointer(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read application pointer: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		return filepath.Clean(target), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("application pointer file is empty")
	}
	return filepath.Clean(value), nil
}

func swapPointer(path, target string) error {
	temp := path + ".tmp-" + fmt.Sprint(os.Getpid())
	if err := os.Symlink(target, temp); err != nil {
		return fmt.Errorf("create pointer: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("swap pointer: %w", err)
	}
	return nil
}

func writeFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}
