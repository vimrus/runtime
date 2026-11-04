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

type schedulerBridge interface {
	Schedule(context.Context, bridge.ScheduleRequest) (bridge.ScheduleResponse, error)
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
	background sync.WaitGroup
}

func New(config Config, queueBridge Bridge) (*Engine, error) {
	if queueBridge == nil {
		return nil, errors.New("queue bridge is required")
	}
	if config.InstanceID == "" || config.WorkerID == "" {
		return nil, errors.New("instanceID and workerID are required")
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
	// The loopback listener and the PHP application may still be warming up
	// right after Caddy starts, so negotiate capabilities with a short retry.
	var capabilities bridge.CapabilitiesResponse
	var capabilitiesErr error
	const capabilitiesAttempts = 10
	for attempt := 1; attempt <= capabilitiesAttempts; attempt++ {
		capabilities, capabilitiesErr = e.bridge.Capabilities(ctx, bridge.CapabilitiesRequest{
			Schema:     bridge.SchemaVersion,
			NodeID:     e.config.NodeID,
			InstanceID: e.config.InstanceID,
		})
		if capabilitiesErr == nil && capabilities.Error == nil {
			break
		}
		if attempt == capabilitiesAttempts {
			e.lastBridge = health.StatusFailed
			if capabilitiesErr == nil {
				e.lastError = capabilities.Error.Message
				return fmt.Errorf("queue bridge rejected capabilities: %s", capabilities.Error.Code)
			}
			e.lastError = capabilitiesErr.Error()
			return fmt.Errorf("queue bridge capabilities: %w", capabilitiesErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	e.lastBridge = health.StatusOK
	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.leases.ResetFatal()
	e.pool.Run(runCtx)
	// Mark the engine started before any background loop is launched. A
	// scheduler/reaper response can report an execution-mode conflict
	// immediately; the monitor must then be able to stop this run.
	e.started = true
	go e.monitorFatal(runCtx)
	e.background.Add(2)
	go func() {
		defer e.background.Done()
		e.leases.Run(runCtx)
	}()
	go func() {
		defer e.background.Done()
		e.reapLoop(runCtx)
	}()
	if _, ok := e.bridge.(schedulerBridge); ok {
		e.background.Add(1)
		go func() {
			defer e.background.Done()
			e.scheduleLoop(runCtx)
		}()
	}
	e.logger.Info("queue engine started", slog.String("driver", capabilities.Driver), slog.String("claimMode", capabilities.ClaimMode))
	return nil
}

// monitorFatal shuts down all queue activity when PHP reports that another
// execution mode owns the global cron lease.  This is terminal for this
// engine instance; it must not silently fall back to the Web fallback path.
func (e *Engine) monitorFatal(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-e.pool.Fatal():
		_ = e.Stop()
	case <-e.leases.Fatal():
		_ = e.Stop()
	}
}

func (e *Engine) scheduleLoop(ctx context.Context) {
	// Scheduling only creates durable queue rows; workers remain responsible
	// for claiming and executing them.
	e.scheduleOnce(ctx)
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.scheduleOnce(ctx)
		}
	}
}

func (e *Engine) scheduleOnce(ctx context.Context) {
	response, err := e.bridge.(schedulerBridge).Schedule(ctx, bridge.ScheduleRequest{Schema: bridge.SchemaVersion, NodeID: e.config.NodeID, InstanceID: e.config.InstanceID})
	if response.Error != nil {
		e.logger.Warn("queue scheduler call failed", slog.String("code", response.Error.Code), slog.String("message", response.Error.Message))
		if response.Error.Code == bridge.ErrorExecutionModeConflict {
			e.pool.SignalFatal(response.Error.Code)
		}
		return
	}
	if err != nil {
		e.logger.Warn("queue scheduler call failed", slog.Any("error", err), slog.Any("response", response.Error))
	}
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
	stopErr := e.pool.Stop()
	// Stop waits for all bridge loops from this run.  Without this barrier a
	// scheduler or heartbeat request can report a late fatal response while a
	// new run is resetting its lifecycle channels.
	e.background.Wait()
	e.mu.Lock()
	e.started = false
	e.mu.Unlock()
	return stopErr
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
			if response.Error != nil {
				e.mu.Lock()
				e.lastBridge = health.StatusDegraded
				e.lastError = response.Error.Error()
				e.mu.Unlock()
				if response.Error.Code == bridge.ErrorExecutionModeConflict {
					e.pool.SignalFatal(response.Error.Code)
					return
				}
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
