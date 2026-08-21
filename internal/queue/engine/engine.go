// Package engine composes the Queue Bridge client, worker pool, lease
// manager and reaper into the Runtime-managed queue engine.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vimrus/runtime/internal/health"
	"github.com/vimrus/runtime/internal/queue/bridge"
	"github.com/vimrus/runtime/internal/queue/lease"
	"github.com/vimrus/runtime/internal/queue/worker"
)

// Bridge is the full Queue Bridge surface used by the engine.
type Bridge interface {
	Capabilities(context.Context, bridge.CapabilitiesRequest) (bridge.CapabilitiesResponse, error)
	Claim(context.Context, bridge.ClaimRequest) (bridge.ClaimResponse, error)
	Execute(context.Context, bridge.ExecuteRequest) (bridge.ExecuteResponse, error)
	Heartbeat(context.Context, bridge.HeartbeatRequest) (bridge.HeartbeatResponse, error)
	Reap(context.Context, bridge.ReapRequest) (bridge.ReapResponse, error)
	Stats(context.Context, bridge.StatsRequest) (bridge.StatsResponse, error)
	Control(context.Context, bridge.ControlRequest) (bridge.ControlResponse, error)
}

type Config struct {
	NodeID            string
	InstanceID        string
	WorkerID          string
	ClaimBatch        int
	LeaseSeconds      int
	HeartbeatInterval time.Duration
	ReapInterval      time.Duration
	ReapBatch         int
	DrainTimeout      time.Duration
	Workers           []worker.QueueConfig
	Logger            *slog.Logger
}

type Engine struct {
	config     Config
	bridge     Bridge
	logger     *slog.Logger
	pool       *worker.Pool
	leases     *lease.Manager
	cancel     context.CancelFunc
	mu         sync.Mutex
	started    bool
	lastBridge health.Status
	lastError  string
}

func New(config Config, queueBridge Bridge) (*Engine, error) {
	if queueBridge == nil {
		return nil, errors.New("queue bridge is required")
	}
	if config.NodeID == "" || config.InstanceID == "" || config.WorkerID == "" {
		return nil, errors.New("nodeID, instanceID and workerID are required")
	}
	if len(config.Workers) == 0 {
		return nil, errors.New("at least one queue worker is required")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	manager := lease.NewManager(config.NodeID, config.InstanceID, config.WorkerID, queueBridge, config.HeartbeatInterval, config.ClaimBatch)
	pool, err := worker.NewPool(queueBridge, manager, worker.Config{
		NodeID:       config.NodeID,
		InstanceID:   config.InstanceID,
		WorkerID:     config.WorkerID,
		Channels:     config.Workers,
		ClaimBatch:   config.ClaimBatch,
		LeaseSeconds: config.LeaseSeconds,
		DrainTimeout: config.DrainTimeout,
	}, config.Logger)
	if err != nil {
		return nil, err
	}
	return &Engine{
		config:     config,
		bridge:     queueBridge,
		logger:     config.Logger,
		pool:       pool,
		leases:     manager,
		lastBridge: health.StatusUnknown,
	}, nil
}

// Start negotiates the Bridge schema and starts claiming work.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return nil
	}
	capabilities, err := e.bridge.Capabilities(ctx, bridge.CapabilitiesRequest{
		Schema:     bridge.SchemaVersion,
		NodeID:     e.config.NodeID,
		InstanceID: e.config.InstanceID,
	})
	if err != nil {
		e.lastBridge = health.StatusFailed
		e.lastError = err.Error()
		return fmt.Errorf("queue bridge capabilities: %w", err)
	}
	if capabilities.Error != nil {
		e.lastBridge = health.StatusFailed
		e.lastError = capabilities.Error.Message
		return fmt.Errorf("queue bridge rejected capabilities: %s", capabilities.Error.Code)
	}
	e.lastBridge = health.StatusOK
	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.pool.Run(runCtx)
	go e.leases.Run(runCtx)
	go e.reapLoop(runCtx)
	e.started = true
	e.logger.Info("queue engine started", slog.String("driver", capabilities.Driver), slog.String("claimMode", capabilities.ClaimMode))
	return nil
}

func (e *Engine) Stop() error {
	e.mu.Lock()
	cancel := e.cancel
	started := e.started
	e.mu.Unlock()
	if !started {
		return nil
	}
	if cancel != nil {
		cancel()
	}
	if err := e.pool.Stop(); err != nil {
		return err
	}
	e.mu.Lock()
	e.started = false
	e.mu.Unlock()
	return nil
}

func (e *Engine) Wake() { e.pool.Wake() }

func (e *Engine) Health() health.Result {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started {
		return health.Result{Name: "queue", Kind: health.KindDependency, Status: health.StatusUnknown, Message: "queue engine is not started"}
	}
	return health.Result{Name: "queue", Kind: health.KindDependency, Status: e.lastBridge, Message: e.lastError}
}

func (e *Engine) reapLoop(ctx context.Context) {
	ticker := time.NewTicker(e.config.ReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			response, err := e.bridge.Reap(ctx, bridge.ReapRequest{
				Schema:     bridge.SchemaVersion,
				NodeID:     e.config.NodeID,
				InstanceID: e.config.InstanceID,
				Limit:      e.config.ReapBatch,
			})
			if err != nil {
				e.mu.Lock()
				e.lastBridge = health.StatusDegraded
				e.lastError = err.Error()
				e.mu.Unlock()
				continue
			}
			e.mu.Lock()
			e.lastBridge = health.StatusOK
			e.lastError = ""
			e.mu.Unlock()
			_ = response
		}
	}
}
