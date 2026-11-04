package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vimrus/runtime/internal/config"
)

const (
	ProfileTokenHeader  = "X-Zentao-Runtime-Profile-Token"
	ProfileSchemaHeader = "X-Zentao-Runtime-Profile-Schema"
	profileMaxBytes     = 256 * 1024
)

// RegistrationResponse is returned by the PHP Runtime Profile Bridge.
type RegistrationResponse struct {
	SchemaVersion     int      `json:"schemaVersion"`
	Status            string   `json:"status"`
	ProfileGeneration uint64   `json:"profileGeneration,omitempty"`
	MismatchDomains   []string `json:"mismatchDomains,omitempty"`
	RetryAfterSeconds int      `json:"retryAfterSeconds,omitempty"`
	Message           string   `json:"message,omitempty"`
}

type ProfileClient struct {
	httpClient *http.Client
	endpoint   string
	token      string
}

func NewProfileClient(endpoint, token string, timeout time.Duration) *ProfileClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &ProfileClient{
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		endpoint: endpoint,
		token:    token,
	}
}

func (c *ProfileClient) Register(ctx context.Context, profile Profile) (RegistrationResponse, error) {
	body, err := json.Marshal(profile)
	if err != nil {
		return RegistrationResponse{}, fmt.Errorf("encode runtime profile: %w", err)
	}
	if len(body) > profileMaxBytes {
		return RegistrationResponse{}, errors.New("runtime profile is too large")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return RegistrationResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(ProfileTokenHeader, c.token)
	request.Header.Set(ProfileSchemaHeader, fmt.Sprint(ProfileSchemaVersion))

	response, err := c.httpClient.Do(request)
	if err != nil {
		return RegistrationResponse{}, fmt.Errorf("register runtime profile: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return RegistrationResponse{}, fmt.Errorf("register runtime profile: HTTP %d", response.StatusCode)
	}
	var result RegistrationResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, profileMaxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return RegistrationResponse{}, fmt.Errorf("decode runtime profile response: %w", err)
	}
	if result.SchemaVersion != ProfileSchemaVersion {
		return RegistrationResponse{}, errors.New("unsupported runtime profile response schema")
	}
	switch result.Status {
	case "ready", "registering", "blocked", "mismatch", "stale", "unsupported-profile":
	default:
		return RegistrationResponse{}, fmt.Errorf("unsupported runtime admission status %q", result.Status)
	}
	return result, nil
}

// ProfileEndpoint returns the loopback application endpoint used for profile
// registration.
func ProfileEndpoint(webListen, appPath string) (string, error) {
	address, err := config.LoopbackWebAddress(webListen)
	if err != nil {
		return "", err
	}
	prefix, err := config.NormalizePath(appPath)
	if err != nil {
		return "", err
	}
	path := "/index.php"
	if prefix != "/" {
		path = strings.TrimRight(prefix, "/") + path
	}
	endpoint := url.URL{
		Scheme:   "http",
		Host:     address,
		Path:     path,
		RawQuery: "m=runtime&f=profile",
	}
	return endpoint.String(), nil
}
