//go:build windows

// Package windows implements Windows service integration for the Runtime
// Host: SCM lifecycle, event logging and bounded stop.
package windows

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

type Service struct {
	Name        string
	Start       func(ctx context.Context) error
	Stop        func(ctx context.Context) error
	StopTimeout time.Duration
}

func Run(service Service) error {
	if service.Name == "" || service.Start == nil {
		return errors.New("windows service name and start callback are required")
	}
	if service.StopTimeout <= 0 {
		service.StopTimeout = 45 * time.Second
	}
	eventLog, err := eventlog.Open(service.Name)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer eventLog.Close()
	_ = eventLog.Info(1, fmt.Sprintf("%s starting", service.Name))
	err = svc.Run(service.Name, &serviceHandler{service: service, eventLog: eventLog})
	if err != nil {
		_ = eventLog.Error(3, fmt.Sprintf("%s failed: %v", service.Name, err))
	}
	return err
}

type serviceHandler struct {
	service  Service
	eventLog *eventlog.Log
}

func (h *serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- h.service.Start(ctx) }()

	select {
	case err := <-started:
		if err != nil {
			cancel()
			status <- svc.Status{State: svc.Stopped}
			return false, 1
		}
	case <-time.After(30 * time.Second):
		cancel()
		status <- svc.Status{State: svc.Stopped}
		return false, 2
	}
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	_ = h.eventLog.Info(1, fmt.Sprintf("%s running", h.service.Name))

	for change := range requests {
		switch change.Cmd {
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			stopCtx, stopCancel := context.WithTimeout(context.Background(), h.service.StopTimeout)
			stopErr := h.service.Stop(stopCtx)
			cancel()
			stopCancel()
			status <- svc.Status{State: svc.Stopped}
			if stopErr != nil {
				_ = h.eventLog.Error(3, fmt.Sprintf("%s stop failed: %v", h.service.Name, stopErr))
				return false, 3
			}
			return false, 0
		case svc.Interrogate:
			status <- change.CurrentStatus
		}
	}
	cancel()
	status <- svc.Status{State: svc.Stopped}
	return false, 0
}
