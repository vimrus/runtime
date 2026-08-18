// Package fake supplies a programmable Queue Bridge for Runtime tests.
package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/vimrus/runtime/internal/queue/bridge"
)

type Bridge struct {
	mu sync.Mutex

	CapabilitiesResponse bridge.CapabilitiesResponse
	ClaimResponse        bridge.ClaimResponse
	ExecuteResponse      bridge.ExecuteResponse
	HeartbeatResponse    bridge.HeartbeatResponse
	ReapResponse         bridge.ReapResponse
	StatsResponse        bridge.StatsResponse
	ControlResponse      bridge.ControlResponse

	CapabilitiesRequests []bridge.CapabilitiesRequest
	ClaimRequests        []bridge.ClaimRequest
	ExecuteRequests      []bridge.ExecuteRequest
	HeartbeatRequests    []bridge.HeartbeatRequest
	ReapRequests         []bridge.ReapRequest
	StatsRequests        []bridge.StatsRequest
	ControlRequests      []bridge.ControlRequest
}

func New() *Bridge {
	return &Bridge{
		CapabilitiesResponse: bridge.CapabilitiesResponse{Schema: bridge.SchemaVersion, QueueSchema: 1, Driver: "fake", ClaimMode: "portable_cas", MaxClaimBatch: bridge.MaxBatchSize},
		ClaimResponse:        bridge.ClaimResponse{Schema: bridge.SchemaVersion},
		ExecuteResponse:      bridge.ExecuteResponse{Schema: bridge.SchemaVersion, Result: bridge.ExecutionSuccess},
		HeartbeatResponse:    bridge.HeartbeatResponse{Schema: bridge.SchemaVersion},
		ReapResponse:         bridge.ReapResponse{Schema: bridge.SchemaVersion},
		StatsResponse:        bridge.StatsResponse{Schema: bridge.SchemaVersion},
		ControlResponse:      bridge.ControlResponse{Schema: bridge.SchemaVersion, Applied: true},
	}
}

func (b *Bridge) Capabilities(_ context.Context, request bridge.CapabilitiesRequest) (bridge.CapabilitiesResponse, error) {
	if err := request.Validate(); err != nil {
		return bridge.CapabilitiesResponse{}, fmt.Errorf("validate fake capabilities request: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.CapabilitiesRequests = append(b.CapabilitiesRequests, request)
	if err := b.CapabilitiesResponse.Validate(); err != nil {
		return bridge.CapabilitiesResponse{}, fmt.Errorf("validate fake capabilities response: %w", err)
	}
	return b.CapabilitiesResponse, nil
}
func (b *Bridge) Claim(_ context.Context, request bridge.ClaimRequest) (bridge.ClaimResponse, error) {
	if err := request.Validate(); err != nil {
		return bridge.ClaimResponse{}, fmt.Errorf("validate fake claim request: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ClaimRequests = append(b.ClaimRequests, request)
	if err := b.ClaimResponse.Validate(); err != nil {
		return bridge.ClaimResponse{}, fmt.Errorf("validate fake claim response: %w", err)
	}
	return b.ClaimResponse, nil
}
func (b *Bridge) Execute(_ context.Context, request bridge.ExecuteRequest) (bridge.ExecuteResponse, error) {
	if err := request.Validate(); err != nil {
		return bridge.ExecuteResponse{}, fmt.Errorf("validate fake execute request: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ExecuteRequests = append(b.ExecuteRequests, request)
	if err := b.ExecuteResponse.Validate(); err != nil {
		return bridge.ExecuteResponse{}, fmt.Errorf("validate fake execute response: %w", err)
	}
	return b.ExecuteResponse, nil
}
func (b *Bridge) Heartbeat(_ context.Context, request bridge.HeartbeatRequest) (bridge.HeartbeatResponse, error) {
	if err := request.Validate(); err != nil {
		return bridge.HeartbeatResponse{}, fmt.Errorf("validate fake heartbeat request: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.HeartbeatRequests = append(b.HeartbeatRequests, request)
	if err := b.HeartbeatResponse.Validate(); err != nil {
		return bridge.HeartbeatResponse{}, fmt.Errorf("validate fake heartbeat response: %w", err)
	}
	return b.HeartbeatResponse, nil
}
func (b *Bridge) Reap(_ context.Context, request bridge.ReapRequest) (bridge.ReapResponse, error) {
	if err := request.Validate(); err != nil {
		return bridge.ReapResponse{}, fmt.Errorf("validate fake reap request: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ReapRequests = append(b.ReapRequests, request)
	if err := b.ReapResponse.Validate(); err != nil {
		return bridge.ReapResponse{}, fmt.Errorf("validate fake reap response: %w", err)
	}
	return b.ReapResponse, nil
}
func (b *Bridge) Stats(_ context.Context, request bridge.StatsRequest) (bridge.StatsResponse, error) {
	if err := request.Validate(); err != nil {
		return bridge.StatsResponse{}, fmt.Errorf("validate fake stats request: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.StatsRequests = append(b.StatsRequests, request)
	if err := b.StatsResponse.Validate(); err != nil {
		return bridge.StatsResponse{}, fmt.Errorf("validate fake stats response: %w", err)
	}
	return b.StatsResponse, nil
}
func (b *Bridge) Control(_ context.Context, request bridge.ControlRequest) (bridge.ControlResponse, error) {
	if err := request.Validate(); err != nil {
		return bridge.ControlResponse{}, fmt.Errorf("validate fake control request: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ControlRequests = append(b.ControlRequests, request)
	if err := b.ControlResponse.Validate(); err != nil {
		return bridge.ControlResponse{}, fmt.Errorf("validate fake control response: %w", err)
	}
	return b.ControlResponse, nil
}
