// Package scheduler provides controlled, registered job orchestration. It
// never executes arbitrary system commands or unregistered callables.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type JobFunc func(ctx context.Context) error

type Job struct {
	Name     string
	Interval time.Duration
	Timeout  time.Duration
	Run      JobFunc
}

type JobStatus struct {
	Name         string    `json:"name"`
	Running      bool      `json:"running"`
	LastStarted  time.Time `json:"lastStarted,omitempty"`
	LastFinished time.Time `json:"lastFinished,omitempty"`
	LastError    string    `json:"lastError,omitempty"`
	Runs         uint64    `json:"runs"`
}

type Manager struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	status  map[string]*JobStatus
	slots   chan struct{}
	running bool
	logger  *slog.Logger
}

func New(maxConcurrency int, logger *slog.Logger) *Manager {
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		jobs:   make(map[string]*Job),
		status: make(map[string]*JobStatus),
		slots:  make(chan struct{}, maxConcurrency),
		logger: logger,
	}
}

func (m *Manager) Register(job Job) error {
	if job.Name == "" || strings.TrimSpace(job.Name) == "" {
		return errors.New("scheduler job name is required")
	}
	if job.Run == nil {
		return fmt.Errorf("scheduler job %q has no callable", job.Name)
	}
	if job.Interval <= 0 {
		return fmt.Errorf("scheduler job %q interval must be positive", job.Name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.jobs[job.Name]; exists {
		return fmt.Errorf("scheduler job %q is already registered", job.Name)
	}
	m.jobs[job.Name] = &job
	m.status[job.Name] = &JobStatus{Name: job.Name}
	return nil
}

// Run starts the periodic loop for every registered job until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, name := range m.jobNames() {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			m.loop(ctx, name)
		}(name)
	}
	wg.Wait()
}

func (m *Manager) loop(ctx context.Context, name string) {
	m.mu.Lock()
	job := m.jobs[name]
	m.mu.Unlock()
	ticker := time.NewTicker(job.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.execute(ctx, name)
		}
	}
}

// Trigger runs a registered job once, subject to the concurrency limit. It
// rejects unregistered names so arbitrary commands can never be scheduled.
func (m *Manager) Trigger(ctx context.Context, name string) error {
	m.mu.Lock()
	job, ok := m.jobs[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("scheduler job %q is not registered", name)
	}
	m.mu.Unlock()
	m.execute(ctx, job.Name)
	return nil
}

func (m *Manager) execute(ctx context.Context, name string) {
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	case <-ctx.Done():
		return
	}
	m.mu.Lock()
	job := m.jobs[name]
	status := m.status[name]
	status.Running = true
	status.LastStarted = time.Now().UTC()
	status.Runs++
	m.mu.Unlock()

	runCtx := ctx
	cancel := func() {}
	if job.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, job.Timeout)
	}
	err := job.Run(runCtx)
	cancel()

	m.mu.Lock()
	status.Running = false
	status.LastFinished = time.Now().UTC()
	if err != nil {
		status.LastError = err.Error()
	}
	m.mu.Unlock()
	if err != nil {
		m.logger.Warn("scheduler job failed", slog.String("job", name), slog.Any("error", err))
	}
}

func (m *Manager) Status() map[string]JobStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]JobStatus, len(m.status))
	for name, status := range m.status {
		result[name] = *status
	}
	return result
}

func (m *Manager) jobNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.jobs))
	for name := range m.jobs {
		names = append(names, name)
	}
	return names
}
