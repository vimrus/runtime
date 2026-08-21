// Package client implements the private, loopback-only PHP Queue Bridge
// transport used by the Runtime Worker Engine.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vimrus/runtime/internal/queue/bridge"
)

const (
	authHeader       = "X-Zentao-Queue-Token"
	schemaHeader     = "X-Zentao-Queue-Schema"
	maxResponseBytes = 64 * 1024
)

type Config struct {
	BaseURL        string
	Token          string
	RequestTimeout time.Duration
}

type Client struct {
	base    string
	token   string
	timeout time.Duration
	http    *http.Client
}

func New(config Config) (*Client, error) {
	if config.Token == "" {
		return nil, errors.New("queue bridge token is required")
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = 5 * time.Second
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse queue bridge base URL: %w", err)
	}
	if err := requireLoopback(parsed); err != nil {
		return nil, err
	}
	// The Bridge path constants already carry the fixed
	// /internal/runtime/queue/v1 prefix, so normalize the base URL to the
	// loopback origin. Accepts both "http://127.0.0.1:8081" and the full
	// prefix form from older configurations.
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return &Client{
		base:    strings.TrimRight(parsed.String(), "/"),
		token:   config.Token,
		timeout: config.RequestTimeout,
		http:    &http.Client{Timeout: config.RequestTimeout},
	}, nil
}

func requireLoopback(parsed *url.URL) error {
	if parsed.Scheme != "http" {
		return errors.New("queue bridge must use http on a loopback address")
	}
	if parsed.User != nil {
		return errors.New("queue bridge URL must not contain credentials")
	}
	host := parsed.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	ips, err := net.LookupIP(host)
	if err == nil {
		for _, ip := range ips {
			if ip.IsLoopback() {
				return nil
			}
		}
	}
	return fmt.Errorf("queue bridge URL %q is not loopback", parsed.Host)
}

func (c *Client) Capabilities(ctx context.Context, request bridge.CapabilitiesRequest) (bridge.CapabilitiesResponse, error) {
	var response bridge.CapabilitiesResponse
	err := c.post(ctx, bridge.PathCapabilities, request, &response)
	return response, err
}

func (c *Client) Claim(ctx context.Context, request bridge.ClaimRequest) (bridge.ClaimResponse, error) {
	var response bridge.ClaimResponse
	err := c.post(ctx, bridge.PathClaim, request, &response)
	return response, err
}

func (c *Client) Execute(ctx context.Context, request bridge.ExecuteRequest) (bridge.ExecuteResponse, error) {
	var response bridge.ExecuteResponse
	err := c.post(ctx, bridge.PathExecute, request, &response)
	return response, err
}

func (c *Client) Heartbeat(ctx context.Context, request bridge.HeartbeatRequest) (bridge.HeartbeatResponse, error) {
	var response bridge.HeartbeatResponse
	err := c.post(ctx, bridge.PathHeartbeat, request, &response)
	return response, err
}

func (c *Client) Reap(ctx context.Context, request bridge.ReapRequest) (bridge.ReapResponse, error) {
	var response bridge.ReapResponse
	err := c.post(ctx, bridge.PathReap, request, &response)
	return response, err
}

func (c *Client) Stats(ctx context.Context, request bridge.StatsRequest) (bridge.StatsResponse, error) {
	var response bridge.StatsResponse
	err := c.post(ctx, bridge.PathStats, request, &response)
	return response, err
}

func (c *Client) Control(ctx context.Context, request bridge.ControlRequest) (bridge.ControlResponse, error) {
	var response bridge.ControlResponse
	err := c.post(ctx, bridge.PathControl, request, &response)
	return response, err
}

func (c *Client) post(ctx context.Context, path string, request bridge.Request, response bridge.Response) error {
	body, err := bridge.Encode(request)
	if err != nil {
		return fmt.Errorf("encode queue bridge request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set(authHeader, c.token)
	httpRequest.Header.Set(schemaHeader, fmt.Sprintf("%d", bridge.SchemaVersion))
	httpResponse, err := c.http.Do(httpRequest)
	if err != nil {
		return bridge.Error{Code: bridge.ErrorUnavailable, Message: "queue bridge transport failed", Retryable: true}
	}
	defer httpResponse.Body.Close()
	data, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes+1))
	if err != nil {
		return bridge.Error{Code: bridge.ErrorUnavailable, Message: "queue bridge response read failed", Retryable: true}
	}
	if len(data) > maxResponseBytes {
		return bridge.Error{Code: bridge.ErrorInvalidResponse, Message: "queue bridge response too large"}
	}
	if httpResponse.StatusCode >= 200 && httpResponse.StatusCode < 300 {
		if err := bridge.DecodeResponse(data, response); err != nil {
			return fmt.Errorf("decode queue bridge response: %w", err)
		}
		return nil
	}
	var wireError struct {
		Error bridge.Error `json:"error"`
	}
	if err := json.Unmarshal(data, &wireError); err != nil || wireError.Error.Code == "" {
		return bridge.Error{Code: bridge.ErrorUnavailable, Message: fmt.Sprintf("queue bridge returned %s", httpResponse.Status), Retryable: httpResponse.StatusCode >= 500}
	}
	return wireError.Error
}
