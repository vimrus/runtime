package cluster

import (
	"context"
	"strings"
	"time"
)

type ProfileRegistrar interface {
	Register(context.Context, Profile) (RegistrationResponse, error)
}

type Target struct {
	AppID     string
	Registrar ProfileRegistrar
	Gate      *Gate
}

// Controller keeps every application's profile registered and updates its
// configuration diagnostic state.
type Controller struct {
	Targets           []Target
	HeartbeatInterval time.Duration
	ProfileInterval   time.Duration
	RetryInterval     time.Duration
	StaleTimeout      time.Duration
	Now               func() time.Time
	Observe           func(appID string, response RegistrationResponse, err error)
}

func NewController(targets []Target, heartbeat, profile, stale time.Duration) *Controller {
	if heartbeat <= 0 {
		heartbeat = time.Minute
	}
	if profile <= 0 {
		profile = heartbeat
	}
	if stale <= 2*heartbeat {
		stale = 2*heartbeat + time.Minute
	}
	return &Controller{
		Targets:           targets,
		HeartbeatInterval: heartbeat,
		ProfileInterval:   profile,
		RetryInterval:     5 * time.Second,
		StaleTimeout:      stale,
		Now:               time.Now,
	}
}

// TryAll performs one registration round and reports whether all targets are
// currently ready.
func (c *Controller) TryAll(ctx context.Context, profile Profile) bool {
	allReady := len(c.Targets) > 0
	for _, target := range c.Targets {
		if target.Gate != nil {
			previous := target.Gate.Status()
			if previous.State != StateReady {
				target.Gate.Set(StateChecking, "registering node profile")
			}
			response, err := target.Registrar.Register(ctx, profile)
			if c.Observe != nil {
				c.Observe(target.AppID, response, err)
			}
			if err != nil {
				allReady = false
				c.handleFailure(target.Gate, previous, err)
				continue
			}
			if !c.applyResponse(target.Gate, response) {
				allReady = false
			}
			continue
		}
		response, err := target.Registrar.Register(ctx, profile)
		if c.Observe != nil {
			c.Observe(target.AppID, response, err)
		}
		if err != nil {
			allReady = false
			continue
		}
		if !c.applyResponse(nil, response) {
			allReady = false
		}
	}
	return allReady
}

func (c *Controller) applyResponse(gate *Gate, response RegistrationResponse) bool {
	switch response.Status {
	case "ready":
		if gate != nil {
			gate.SetReadyAt(response.ProfileGeneration, c.Now().UTC())
		}
		return true
	case "registering":
		c.setGate(gate, StateRegistering, "profile registration is pending")
	case "mismatch":
		reason := mismatchReason(response.MismatchDomains)
		c.setGate(gate, StateMismatch, reason)
	case "blocked":
		reason := mismatchReason(response.MismatchDomains)
		c.setGate(gate, StateBlocked, reason)
	case "stale":
		c.setGate(gate, StateStale, "node heartbeat expired")
	case "unsupported-profile":
		c.setGate(gate, StateBlocked, "runtime profile schema is unsupported")
	default:
		c.setGate(gate, StateBlocked, "unknown cluster admission status")
	}
	return false
}

// Run registers immediately, refreshes the node profile on ProfileInterval and
// sends the cached profile for intermediate heartbeats.
func (c *Controller) Run(ctx context.Context, build func() Profile) {
	delay := time.Duration(0)
	var profile Profile
	var lastBuild time.Time
	for {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		now := c.Now().UTC()
		if c.shouldRebuildProfile(now, lastBuild) {
			profile = build()
			lastBuild = now
		}
		ready := c.TryAll(ctx, profile)
		if ready {
			delay = c.HeartbeatInterval
		} else {
			delay = c.RetryInterval
		}
	}
}

func (c *Controller) shouldRebuildProfile(now, lastBuild time.Time) bool {
	return lastBuild.IsZero() || c.ProfileInterval <= 0 || !now.Before(lastBuild.Add(c.ProfileInterval))
}

func (c *Controller) handleFailure(gate *Gate, previous AdmissionStatus, err error) {
	if gate == nil {
		return
	}
	if previous.State == StateReady {
		now := c.Now()
		if !previous.LastSuccess.IsZero() && now.Sub(previous.LastSuccess) <= c.StaleTimeout {
			return
		}
		gate.Set(StateStale, "profile heartbeat exceeded stale timeout")
		return
	}
	gate.Set(StateRegistering, "profile registration failed: "+sanitizeReason(err.Error()))
}

func (c *Controller) setGate(gate *Gate, state AdmissionState, reason string) {
	if gate != nil {
		gate.Set(state, reason)
	}
}

func mismatchReason(domains []string) string {
	if len(domains) == 0 {
		return "cluster configuration check failed"
	}
	return "cluster configuration mismatch: " + strings.Join(domains, ",")
}

func sanitizeReason(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 240 {
		value = value[:240]
	}
	return value
}
