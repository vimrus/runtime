package upgrade

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
)

// Controller exposes the upgrade transaction through the local control
// plane. It keeps a single in-flight transaction per Runtime process.
type Controller struct {
	mu          sync.Mutex
	layout      Layout
	transaction *Transaction
}

func NewController(installRoot string) (*Controller, error) {
	installRoot = filepath.Clean(installRoot)
	if !filepath.IsAbs(installRoot) {
		return nil, errors.New("install root must be an absolute path")
	}
	if installRoot == string(filepath.Separator) {
		return nil, errors.New("install root must not be the filesystem root")
	}
	layout := Layout{
		RuntimeDir: filepath.Join(installRoot, "runtime"),
		AppsDir:    filepath.Join(installRoot, "app", "releases"),
		AppCurrent: filepath.Join(installRoot, "app", "current"),
		ConfigDir:  filepath.Join(installRoot, "config"),
		DataDir:    filepath.Join(installRoot, "data"),
		BackupsDir: filepath.Join(installRoot, "backups"),
		LogsDir:    filepath.Join(installRoot, "logs"),
	}
	if err := layout.Validate(); err != nil {
		return nil, err
	}
	return &Controller{layout: layout}, nil
}

func (c *Controller) Layout() Layout { return c.layout }

type preparePayload struct {
	Release string `json:"release"`
}

// Handle processes one upgrade control action.
func (c *Controller) Handle(action string, payload json.RawMessage) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch action {
	case "prepare":
		var request preparePayload
		if err := decodePayload(payload, &request); err != nil {
			return nil, err
		}
		if request.Release == "" {
			return nil, errors.New("upgrade prepare requires release path")
		}
		transaction, err := Begin(c.layout)
		if err != nil {
			return nil, err
		}
		if err := transaction.Stage(request.Release); err != nil {
			return nil, err
		}
		c.transaction = transaction
		return map[string]any{"state": transaction.State(), "backupDir": transaction.BackupDir()}, nil
	case "apply":
		if c.transaction == nil {
			return nil, errors.New("no upgrade transaction prepared")
		}
		if err := c.transaction.Apply(); err != nil {
			return nil, err
		}
		if err := c.transaction.Verify(); err != nil {
			return nil, err
		}
		if err := c.transaction.Commit(); err != nil {
			return nil, err
		}
		return map[string]any{"state": c.transaction.State(), "release": c.transaction.release}, nil
	case "rollback":
		if c.transaction == nil {
			return nil, errors.New("no upgrade transaction prepared")
		}
		if err := c.transaction.Rollback(); err != nil {
			return nil, err
		}
		return map[string]any{"state": c.transaction.State()}, nil
	case "status":
		state := StateIdle
		if c.transaction != nil {
			state = c.transaction.State()
		}
		current := ""
		if pointer, err := readPointer(c.layout.AppCurrent); err == nil {
			current = pointer
		}
		return map[string]any{
			"layout":  c.layout,
			"state":   state,
			"current": current,
		}, nil
	default:
		return nil, fmt.Errorf("unknown upgrade action %q", action)
	}
}

func decodePayload(payload json.RawMessage, target any) error {
	if len(payload) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode upgrade payload: %w", err)
	}
	return nil
}
