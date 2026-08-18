package scheduler

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestTriggerRunsRegisteredJobAndRejectsUnknown(t *testing.T) {
	manager := New(1, slog.Default())
	var runs atomic.Int64
	if err := manager.Register(Job{
		Name:     "index.rebuild",
		Interval: time.Hour,
		Timeout:  time.Second,
		Run: func(ctx context.Context) error {
			runs.Add(1)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Trigger(context.Background(), "index.rebuild"); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 1 {
		t.Fatalf("runs = %d, want 1", runs.Load())
	}
	if err := manager.Trigger(context.Background(), "rm -rf /"); err == nil {
		t.Fatal("unregistered job must be rejected")
	}
}

func TestRegisterRejectsDuplicateAndNilCallable(t *testing.T) {
	manager := New(1, slog.Default())
	job := Job{Name: "job", Interval: time.Second, Run: func(context.Context) error { return nil }}
	if err := manager.Register(job); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(job); err == nil {
		t.Fatal("duplicate registration must fail")
	}
	if err := manager.Register(Job{Name: "job2", Interval: time.Second}); err == nil {
		t.Fatal("nil callable must fail")
	}
}

func TestStatusTracksExecution(t *testing.T) {
	manager := New(1, slog.Default())
	if err := manager.Register(Job{Name: "job", Interval: time.Hour, Run: func(context.Context) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		manager.Run(ctx)
		close(done)
	}()
	if err := manager.Trigger(context.Background(), "job"); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	status := manager.Status()["job"]
	if status.Runs != 1 || status.Running {
		t.Fatalf("unexpected status: %#v", status)
	}
}
