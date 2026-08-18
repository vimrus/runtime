// Package config loads and validates the versioned Runtime configuration.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = 1

type Config struct {
	SchemaVersion int           `json:"schemaVersion"`
	Runtime       Runtime       `json:"runtime"`
	Web           Web           `json:"web"`
	Queue         Queue         `json:"queue,omitempty"`
	Observability Observability `json:"observability,omitempty"`
}

type Runtime struct {
	ControlSocket string        `json:"controlSocket"`
	PIDFile       string        `json:"pidFile"`
	DrainTimeout  time.Duration `json:"drainTimeout"`
	AuditLog      string        `json:"auditLog,omitempty"`
	LogPath       string        `json:"logPath,omitempty"`
	LogMaxBytes   int64         `json:"logMaxBytes,omitempty"`
	LogMaxBackups int           `json:"logMaxBackups,omitempty"`
	NodeID        string        `json:"nodeID,omitempty"`
	ClusterID     string        `json:"clusterID,omitempty"`
}

type Web struct {
	Root              string        `json:"root"`
	Listen            string        `json:"listen"`
	Threads           int           `json:"threads"`
	ReadHeaderTimeout time.Duration `json:"readHeaderTimeout"`
	IdleTimeout       time.Duration `json:"idleTimeout"`
	MaxHeaderBytes    int           `json:"maxHeaderBytes"`
	AccessLog         string        `json:"accessLog,omitempty"`
}

type Queue struct {
	Enabled           bool          `json:"enabled"`
	BridgeBaseURL     string        `json:"bridgeBaseURL"`
	BridgeToken       string        `json:"bridgeToken,omitempty"`
	RequestTimeout    time.Duration `json:"requestTimeout"`
	ClaimBatch        int           `json:"claimBatch"`
	LeaseSeconds      int           `json:"leaseSeconds"`
	HeartbeatInterval time.Duration `json:"heartbeatInterval"`
	ReapInterval      time.Duration `json:"reapInterval"`
	ReapBatch         int           `json:"reapBatch"`
	DrainTimeout      time.Duration `json:"drainTimeout"`
	Workers           []QueueWorker `json:"workers"`
}

type QueueWorker struct {
	Name        string        `json:"name"`
	Concurrency int           `json:"concurrency"`
	MinPoll     time.Duration `json:"minPoll"`
	MaxPoll     time.Duration `json:"maxPoll"`
}

type Observability struct {
	Enabled       bool          `json:"enabled"`
	DatasetRoot   string        `json:"datasetRoot"`
	SpoolPath     string        `json:"spoolPath"`
	MaxSpoolBytes int64         `json:"maxSpoolBytes"`
	MaxBatchRows  int           `json:"maxBatchRows"`
	MaxBatchBytes int64         `json:"maxBatchBytes"`
	FlushInterval time.Duration `json:"flushInterval"`
	MetricsDays   int           `json:"metricsDays"`
	LogDays       int           `json:"logDays"`
}

type Overrides struct {
	Root                 *string
	Listen               *string
	Threads              *int
	ControlSocket        *string
	PIDFile              *string
	DrainTimeout         *time.Duration
	ReadHeaderTimeout    *time.Duration
	IdleTimeout          *time.Duration
	MaxHeaderBytes       *int
	AccessLog            *string
	AuditLog             *string
	LogPath              *string
	LogMaxBytes          *int64
	LogMaxBackups        *int
	NodeID               *string
	ClusterID            *string
	QueueEnabled         *bool
	QueueBridgeURL       *string
	QueueToken           *string
	ObservabilityEnabled *bool
	ObservabilityRoot    *string
	ObservabilitySpool   *string
}

func Default() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Runtime: Runtime{
			ControlSocket: defaultControlSocket(),
			PIDFile:       defaultPIDFile(),
			DrainTimeout:  30 * time.Second,
			LogMaxBytes:   16 * 1024 * 1024,
			LogMaxBackups: 5,
		},
		Web: Web{
			Listen:            "127.0.0.1:8080",
			Threads:           4,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       30 * time.Second,
			MaxHeaderBytes:    16 * 1024,
		},
		Queue: Queue{
			RequestTimeout:    5 * time.Second,
			ClaimBatch:        8,
			LeaseSeconds:      60,
			HeartbeatInterval: 20 * time.Second,
			ReapInterval:      10 * time.Second,
			ReapBatch:         100,
			DrainTimeout:      30 * time.Second,
			Workers: []QueueWorker{
				{Name: "default", Concurrency: 4, MinPoll: 50 * time.Millisecond, MaxPoll: 5 * time.Second},
			},
		},
		Observability: Observability{
			MaxSpoolBytes: 1024 * 1024 * 1024,
			MaxBatchRows:  10000,
			MaxBatchBytes: 16 * 1024 * 1024,
			FlushInterval: 60 * time.Second,
			MetricsDays:   30,
			LogDays:       7,
		},
	}
}

func Load(path string, environ []string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read runtime configuration: %w", err)
	}

	config, err := decode(data)
	if err != nil {
		return Config{}, err
	}
	config, err = ApplyEnvironment(config, environ)
	if err != nil {
		return Config{}, err
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func decode(data []byte) (Config, error) {
	type rawRuntime struct {
		ControlSocket *string `json:"controlSocket"`
		PIDFile       *string `json:"pidFile"`
		DrainTimeout  *string `json:"drainTimeout"`
		AuditLog      *string `json:"auditLog"`
		LogPath       *string `json:"logPath"`
		LogMaxBytes   *int64  `json:"logMaxBytes"`
		LogMaxBackups *int    `json:"logMaxBackups"`
		NodeID        *string `json:"nodeID"`
		ClusterID     *string `json:"clusterID"`
	}
	type rawWeb struct {
		Root              *string `json:"root"`
		Listen            *string `json:"listen"`
		Threads           *int    `json:"threads"`
		ReadHeaderTimeout *string `json:"readHeaderTimeout"`
		IdleTimeout       *string `json:"idleTimeout"`
		MaxHeaderBytes    *int    `json:"maxHeaderBytes"`
		AccessLog         *string `json:"accessLog"`
	}
	type rawQueueWorker struct {
		Name        *string `json:"name"`
		Concurrency *int    `json:"concurrency"`
		MinPoll     *string `json:"minPoll"`
		MaxPoll     *string `json:"maxPoll"`
	}
	type rawQueue struct {
		Enabled           *bool             `json:"enabled"`
		BridgeBaseURL     *string           `json:"bridgeBaseURL"`
		BridgeToken       *string           `json:"bridgeToken"`
		RequestTimeout    *string           `json:"requestTimeout"`
		ClaimBatch        *int              `json:"claimBatch"`
		LeaseSeconds      *int              `json:"leaseSeconds"`
		HeartbeatInterval *string           `json:"heartbeatInterval"`
		ReapInterval      *string           `json:"reapInterval"`
		ReapBatch         *int              `json:"reapBatch"`
		DrainTimeout      *string           `json:"drainTimeout"`
		Workers           *[]rawQueueWorker `json:"workers"`
	}
	type rawObservability struct {
		Enabled       *bool   `json:"enabled"`
		DatasetRoot   *string `json:"datasetRoot"`
		SpoolPath     *string `json:"spoolPath"`
		MaxSpoolBytes *int64  `json:"maxSpoolBytes"`
		MaxBatchRows  *int    `json:"maxBatchRows"`
		MaxBatchBytes *int64  `json:"maxBatchBytes"`
		FlushInterval *string `json:"flushInterval"`
		MetricsDays   *int    `json:"metricsDays"`
		LogDays       *int    `json:"logDays"`
	}
	type rawConfig struct {
		SchemaVersion *int              `json:"schemaVersion"`
		Runtime       *rawRuntime       `json:"runtime"`
		Web           *rawWeb           `json:"web"`
		Queue         *rawQueue         `json:"queue"`
		Observability *rawObservability `json:"observability"`
	}

	config := Default()
	var raw rawConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode runtime configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Config{}, fmt.Errorf("decode runtime configuration: multiple JSON values")
	}
	if raw.SchemaVersion != nil {
		config.SchemaVersion = *raw.SchemaVersion
	}
	if raw.Runtime != nil {
		if raw.Runtime.ControlSocket != nil {
			config.Runtime.ControlSocket = *raw.Runtime.ControlSocket
		}
		if raw.Runtime.PIDFile != nil {
			config.Runtime.PIDFile = *raw.Runtime.PIDFile
		}
		if raw.Runtime.DrainTimeout != nil {
			value, err := time.ParseDuration(*raw.Runtime.DrainTimeout)
			if err != nil {
				return Config{}, fmt.Errorf("parse runtime.drainTimeout: %w", err)
			}
			config.Runtime.DrainTimeout = value
		}
		if raw.Runtime.AuditLog != nil {
			config.Runtime.AuditLog = *raw.Runtime.AuditLog
		}
		if raw.Runtime.LogPath != nil {
			config.Runtime.LogPath = *raw.Runtime.LogPath
		}
		if raw.Runtime.LogMaxBytes != nil {
			config.Runtime.LogMaxBytes = *raw.Runtime.LogMaxBytes
		}
		if raw.Runtime.LogMaxBackups != nil {
			config.Runtime.LogMaxBackups = *raw.Runtime.LogMaxBackups
		}
		if raw.Runtime.NodeID != nil {
			config.Runtime.NodeID = *raw.Runtime.NodeID
		}
		if raw.Runtime.ClusterID != nil {
			config.Runtime.ClusterID = *raw.Runtime.ClusterID
		}
	}
	if raw.Web != nil {
		if raw.Web.Root != nil {
			config.Web.Root = *raw.Web.Root
		}
		if raw.Web.Listen != nil {
			config.Web.Listen = *raw.Web.Listen
		}
		if raw.Web.Threads != nil {
			config.Web.Threads = *raw.Web.Threads
		}
		if raw.Web.MaxHeaderBytes != nil {
			config.Web.MaxHeaderBytes = *raw.Web.MaxHeaderBytes
		}
		if raw.Web.ReadHeaderTimeout != nil {
			value, err := time.ParseDuration(*raw.Web.ReadHeaderTimeout)
			if err != nil {
				return Config{}, fmt.Errorf("parse web.readHeaderTimeout: %w", err)
			}
			config.Web.ReadHeaderTimeout = value
		}
		if raw.Web.IdleTimeout != nil {
			value, err := time.ParseDuration(*raw.Web.IdleTimeout)
			if err != nil {
				return Config{}, fmt.Errorf("parse web.idleTimeout: %w", err)
			}
			config.Web.IdleTimeout = value
		}
		if raw.Web.AccessLog != nil {
			config.Web.AccessLog = *raw.Web.AccessLog
		}
	}
	if raw.Queue != nil {
		if raw.Queue.Enabled != nil {
			config.Queue.Enabled = *raw.Queue.Enabled
		}
		if raw.Queue.BridgeBaseURL != nil {
			config.Queue.BridgeBaseURL = *raw.Queue.BridgeBaseURL
		}
		if raw.Queue.BridgeToken != nil {
			config.Queue.BridgeToken = *raw.Queue.BridgeToken
		}
		if raw.Queue.RequestTimeout != nil {
			value, err := time.ParseDuration(*raw.Queue.RequestTimeout)
			if err != nil {
				return Config{}, fmt.Errorf("parse queue.requestTimeout: %w", err)
			}
			config.Queue.RequestTimeout = value
		}
		if raw.Queue.ClaimBatch != nil {
			config.Queue.ClaimBatch = *raw.Queue.ClaimBatch
		}
		if raw.Queue.LeaseSeconds != nil {
			config.Queue.LeaseSeconds = *raw.Queue.LeaseSeconds
		}
		if raw.Queue.HeartbeatInterval != nil {
			value, err := time.ParseDuration(*raw.Queue.HeartbeatInterval)
			if err != nil {
				return Config{}, fmt.Errorf("parse queue.heartbeatInterval: %w", err)
			}
			config.Queue.HeartbeatInterval = value
		}
		if raw.Queue.ReapInterval != nil {
			value, err := time.ParseDuration(*raw.Queue.ReapInterval)
			if err != nil {
				return Config{}, fmt.Errorf("parse queue.reapInterval: %w", err)
			}
			config.Queue.ReapInterval = value
		}
		if raw.Queue.ReapBatch != nil {
			config.Queue.ReapBatch = *raw.Queue.ReapBatch
		}
		if raw.Queue.DrainTimeout != nil {
			value, err := time.ParseDuration(*raw.Queue.DrainTimeout)
			if err != nil {
				return Config{}, fmt.Errorf("parse queue.drainTimeout: %w", err)
			}
			config.Queue.DrainTimeout = value
		}
		if raw.Queue.Workers != nil {
			config.Queue.Workers = nil
			for _, rawWorker := range *raw.Queue.Workers {
				worker := QueueWorker{}
				if rawWorker.Name != nil {
					worker.Name = *rawWorker.Name
				}
				if rawWorker.Concurrency != nil {
					worker.Concurrency = *rawWorker.Concurrency
				}
				if rawWorker.MinPoll != nil {
					value, err := time.ParseDuration(*rawWorker.MinPoll)
					if err != nil {
						return Config{}, fmt.Errorf("parse queue worker minPoll: %w", err)
					}
					worker.MinPoll = value
				}
				if rawWorker.MaxPoll != nil {
					value, err := time.ParseDuration(*rawWorker.MaxPoll)
					if err != nil {
						return Config{}, fmt.Errorf("parse queue worker maxPoll: %w", err)
					}
					worker.MaxPoll = value
				}
				config.Queue.Workers = append(config.Queue.Workers, worker)
			}
		}
	}
	if raw.Observability != nil {
		if raw.Observability.Enabled != nil {
			config.Observability.Enabled = *raw.Observability.Enabled
		}
		if raw.Observability.DatasetRoot != nil {
			config.Observability.DatasetRoot = *raw.Observability.DatasetRoot
		}
		if raw.Observability.SpoolPath != nil {
			config.Observability.SpoolPath = *raw.Observability.SpoolPath
		}
		if raw.Observability.MaxSpoolBytes != nil {
			config.Observability.MaxSpoolBytes = *raw.Observability.MaxSpoolBytes
		}
		if raw.Observability.MaxBatchRows != nil {
			config.Observability.MaxBatchRows = *raw.Observability.MaxBatchRows
		}
		if raw.Observability.MaxBatchBytes != nil {
			config.Observability.MaxBatchBytes = *raw.Observability.MaxBatchBytes
		}
		if raw.Observability.FlushInterval != nil {
			value, err := time.ParseDuration(*raw.Observability.FlushInterval)
			if err != nil {
				return Config{}, fmt.Errorf("parse observability.flushInterval: %w", err)
			}
			config.Observability.FlushInterval = value
		}
		if raw.Observability.MetricsDays != nil {
			config.Observability.MetricsDays = *raw.Observability.MetricsDays
		}
		if raw.Observability.LogDays != nil {
			config.Observability.LogDays = *raw.Observability.LogDays
		}
	}
	return config, nil
}

func (c Config) Apply(overrides Overrides) (Config, error) {
	if overrides.Root != nil {
		c.Web.Root = *overrides.Root
	}
	if overrides.Listen != nil {
		c.Web.Listen = *overrides.Listen
	}
	if overrides.Threads != nil {
		c.Web.Threads = *overrides.Threads
	}
	if overrides.ControlSocket != nil {
		c.Runtime.ControlSocket = *overrides.ControlSocket
	}
	if overrides.PIDFile != nil {
		c.Runtime.PIDFile = *overrides.PIDFile
	}
	if overrides.DrainTimeout != nil {
		c.Runtime.DrainTimeout = *overrides.DrainTimeout
	}
	if overrides.ReadHeaderTimeout != nil {
		c.Web.ReadHeaderTimeout = *overrides.ReadHeaderTimeout
	}
	if overrides.IdleTimeout != nil {
		c.Web.IdleTimeout = *overrides.IdleTimeout
	}
	if overrides.MaxHeaderBytes != nil {
		c.Web.MaxHeaderBytes = *overrides.MaxHeaderBytes
	}
	if overrides.AccessLog != nil {
		c.Web.AccessLog = *overrides.AccessLog
	}
	if overrides.AuditLog != nil {
		c.Runtime.AuditLog = *overrides.AuditLog
	}
	if overrides.LogPath != nil {
		c.Runtime.LogPath = *overrides.LogPath
	}
	if overrides.LogMaxBytes != nil {
		c.Runtime.LogMaxBytes = *overrides.LogMaxBytes
	}
	if overrides.LogMaxBackups != nil {
		c.Runtime.LogMaxBackups = *overrides.LogMaxBackups
	}
	if overrides.NodeID != nil {
		c.Runtime.NodeID = *overrides.NodeID
	}
	if overrides.ClusterID != nil {
		c.Runtime.ClusterID = *overrides.ClusterID
	}
	if overrides.QueueEnabled != nil {
		c.Queue.Enabled = *overrides.QueueEnabled
	}
	if overrides.QueueBridgeURL != nil {
		c.Queue.BridgeBaseURL = *overrides.QueueBridgeURL
	}
	if overrides.QueueToken != nil {
		c.Queue.BridgeToken = *overrides.QueueToken
	}
	if overrides.ObservabilityEnabled != nil {
		c.Observability.Enabled = *overrides.ObservabilityEnabled
	}
	if overrides.ObservabilityRoot != nil {
		c.Observability.DatasetRoot = *overrides.ObservabilityRoot
	}
	if overrides.ObservabilitySpool != nil {
		c.Observability.SpoolPath = *overrides.ObservabilitySpool
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func ApplyEnvironment(config Config, environ []string) (Config, error) {
	values := make(map[string]string)
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}

	overrides := Overrides{}
	if value, ok := values["ZENTAO_RUNTIME_ROOT"]; ok {
		overrides.Root = &value
	}
	if value, ok := values["ZENTAO_RUNTIME_LISTEN"]; ok {
		overrides.Listen = &value
	}
	if value, ok := values["ZENTAO_RUNTIME_CONTROL_SOCKET"]; ok {
		overrides.ControlSocket = &value
	}
	if value, ok := values["ZENTAO_RUNTIME_PID_FILE"]; ok {
		overrides.PIDFile = &value
	}
	if value, ok := values["ZENTAO_RUNTIME_THREADS"]; ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse ZENTAO_RUNTIME_THREADS: %w", err)
		}
		overrides.Threads = &parsed
	}
	if value, ok := values["ZENTAO_RUNTIME_DRAIN_TIMEOUT"]; ok {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse ZENTAO_RUNTIME_DRAIN_TIMEOUT: %w", err)
		}
		overrides.DrainTimeout = &parsed
	}
	if value, ok := values["ZENTAO_RUNTIME_AUDIT_LOG"]; ok {
		overrides.AuditLog = &value
	}
	if value, ok := values["ZENTAO_RUNTIME_LOG_PATH"]; ok {
		overrides.LogPath = &value
	}
	if value, ok := values["ZENTAO_RUNTIME_LOG_MAX_BYTES"]; ok {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parse ZENTAO_RUNTIME_LOG_MAX_BYTES: %w", err)
		}
		overrides.LogMaxBytes = &parsed
	}
	if value, ok := values["ZENTAO_RUNTIME_LOG_MAX_BACKUPS"]; ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse ZENTAO_RUNTIME_LOG_MAX_BACKUPS: %w", err)
		}
		overrides.LogMaxBackups = &parsed
	}
	if value, ok := values["ZENTAO_RUNTIME_NODE_ID"]; ok {
		overrides.NodeID = &value
	}
	if value, ok := values["ZENTAO_RUNTIME_CLUSTER_ID"]; ok {
		overrides.ClusterID = &value
	}
	if value, ok := values["ZENTAO_RUNTIME_ACCESS_LOG"]; ok {
		overrides.AccessLog = &value
	}
	if value, ok := values["ZENTAO_QUEUE_ENABLED"]; ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse ZENTAO_QUEUE_ENABLED: %w", err)
		}
		overrides.QueueEnabled = &parsed
	}
	if value, ok := values["ZENTAO_QUEUE_BRIDGE_URL"]; ok {
		overrides.QueueBridgeURL = &value
	}
	if value, ok := values["ZENTAO_QUEUE_TOKEN"]; ok {
		overrides.QueueToken = &value
	}
	if value, ok := values["ZENTAO_OBSERVABILITY_ENABLED"]; ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse ZENTAO_OBSERVABILITY_ENABLED: %w", err)
		}
		overrides.ObservabilityEnabled = &parsed
	}
	if value, ok := values["ZENTAO_OBSERVABILITY_DATASET_ROOT"]; ok {
		overrides.ObservabilityRoot = &value
	}
	if value, ok := values["ZENTAO_OBSERVABILITY_SPOOL_PATH"]; ok {
		overrides.ObservabilitySpool = &value
	}
	return config.Apply(overrides)
}

func (c Config) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported runtime configuration schemaVersion %d", c.SchemaVersion)
	}
	if c.Web.Root == "" || !filepath.IsAbs(c.Web.Root) {
		return fmt.Errorf("web.root must be an absolute path")
	}
	if _, _, err := net.SplitHostPort(c.Web.Listen); err != nil {
		return fmt.Errorf("web.listen: %w", err)
	}
	if c.Web.Threads < 2 || c.Web.Threads > 256 {
		return fmt.Errorf("web.threads must be between 2 and 256")
	}
	if c.Web.ReadHeaderTimeout <= 0 || c.Web.IdleTimeout <= 0 || c.Runtime.DrainTimeout <= 0 {
		return fmt.Errorf("timeouts must be greater than zero")
	}
	if c.Web.MaxHeaderBytes < 1024 || c.Web.MaxHeaderBytes > 1024*1024 {
		return fmt.Errorf("web.maxHeaderBytes must be between 1024 and 1048576")
	}
	if c.Runtime.ControlSocket == "" || !filepath.IsAbs(c.Runtime.ControlSocket) {
		return fmt.Errorf("runtime.controlSocket must be an absolute path")
	}
	if c.Runtime.PIDFile == "" || !filepath.IsAbs(c.Runtime.PIDFile) {
		return fmt.Errorf("runtime.pidFile must be an absolute path")
	}
	for name, path := range map[string]string{"auditLog": c.Runtime.AuditLog, "logPath": c.Runtime.LogPath} {
		if path != "" && !filepath.IsAbs(path) {
			return fmt.Errorf("runtime.%s must be an absolute path", name)
		}
	}
	if c.Web.AccessLog != "" && !filepath.IsAbs(c.Web.AccessLog) {
		return fmt.Errorf("web.accessLog must be an absolute path")
	}
	if c.Runtime.LogMaxBytes < 0 || c.Runtime.LogMaxBackups < 0 {
		return fmt.Errorf("runtime.logMaxBytes and runtime.logMaxBackups must not be negative")
	}
	for name, value := range map[string]string{"nodeID": c.Runtime.NodeID, "clusterID": c.Runtime.ClusterID} {
		if value != "" && !validIdentity(value) {
			return fmt.Errorf("runtime.%s contains unsupported characters", name)
		}
	}
	if c.Queue.Enabled {
		if c.Queue.BridgeBaseURL == "" {
			return fmt.Errorf("queue.bridgeBaseURL is required when queue is enabled")
		}
		if len(c.Queue.Workers) == 0 {
			return fmt.Errorf("queue.workers must not be empty when queue is enabled")
		}
		for _, worker := range c.Queue.Workers {
			if worker.Name == "" || worker.Concurrency < 1 {
				return fmt.Errorf("queue workers require a name and positive concurrency")
			}
		}
		if c.Queue.ClaimBatch < 1 || c.Queue.LeaseSeconds < 1 || c.Queue.ReapBatch < 1 {
			return fmt.Errorf("queue batch sizes and lease must be positive")
		}
		if c.Queue.RequestTimeout <= 0 || c.Queue.DrainTimeout <= 0 {
			return fmt.Errorf("queue timeouts must be positive")
		}
	}
	if c.Observability.Enabled {
		if c.Observability.DatasetRoot == "" || !filepath.IsAbs(c.Observability.DatasetRoot) {
			return fmt.Errorf("observability.datasetRoot must be an absolute path when enabled")
		}
		if c.Observability.SpoolPath == "" || !filepath.IsAbs(c.Observability.SpoolPath) {
			return fmt.Errorf("observability.spoolPath must be an absolute path when enabled")
		}
		if c.Observability.MaxSpoolBytes <= 0 || c.Observability.MaxBatchRows <= 0 || c.Observability.MaxBatchBytes <= 0 {
			return fmt.Errorf("observability limits must be positive")
		}
		if c.Observability.FlushInterval <= 0 || c.Observability.MetricsDays <= 0 || c.Observability.LogDays <= 0 {
			return fmt.Errorf("observability intervals and retention must be positive")
		}
	}
	return nil
}

func validIdentity(value string) bool {
	if len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func RestartRequired(current, candidate Config) bool {
	return current.Web.Root != candidate.Web.Root ||
		current.Web.Listen != candidate.Web.Listen ||
		current.Web.Threads != candidate.Web.Threads ||
		current.Runtime.ControlSocket != candidate.Runtime.ControlSocket ||
		current.Runtime.PIDFile != candidate.Runtime.PIDFile ||
		current.Runtime.AuditLog != candidate.Runtime.AuditLog ||
		current.Runtime.LogPath != candidate.Runtime.LogPath ||
		current.Runtime.LogMaxBytes != candidate.Runtime.LogMaxBytes ||
		current.Runtime.LogMaxBackups != candidate.Runtime.LogMaxBackups ||
		current.Runtime.NodeID != candidate.Runtime.NodeID ||
		current.Runtime.ClusterID != candidate.Runtime.ClusterID ||
		current.Web.AccessLog != candidate.Web.AccessLog ||
		current.Queue.Enabled != candidate.Queue.Enabled ||
		current.Queue.BridgeBaseURL != candidate.Queue.BridgeBaseURL ||
		current.Queue.BridgeToken != candidate.Queue.BridgeToken ||
		current.Queue.RequestTimeout != candidate.Queue.RequestTimeout ||
		current.Queue.ClaimBatch != candidate.Queue.ClaimBatch ||
		current.Queue.LeaseSeconds != candidate.Queue.LeaseSeconds ||
		current.Queue.DrainTimeout != candidate.Queue.DrainTimeout ||
		current.Observability.Enabled != candidate.Observability.Enabled ||
		current.Observability.DatasetRoot != candidate.Observability.DatasetRoot ||
		current.Observability.SpoolPath != candidate.Observability.SpoolPath ||
		current.Observability.FlushInterval != candidate.Observability.FlushInterval
}
