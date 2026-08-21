// Package worker implements bounded, per-queue worker pools with adaptive
// claiming, timeouts, cancellation and bounded drain.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/vimrus/runtime/internal/queue/bridge"
	"github.com/vimrus/runtime/internal/queue/lease"
)

// Bridge is the subset of the Queue Bridge used by the worker pool.
type Bridge interface {
	Claim(context.Context, bridge.ClaimRequest) (bridge.ClaimResponse, error)
	Execute(context.Context, bridge.ExecuteRequest) (bridge.ExecuteResponse, error)
}

type QueueConfig struct {
	Name        string
	Concurrency int
	MinPoll     time.Duration
	MaxPoll     time.Duration
}

type Config struct {
	NodeID       string
	InstanceID   string
	WorkerID     string
	Channels     []QueueConfig
	ClaimBatch   int
	LeaseSeconds int
	DrainTimeout time.Duration
}

type Pool struct {
	bridge Bridge
	leases *lease.Manager
	config Config
	logger *slog.Logger

	mu       sync.Mutex
	sems     map[string]chan struct{}
	wake     chan struct{}
	running  bool
	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
	wg       sync.WaitGroup
}

func NewPool(queueBridge Bridge, leases *lease.Manager, config Config, logger *slog.Logger) (*Pool, error) {
	if queueBridge == nil || leases == nil {
		return nil, fmt.Errorf("bridge and lease manager are required")
	}
	if config.NodeID == "" || config.InstanceID == "" || config.WorkerID == "" {
		return nil, fmt.Errorf("nodeID, instanceID and workerID are required")
	}
	if config.ClaimBatch <= 0 || config.ClaimBatch > bridge.MaxBatchSize {
		config.ClaimBatch = 8
	}
	if config.LeaseSeconds <= 0 {
		config.LeaseSeconds = 60
	}
	if config.DrainTimeout <= 0 {
		config.DrainTimeout = 30 * time.Second
	}
	sems := make(map[string]chan struct{}, len(config.Channels))
	for _, queue := range config.Channels {
		if queue.Concurrency <= 0 {
			queue.Concurrency = 1
		}
		if queue.MinPoll <= 0 {
			queue.MinPoll = 50 * time.Millisecond
		}
		if queue.MaxPoll <= 0 {
			queue.MaxPoll = 5 * time.Second
		}
		if queue.MaxPoll < queue.MinPoll {
			queue.MaxPoll = queue.MinPoll
		}
		sems[queue.Name] = make(chan struct{}, queue.Concurrency)
	}
	return &Pool{
		bridge: queueBridge,
		leases: leases,
		config: config,
		logger: logger,
		sems:   sems,
		wake:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}, nil
}

// Wake requests an immediate claim cycle for all queues. It is best-effort:
// a lost wakeup only delays consumption until the next poll interval.
func (p *Pool) Wake() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run starts one adaptive claim loop per queue and returns after all loops
// and in-flight jobs have drained.
func (p *Pool) Run(ctx context.Context) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()

	for _, queue := range p.config.Channels {
		p.wg.Add(1)
		go func(queue QueueConfig) {
			defer p.wg.Done()
			p.claimLoop(ctx, queue)
		}(queue)
	}
	go func() {
		p.wg.Wait()
		close(p.done)
	}()
}

// Stop stops claiming new work and waits for in-flight jobs within the
// configured drain timeout; remaining jobs are cancelled and left for lease
// recovery.
func (p *Pool) Stop() error {
	p.stopOnce.Do(func() { close(p.stop) })
	select {
	case <-p.done:
		return nil
	case <-time.After(p.config.DrainTimeout):
		return fmt.Errorf("worker drain exceeded %s", p.config.DrainTimeout)
	}
}

func (p *Pool) claimLoop(ctx context.Context, queue QueueConfig) {
	interval := queue.MinPoll
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stop:
			return
		case <-p.wake:
			interval = queue.MinPoll
			continue
		default:
		}
		request := bridge.ClaimRequest{
			Schema:       bridge.SchemaVersion,
			NodeID:       p.config.NodeID,
			InstanceID:   p.config.InstanceID,
			WorkerID:     p.config.WorkerID,
			Channels:     []string{queue.Name},
			Limit:        p.config.ClaimBatch,
			LeaseSeconds: p.config.LeaseSeconds,
		}
		response, err := p.bridge.Claim(ctx, request)
		if err != nil {
			p.logger.Warn("queue claim failed", slog.String("channel", queue.Name), slog.Any("error", err))
			interval = backoff(interval, queue.MaxPoll)
			if !sleepCtx(ctx, interval) {
				return
			}
			continue
		}
		if response.Error != nil {
			p.logger.Warn("queue claim rejected", slog.String("channel", queue.Name), slog.String("code", response.Error.Code))
			interval = backoff(interval, queue.MaxPoll)
			if !sleepCtx(ctx, interval) {
				return
			}
			continue
		}
		if len(response.Leases) == 0 {
			interval = backoff(interval, queue.MaxPoll)
			if !sleepCtx(ctx, interval) {
				return
			}
			continue
		}
		interval = queue.MinPoll
		for _, claimed := range response.Leases {
			sem := p.sem(queue.Name)
			if sem == nil {
				p.logger.Warn("lease references unknown queue", slog.String("channel", claimed.Channel), slog.String("job", claimed.UUID))
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-p.stop:
				return
			case sem <- struct{}{}:
				p.wg.Add(1)
				go func() {
					defer p.wg.Done()
					defer func() { <-sem }()
					p.execute(ctx, claimed)
				}()
			}
		}
	}
}

func (p *Pool) execute(parent context.Context, claimed bridge.Lease) {
	execCtx, cancel := context.WithCancel(parent)
	timeout := time.Duration(claimed.TimeoutSeconds) * time.Second
	if timeout > 0 {
		execCtx, cancel = context.WithTimeout(execCtx, timeout)
	}
	p.leases.Add(claimed, cancel)
	defer func() {
		cancel()
		p.leases.Remove(claimed.UUID)
	}()

	request := bridge.ExecuteRequest{
		Schema:     bridge.SchemaVersion,
		UUID:       claimed.UUID,
		Attempt:    claimed.Attempt,
		LeaseToken: claimed.LeaseToken,
		TraceID:    claimed.TraceID,
	}
	response, err := p.bridge.Execute(execCtx, request)
	if err != nil {
		// Result is unknown: do not ACK, retry or fail. Lease recovery will
		// reschedule the job after expiry.
		p.logger.Warn("queue execute transport failed", slog.String("job", claimed.UUID), slog.String("channel", claimed.Channel), slog.Any("error", err))
		return
	}
	if response.Error != nil {
		p.logger.Warn("queue execute rejected", slog.String("job", claimed.UUID), slog.String("code", response.Error.Code))
		return
	}
	p.logger.Info("queue job finished", slog.String("job", claimed.UUID), slog.String("channel", claimed.Channel), slog.String("result", string(response.Result)))
}

func (p *Pool) sem(queue string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sems[queue]
}

func backoff(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		next = max
	}
	jitter := time.Duration(rand.Int64N(int64(next/10) + 1))
	if next > jitter {
		return next - jitter
	}
	return next
}

func sleepCtx(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
