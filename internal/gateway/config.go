// Package gateway generates the embedded Caddy Gateway configuration for
// two Linux nodes without an external load balancer.
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
)

type Node struct {
	ID       string
	Address  string // host:port of the app listener
	Upstream string // http://host:port
}

type Options struct {
	Listen          string
	Nodes           []Node
	HealthPath      string
	HealthInterval  time.Duration
	HealthTimeout   time.Duration
	UnhealthyStatus int
	MaxFails        int
}

func (o Options) validate() error {
	if o.Listen == "" {
		return errors.New("gateway listen address is required")
	}
	if len(o.Nodes) < 1 {
		return errors.New("gateway requires at least one backend node")
	}
	if o.HealthPath == "" {
		o.HealthPath = "/_runtime/readiness"
	}
	if o.HealthInterval <= 0 {
		o.HealthInterval = 5 * time.Second
	}
	if o.HealthTimeout <= 0 {
		o.HealthTimeout = 2 * time.Second
	}
	if o.MaxFails <= 0 {
		o.MaxFails = 3
	}
	return nil
}

// Config renders a Caddy JSON document that proxies to the app listeners
// with least_conn load balancing and active health checks. Retry policy
// deliberately excludes non-idempotent methods.
func Config(options Options) (*caddy.Config, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	nodes := append([]Node(nil), options.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	upstreams := make([]*reverseproxy.Upstream, 0, len(nodes))
	for _, node := range nodes {
		if _, _, err := net.SplitHostPort(node.Address); err != nil {
			return nil, fmt.Errorf("node %s address: %w", node.ID, err)
		}
		if node.Upstream == "" {
			node.Upstream = "http://" + node.Address
		}
		upstream := &reverseproxy.Upstream{
			Dial: node.Upstream,
		}
		upstreams = append(upstreams, upstream)
	}
	loadBalancing := &reverseproxy.LoadBalancing{
		SelectionPolicyRaw: caddyconfig.JSONModuleObject(reverseproxy.LeastConnSelection{}, "policy", "least_conn", nil),
		Retries:            0,
	}
	healthChecks := &reverseproxy.HealthChecks{
		Active: &reverseproxy.ActiveHealthChecks{
			URI:          options.HealthPath,
			Interval:     caddy.Duration(options.HealthInterval),
			Timeout:      caddy.Duration(options.HealthTimeout),
			Fails:        options.MaxFails,
			ExpectStatus: options.UnhealthyStatus,
		},
		Passive: &reverseproxy.PassiveHealthChecks{
			FailDuration:    caddy.Duration(options.HealthInterval),
			MaxFails:        options.MaxFails,
			UnhealthyStatus: []int{options.UnhealthyStatus},
		},
	}
	handler := &reverseproxy.Handler{
		Upstreams:     upstreams,
		LoadBalancing: loadBalancing,
		HealthChecks:  healthChecks,
	}
	server := &caddyhttp.Server{
		Listen: []string{options.Listen},
		Routes: caddyhttp.RouteList{
			{
				HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(handler, "handler", "reverse_proxy", nil)},
			},
		},
	}
	persist := false
	return &caddy.Config{
		Admin: &caddy.AdminConfig{
			Disabled: true,
			Config:   &caddy.ConfigSettings{Persist: &persist},
		},
		AppsRaw: caddy.ModuleMap{
			"http": caddyconfig.JSON(caddyhttp.App{Servers: map[string]*caddyhttp.Server{"gateway": server}}, nil),
		},
	}, nil
}
