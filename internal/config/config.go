// Package config loads and validates the versioned Runtime configuration.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const SchemaVersion = 1

type Config struct {
	SchemaVersion int           `json:"schemaVersion" toml:"schemaVersion"`
	Runtime       Runtime       `json:"runtime" toml:"runtime"`
	Web           Web           `json:"web" toml:"web"`
	Apps          []App         `json:"apps,omitempty" toml:"apps,omitempty"`
	Queue         Queue         `json:"queue,omitempty" toml:"queue,omitempty"`
	Observability Observability `json:"observability,omitempty" toml:"observability,omitempty"`

	// installRoot remembers the Runtime installation directory so that
	// application defaults can be materialized without re-reading the
	// configuration file. It is never encoded or compared directly.
	installRoot string
	// legacyApp marks the application entry synthesized from the deprecated
	// web.root option so that root overrides keep working for old configs.
	legacyApp bool
}

type Runtime struct {
	ControlSocket string        `json:"controlSocket" toml:"controlSocket"`
	PIDFile       string        `json:"pidFile" toml:"pidFile"`
	DrainTimeout  time.Duration `json:"drainTimeout" toml:"drainTimeout"`
	AuditLog      string        `json:"auditLog,omitempty" toml:"auditLog,omitempty"`
	LogLevel      string        `json:"logLevel,omitempty" toml:"logLevel,omitempty"`
	LogPath       string        `json:"logPath,omitempty" toml:"logPath,omitempty"`
	LogMaxBytes   int64         `json:"logMaxBytes,omitempty" toml:"logMaxBytes,omitempty"`
	LogMaxBackups int           `json:"logMaxBackups,omitempty" toml:"logMaxBackups,omitempty"`
	// PHPErrorLogMaxBytes/Backups control Runtime-side copytruncate
	// rotation of logs/php-error.log (written by the embedded PHP
	// error_log, which cannot be signalled to reopen the file). The
	// day-based limit reuses observability.jsonlKeepDays.
	PHPErrorLogMaxBytes   int64 `json:"phpErrorLogMaxBytes,omitempty" toml:"phpErrorLogMaxBytes,omitempty"`
	PHPErrorLogMaxBackups int   `json:"phpErrorLogMaxBackups,omitempty" toml:"phpErrorLogMaxBackups,omitempty"`
	// AuditLogMaxBytes/Backups/KeepDays control the built-in audit rotation.
	// When AuditLogKeepDays is positive it takes precedence over
	// AuditLogMaxBackups, matching the Runtime JSONL retention semantics.
	AuditLogMaxBytes   int64  `json:"auditLogMaxBytes,omitempty" toml:"auditLogMaxBytes,omitempty"`
	AuditLogMaxBackups int    `json:"auditLogMaxBackups,omitempty" toml:"auditLogMaxBackups,omitempty"`
	AuditLogKeepDays   int    `json:"auditLogKeepDays,omitempty" toml:"auditLogKeepDays,omitempty"`
	NodeID             string `json:"nodeID,omitempty" toml:"nodeID,omitempty"`
	ClusterID          string `json:"clusterID,omitempty" toml:"clusterID,omitempty"`
}

type Web struct {
	Root              string        `json:"root" toml:"root"`
	Listen            string        `json:"listen" toml:"listen"`
	Threads           int           `json:"threads" toml:"threads"`
	ReadHeaderTimeout time.Duration `json:"readHeaderTimeout" toml:"readHeaderTimeout"`
	IdleTimeout       time.Duration `json:"idleTimeout" toml:"idleTimeout"`
	MaxHeaderBytes    int           `json:"maxHeaderBytes" toml:"maxHeaderBytes"`
	AccessLog         string        `json:"accessLog,omitempty" toml:"accessLog,omitempty"` // deprecated: runtime now derives per-node access log paths
}

// App declares one pluggable web application. A Runtime instance can host
// several applications on the same listener, distinguished by URL prefix.
// Application code, data, session and temp roots are kept outside the
// Runtime artifact so that applications can be replaced independently.
type App struct {
	ID      string            `json:"id" toml:"id"`
	Path    string            `json:"path" toml:"path"`
	Dir     string            `json:"dir" toml:"dir"`
	WebRoot string            `json:"webRoot,omitempty" toml:"webRoot,omitempty"`
	DataDir string            `json:"dataDir,omitempty" toml:"dataDir,omitempty"`
	TmpDir  string            `json:"tmpDir,omitempty" toml:"tmpDir,omitempty"`
	Env     map[string]string `json:"env,omitempty" toml:"env,omitempty"`
	Enabled bool              `json:"enabled" toml:"enabled"`
	Queue   AppQueue          `json:"queue,omitempty" toml:"queue,omitempty"`
}

// DefaultAppQueueBridgeURLTemplate keeps applications that still expose the
// bridge through their normal router (PATH_INFO request mode) working when an
// application enables queue without an explicit template.
const DefaultAppQueueBridgeURLTemplate = "/index.php/queue-{endpoint}"

// AppQueue declares how the Runtime reaches one application's private Queue
// Bridge. The template is an in-application URL path containing exactly one
// {endpoint} placeholder, for example:
//
//	queue = { enabled = true, bridgeUrlTemplate = "/bridge.php?endpoint={endpoint}" }
//
// Token is optional: when empty the Runtime generates one per start and
// injects it as ZENTAO_QUEUE_TOKEN into that application only.
type AppQueue struct {
	Enabled           bool   `json:"enabled" toml:"enabled"`
	BridgeURLTemplate string `json:"bridgeUrlTemplate,omitempty" toml:"bridgeUrlTemplate,omitempty"`
	Token             string `json:"token,omitempty" toml:"token,omitempty"`
}

// DocumentRoot returns the absolute document root of the application.
func (a App) DocumentRoot() string {
	if a.WebRoot == "" {
		return a.Dir
	}
	return filepath.Join(a.Dir, a.WebRoot)
}

// ObservabilityDir returns the per-application observability root that holds
// the application's Parquet datasets (logs/metrics) and, in file mode, the
// raw JSONL logs (raw-logs/).
func (a App) ObservabilityDir() string {
	return filepath.Join(a.DataDir, "observability")
}

type Queue struct {
	Enabled           bool          `json:"enabled" toml:"enabled"`
	BridgeBaseURL     string        `json:"bridgeBaseURL" toml:"bridgeBaseURL"`
	BridgeToken       string        `json:"bridgeToken,omitempty" toml:"bridgeToken,omitempty"`
	RequestTimeout    time.Duration `json:"requestTimeout" toml:"requestTimeout"`
	ClaimBatch        int           `json:"claimBatch" toml:"claimBatch"`
	LeaseSeconds      int           `json:"leaseSeconds" toml:"leaseSeconds"`
	HeartbeatInterval time.Duration `json:"heartbeatInterval" toml:"heartbeatInterval"`
	ReapInterval      time.Duration `json:"reapInterval" toml:"reapInterval"`
	ReapBatch         int           `json:"reapBatch" toml:"reapBatch"`
	DrainTimeout      time.Duration `json:"drainTimeout" toml:"drainTimeout"`
	Workers           []QueueWorker `json:"workers" toml:"workers"`
}

type QueueWorker struct {
	Name        string        `json:"name" toml:"name"`
	Concurrency int           `json:"concurrency" toml:"concurrency"`
	MinPoll     time.Duration `json:"minPoll" toml:"minPoll"`
	MaxPoll     time.Duration `json:"maxPoll" toml:"maxPoll"`
}

type Observability struct {
	Enabled                 bool          `json:"enabled" toml:"enabled"`
	BridgeListen            string        `json:"bridgeListen,omitempty" toml:"bridgeListen,omitempty"`
	DatasetRoot             string        `json:"datasetRoot" toml:"datasetRoot"`
	SpoolPath               string        `json:"spoolPath" toml:"spoolPath"`
	MaxSpoolBytes           int64         `json:"maxSpoolBytes" toml:"maxSpoolBytes"`
	MaxBatchRows            int           `json:"maxBatchRows" toml:"maxBatchRows"`
	MaxBatchBytes           int64         `json:"maxBatchBytes" toml:"maxBatchBytes"`
	MinBatchRows            int           `json:"minBatchRows,omitempty" toml:"minBatchRows,omitempty"`
	MinBatchBytes           int64         `json:"minBatchBytes,omitempty" toml:"minBatchBytes,omitempty"`
	FlushInterval           time.Duration `json:"flushInterval" toml:"flushInterval"`
	MaxFlushInterval        time.Duration `json:"maxFlushInterval,omitempty" toml:"maxFlushInterval,omitempty"`
	MetricsDays             int           `json:"metricsDays" toml:"metricsDays"`
	LogDays                 int           `json:"logDays" toml:"logDays"`
	JSONLConvertInterval    time.Duration `json:"jsonlConvertInterval,omitempty" toml:"jsonlConvertInterval,omitempty"`
	JSONLConvertSources     []string      `json:"jsonlConvertSources,omitempty" toml:"jsonlConvertSources,omitempty"`
	JSONLKeepDays           int           `json:"jsonlKeepDays,omitempty" toml:"jsonlKeepDays,omitempty"`
	JSONLDeleteAfterConvert bool          `json:"jsonlDeleteAfterConvert,omitempty" toml:"jsonlDeleteAfterConvert,omitempty"`
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
			ControlSocket:         defaultControlSocket(),
			PIDFile:               defaultPIDFile(),
			DrainTimeout:          30 * time.Second,
			LogLevel:              "info",
			LogPath:               "logs/runtime.log",
			LogMaxBytes:           16 * 1024 * 1024,
			LogMaxBackups:         5,
			PHPErrorLogMaxBytes:   16 * 1024 * 1024,
			PHPErrorLogMaxBackups: 5,
			AuditLogMaxBytes:      16 * 1024 * 1024,
			AuditLogMaxBackups:    5,
		},
		Web: Web{
			Root:              "app/zentaopms/www",
			Listen:            "0.0.0.0:8080",
			Threads:           4,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       30 * time.Second,
			MaxHeaderBytes:    16 * 1024,
		},
		Queue: Queue{
			Enabled:           true,
			BridgeBaseURL:     "http://127.0.0.1:8081/internal/runtime/queue/v1",
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
			// Observability (flush mode) is enabled by default for Runtime
			// deployments; set enabled = false to keep file-mode logs instead.
			Enabled:                 true,
			BridgeListen:            "127.0.0.1:8082",
			DatasetRoot:             "data/observability",
			SpoolPath:               "spool/observability",
			MaxSpoolBytes:           1024 * 1024 * 1024,
			MaxBatchRows:            10000,
			MaxBatchBytes:           16 * 1024 * 1024,
			MinBatchRows:            1000,
			MinBatchBytes:           1024 * 1024,
			FlushInterval:           60 * time.Second,
			MaxFlushInterval:        10 * time.Minute,
			MetricsDays:             30,
			LogDays:                 30,
			JSONLConvertInterval:    time.Hour,
			JSONLConvertSources:     []string{"access", "runtime"},
			JSONLKeepDays:           7,
			JSONLDeleteAfterConvert: true,
		},
	}
}

// DefaultForRoot returns the default configuration rooted at the Runtime
// installation directory. Paths in TOML can override these values, while
// omitted fields continue to use this installation-local layout.
func DefaultForRoot(root string) Config {
	config := Default()
	config.installRoot = root
	if root == "" {
		return config
	}
	config.Runtime.ControlSocket = filepath.Join(root, "run", "runtime.sock")
	config.Runtime.PIDFile = filepath.Join(root, "run", "runtime.pid")
	config.Runtime.LogPath = filepath.Join(root, "logs", "runtime.log")
	config.Web.Root = filepath.Join(root, "app", "zentaopms", "www")
	config.Observability.DatasetRoot = filepath.Join(root, "data", "observability")
	config.Observability.SpoolPath = filepath.Join(root, "spool", "observability")
	return config
}

func Load(path string, environ []string) (Config, error) {
	configRoot, err := filepath.Abs(filepath.Dir(filepath.Dir(path)))
	if err != nil {
		return Config{}, fmt.Errorf("resolve runtime configuration root: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("read runtime configuration: %w", err)
	}

	config := DefaultForRoot(configRoot)
	if err == nil {
		config, err = decode(data, config)
		if err != nil {
			return Config{}, err
		}
	}
	if err := config.normalizeApps(); err != nil {
		return Config{}, err
	}
	if err := config.resolvePaths(configRoot); err != nil {
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

// resolvePaths expands paths relative to the installation root. Keeping this
// normalization at the configuration boundary lets the rest of Runtime use
// absolute paths without exposing the installation prefix in TOML.
func (c *Config) resolvePaths(root string) error {
	if root == "" {
		return fmt.Errorf("runtime configuration root is empty")
	}
	c.installRoot = root
	resolve := func(path *string) {
		if *path == "" || filepath.IsAbs(*path) || strings.HasPrefix(*path, `\\`) {
			return
		}
		*path = filepath.Join(root, *path)
	}
	resolve(&c.Runtime.ControlSocket)
	resolve(&c.Runtime.PIDFile)
	resolve(&c.Runtime.AuditLog)
	resolve(&c.Runtime.LogPath)
	resolve(&c.Web.Root)
	resolve(&c.Web.AccessLog)
	resolve(&c.Observability.DatasetRoot)
	resolve(&c.Observability.SpoolPath)
	for index := range c.Apps {
		app := &c.Apps[index]
		if app.WebRoot == "" {
			app.WebRoot = "www"
		}
		if app.DataDir == "" {
			app.DataDir = filepath.Join("data", "apps", app.ID)
		}
		if app.TmpDir == "" {
			app.TmpDir = filepath.Join("tmp-local", app.ID)
		}
		resolve(&app.Dir)
		resolve(&app.DataDir)
		resolve(&app.TmpDir)
	}
	return nil
}

// normalizeApps materializes the deprecated single-application web.root into
// an explicit application entry so that the rest of the Runtime only deals
// with one representation. When [[apps]] is present web.root must stay empty:
// mixing both would silently create an undeclared application.
func (c *Config) normalizeApps() error {
	for index := range c.Apps {
		if c.Apps[index].Queue.Enabled && c.Apps[index].Queue.BridgeURLTemplate == "" {
			c.Apps[index].Queue.BridgeURLTemplate = DefaultAppQueueBridgeURLTemplate
		}
	}
	if len(c.Apps) > 0 {
		return nil
	}
	if c.Web.Root == "" {
		return nil
	}
	app := App{
		ID:      "zentao",
		Path:    "/",
		Dir:     filepath.Dir(c.Web.Root),
		WebRoot: filepath.Base(c.Web.Root),
		Enabled: true,
	}
	if c.installRoot != "" {
		app.DataDir = filepath.Join(c.installRoot, "data", "apps", app.ID)
		app.TmpDir = filepath.Join(c.installRoot, "tmp-local", app.ID)
	}
	c.Apps = []App{app}
	c.legacyApp = true
	return nil
}

// LegacyApp reports whether the configuration uses the deprecated single
// application web.root form instead of explicit [[apps]] entries.
func (c Config) LegacyApp() bool { return c.legacyApp }

func decode(data []byte, config Config) (Config, error) {
	type rawRuntime struct {
		ControlSocket         *string `toml:"controlSocket"`
		PIDFile               *string `toml:"pidFile"`
		DrainTimeout          *string `toml:"drainTimeout"`
		AuditLog              *string `toml:"auditLog"`
		LogLevel              *string `toml:"logLevel"`
		LogPath               *string `toml:"logPath"`
		LogMaxBytes           *int64  `toml:"logMaxBytes"`
		LogMaxBackups         *int    `toml:"logMaxBackups"`
		PHPErrorLogMaxBytes   *int64  `toml:"phpErrorLogMaxBytes"`
		PHPErrorLogMaxBackups *int    `toml:"phpErrorLogMaxBackups"`
		AuditLogMaxBytes      *int64  `toml:"auditLogMaxBytes"`
		AuditLogMaxBackups    *int    `toml:"auditLogMaxBackups"`
		AuditLogKeepDays      *int    `toml:"auditLogKeepDays"`
		NodeID                *string `toml:"nodeID"`
		ClusterID             *string `toml:"clusterID"`
	}
	type rawWeb struct {
		Root              *string `toml:"root"`
		Listen            *string `toml:"listen"`
		Threads           *int    `toml:"threads"`
		ReadHeaderTimeout *string `toml:"readHeaderTimeout"`
		IdleTimeout       *string `toml:"idleTimeout"`
		MaxHeaderBytes    *int    `toml:"maxHeaderBytes"`
		AccessLog         *string `toml:"accessLog"`
	}
	type rawQueueWorker struct {
		Name        *string `toml:"name"`
		Concurrency *int    `toml:"concurrency"`
		MinPoll     *string `toml:"minPoll"`
		MaxPoll     *string `toml:"maxPoll"`
	}
	type rawQueue struct {
		Enabled           *bool             `toml:"enabled"`
		BridgeBaseURL     *string           `toml:"bridgeBaseURL"`
		BridgeToken       *string           `toml:"bridgeToken"`
		RequestTimeout    *string           `toml:"requestTimeout"`
		ClaimBatch        *int              `toml:"claimBatch"`
		LeaseSeconds      *int              `toml:"leaseSeconds"`
		HeartbeatInterval *string           `toml:"heartbeatInterval"`
		ReapInterval      *string           `toml:"reapInterval"`
		ReapBatch         *int              `toml:"reapBatch"`
		DrainTimeout      *string           `toml:"drainTimeout"`
		Workers           *[]rawQueueWorker `toml:"workers"`
	}
	type rawObservability struct {
		Enabled                 *bool    `toml:"enabled"`
		BridgeListen            *string  `toml:"bridgeListen"`
		DatasetRoot             *string  `toml:"datasetRoot"`
		SpoolPath               *string  `toml:"spoolPath"`
		MaxSpoolBytes           *int64   `toml:"maxSpoolBytes"`
		MaxBatchRows            *int     `toml:"maxBatchRows"`
		MaxBatchBytes           *int64   `toml:"maxBatchBytes"`
		MinBatchRows            *int     `toml:"minBatchRows"`
		MinBatchBytes           *int64   `toml:"minBatchBytes"`
		FlushInterval           *string  `toml:"flushInterval"`
		MaxFlushInterval        *string  `toml:"maxFlushInterval"`
		MetricsDays             *int     `toml:"metricsDays"`
		LogDays                 *int     `toml:"logDays"`
		JSONLConvertInterval    *string  `toml:"jsonlConvertInterval"`
		JSONLConvertSources     []string `toml:"jsonlConvertSources"`
		JSONLKeepDays           *int     `toml:"jsonlKeepDays"`
		JSONLDeleteAfterConvert *bool    `toml:"jsonlDeleteAfterConvert"`
	}
	type rawAppQueue struct {
		Enabled           *bool   `toml:"enabled"`
		BridgeURLTemplate *string `toml:"bridgeUrlTemplate"`
		Token             *string `toml:"token"`
	}
	type rawApp struct {
		ID      *string           `toml:"id"`
		Path    *string           `toml:"path"`
		Dir     *string           `toml:"dir"`
		WebRoot *string           `toml:"webRoot"`
		DataDir *string           `toml:"dataDir"`
		TmpDir  *string           `toml:"tmpDir"`
		Env     map[string]string `toml:"env"`
		Enabled *bool             `toml:"enabled"`
		Queue   *rawAppQueue      `toml:"queue"`
	}
	type rawConfig struct {
		SchemaVersion *int              `toml:"schemaVersion"`
		Runtime       *rawRuntime       `toml:"runtime"`
		Web           *rawWeb           `toml:"web"`
		Apps          *[]rawApp         `toml:"apps"`
		Queue         *rawQueue         `toml:"queue"`
		Observability *rawObservability `toml:"observability"`
	}

	var raw rawConfig
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode runtime configuration: %w", err)
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
		if raw.Runtime.LogLevel != nil {
			config.Runtime.LogLevel = *raw.Runtime.LogLevel
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
		if raw.Runtime.PHPErrorLogMaxBytes != nil {
			config.Runtime.PHPErrorLogMaxBytes = *raw.Runtime.PHPErrorLogMaxBytes
		}
		if raw.Runtime.PHPErrorLogMaxBackups != nil {
			config.Runtime.PHPErrorLogMaxBackups = *raw.Runtime.PHPErrorLogMaxBackups
		}
		if raw.Runtime.AuditLogMaxBytes != nil {
			config.Runtime.AuditLogMaxBytes = *raw.Runtime.AuditLogMaxBytes
		}
		if raw.Runtime.AuditLogMaxBackups != nil {
			config.Runtime.AuditLogMaxBackups = *raw.Runtime.AuditLogMaxBackups
		}
		if raw.Runtime.AuditLogKeepDays != nil {
			config.Runtime.AuditLogKeepDays = *raw.Runtime.AuditLogKeepDays
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
	if raw.Apps != nil {
		if raw.Web != nil && raw.Web.Root != nil {
			return Config{}, fmt.Errorf("web.root cannot be combined with [[apps]]; configure apps[].dir instead")
		}
		// The installation-local legacy default must not silently become an
		// extra application once [[apps]] is present.
		config.Web.Root = ""
		apps := make([]App, 0, len(*raw.Apps))
		for index, item := range *raw.Apps {
			app := App{Enabled: true}
			if item.ID != nil {
				app.ID = *item.ID
			}
			if item.Path != nil {
				app.Path = *item.Path
			}
			if item.Dir != nil {
				app.Dir = *item.Dir
			}
			if item.WebRoot != nil {
				app.WebRoot = *item.WebRoot
			}
			if item.DataDir != nil {
				app.DataDir = *item.DataDir
			}
			if item.TmpDir != nil {
				app.TmpDir = *item.TmpDir
			}
			if item.Env != nil {
				app.Env = item.Env
			}
			if item.Enabled != nil {
				app.Enabled = *item.Enabled
			}
			if item.Queue != nil {
				if item.Queue.Enabled != nil {
					app.Queue.Enabled = *item.Queue.Enabled
				}
				if item.Queue.BridgeURLTemplate != nil {
					app.Queue.BridgeURLTemplate = *item.Queue.BridgeURLTemplate
				}
				if item.Queue.Token != nil {
					app.Queue.Token = *item.Queue.Token
				}
			}
			if app.ID == "" {
				return Config{}, fmt.Errorf("apps[%d].id is required", index)
			}
			apps = append(apps, app)
		}
		config.Apps = apps
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
		if raw.Observability.BridgeListen != nil {
			config.Observability.BridgeListen = *raw.Observability.BridgeListen
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
		if raw.Observability.MinBatchRows != nil {
			config.Observability.MinBatchRows = *raw.Observability.MinBatchRows
		}
		if raw.Observability.MinBatchBytes != nil {
			config.Observability.MinBatchBytes = *raw.Observability.MinBatchBytes
		}
		if raw.Observability.FlushInterval != nil {
			value, err := time.ParseDuration(*raw.Observability.FlushInterval)
			if err != nil {
				return Config{}, fmt.Errorf("parse observability.flushInterval: %w", err)
			}
			config.Observability.FlushInterval = value
		}
		if raw.Observability.MaxFlushInterval != nil {
			value, err := time.ParseDuration(*raw.Observability.MaxFlushInterval)
			if err != nil {
				return Config{}, fmt.Errorf("parse observability.maxFlushInterval: %w", err)
			}
			config.Observability.MaxFlushInterval = value
		}
		if raw.Observability.MetricsDays != nil {
			config.Observability.MetricsDays = *raw.Observability.MetricsDays
		}
		if raw.Observability.LogDays != nil {
			config.Observability.LogDays = *raw.Observability.LogDays
		}
		if raw.Observability.JSONLConvertInterval != nil {
			value, err := time.ParseDuration(*raw.Observability.JSONLConvertInterval)
			if err != nil {
				return Config{}, fmt.Errorf("parse observability.jsonlConvertInterval: %w", err)
			}
			config.Observability.JSONLConvertInterval = value
		}
		if raw.Observability.JSONLConvertSources != nil {
			config.Observability.JSONLConvertSources = raw.Observability.JSONLConvertSources
		}
		if raw.Observability.JSONLKeepDays != nil {
			config.Observability.JSONLKeepDays = *raw.Observability.JSONLKeepDays
		}
		if raw.Observability.JSONLDeleteAfterConvert != nil {
			config.Observability.JSONLDeleteAfterConvert = *raw.Observability.JSONLDeleteAfterConvert
		}
	}
	return config, nil
}

func (c Config) Apply(overrides Overrides) (Config, error) {
	if overrides.Root != nil {
		if len(c.Apps) > 0 && !c.legacyApp {
			return Config{}, fmt.Errorf("web.root/ZENTAO_RUNTIME_ROOT cannot be combined with [[apps]]; configure apps[].dir instead")
		}
		c.Web.Root = *overrides.Root
		if c.legacyApp {
			// Re-synthesize the legacy application from the new root.
			c.Apps = nil
			c.legacyApp = false
		}
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
	if err := c.normalizeApps(); err != nil {
		return Config{}, err
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
	if c.Web.Root != "" && !filepath.IsAbs(c.Web.Root) {
		return fmt.Errorf("web.root must be an absolute path")
	}
	if err := c.validateApps(); err != nil {
		return err
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
	if c.Runtime.PHPErrorLogMaxBytes < 0 || c.Runtime.PHPErrorLogMaxBackups < 0 {
		return fmt.Errorf("runtime.phpErrorLogMaxBytes and runtime.phpErrorLogMaxBackups must not be negative")
	}
	switch c.Runtime.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("runtime.logLevel must be one of debug, info, warn or error")
	}
	if c.Runtime.AuditLogMaxBytes < 0 || c.Runtime.AuditLogMaxBackups < 0 || c.Runtime.AuditLogKeepDays < 0 {
		return fmt.Errorf("runtime audit log limits must not be negative")
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
		bridgeURL, err := url.Parse(c.Queue.BridgeBaseURL)
		if err != nil || bridgeURL.Scheme != "http" || bridgeURL.User != nil {
			return fmt.Errorf("queue.bridgeBaseURL must be an unauthenticated http URL")
		}
		host := bridgeURL.Hostname()
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return fmt.Errorf("queue.bridgeBaseURL must use a loopback address")
		}
		port, err := strconv.Atoi(bridgeURL.Port())
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("queue.bridgeBaseURL must include a valid port")
		}
		if len(c.Queue.Workers) == 0 {
			return fmt.Errorf("queue.workers must not be empty when queue is enabled")
		}
		workerNames := make(map[string]struct{}, len(c.Queue.Workers))
		for _, worker := range c.Queue.Workers {
			if worker.Name == "" || worker.Concurrency < 1 {
				return fmt.Errorf("queue workers require a name and positive concurrency")
			}
			if _, exists := workerNames[worker.Name]; exists {
				return fmt.Errorf("queue worker %q is configured more than once", worker.Name)
			}
			workerNames[worker.Name] = struct{}{}
		}
		if c.Queue.ClaimBatch < 1 || c.Queue.LeaseSeconds < 1 || c.Queue.ReapBatch < 1 {
			return fmt.Errorf("queue batch sizes and lease must be positive")
		}
		if c.Queue.RequestTimeout <= 0 || c.Queue.DrainTimeout <= 0 {
			return fmt.Errorf("queue timeouts must be positive")
		}
	}
	if c.Observability.Enabled {
		host, port, err := net.SplitHostPort(c.Observability.BridgeListen)
		if err != nil || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
			return fmt.Errorf("observability.bridgeListen must use a loopback address")
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return fmt.Errorf("observability.bridgeListen must include a valid port")
		}
		if c.Observability.DatasetRoot == "" || !filepath.IsAbs(c.Observability.DatasetRoot) {
			return fmt.Errorf("observability.datasetRoot must be an absolute path when enabled")
		}
		if c.Observability.SpoolPath == "" || !filepath.IsAbs(c.Observability.SpoolPath) {
			return fmt.Errorf("observability.spoolPath must be an absolute path when enabled")
		}
		if c.Observability.MaxSpoolBytes <= 0 || c.Observability.MaxBatchRows <= 0 || c.Observability.MaxBatchBytes <= 0 {
			return fmt.Errorf("observability limits must be positive")
		}
		if c.Observability.MinBatchRows < 0 || c.Observability.MinBatchBytes < 0 {
			return fmt.Errorf("observability minimum batch limits must not be negative")
		}
		if c.Observability.FlushInterval <= 0 || c.Observability.MetricsDays <= 0 || c.Observability.LogDays <= 0 {
			return fmt.Errorf("observability intervals and retention must be positive")
		}
		if c.Observability.MaxFlushInterval <= 0 {
			return fmt.Errorf("observability.maxFlushInterval must be positive")
		}
		if c.Observability.JSONLConvertInterval <= 0 || c.Observability.JSONLKeepDays <= 0 {
			return fmt.Errorf("observability jsonl convert interval and keep days must be positive")
		}
		for _, source := range c.Observability.JSONLConvertSources {
			if source != "access" && source != "runtime" {
				return fmt.Errorf("observability.jsonlConvertSources only supports access or runtime")
			}
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

// ActiveApps returns the applications that are enabled and therefore part of
// the generated web configuration.
func (c Config) ActiveApps() []App {
	apps := make([]App, 0, len(c.Apps))
	for _, app := range c.Apps {
		if app.Enabled {
			apps = append(apps, app)
		}
	}
	return apps
}

// NormalizePath returns the canonical URL prefix of an application: "/" or
// "/segment" without a trailing slash.
func NormalizePath(value string) (string, error) {
	path := strings.TrimSpace(value)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("path must start with /")
	}
	if strings.ContainsAny(path, "\\?#") || strings.Contains(path, "//") {
		return "", fmt.Errorf("path must be a plain URL prefix")
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		path = "/"
	}
	if path != "/" {
		for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
			if segment == "" || segment == "." || segment == ".." {
				return "", fmt.Errorf("path contains an invalid segment")
			}
		}
	}
	return path, nil
}

func (c Config) validateApps() error {
	seenIDs := make(map[string]struct{}, len(c.Apps))
	seenPaths := make(map[string]string, len(c.Apps))
	prefixes := make([]string, 0, len(c.Apps))
	for index, app := range c.Apps {
		where := fmt.Sprintf("apps[%d]", index)
		if app.ID == "" {
			return fmt.Errorf("%s.id is required", where)
		}
		if !validIdentity(app.ID) {
			return fmt.Errorf("%s.id contains unsupported characters", where)
		}
		if _, exists := seenIDs[app.ID]; exists {
			return fmt.Errorf("application id %q is configured more than once", app.ID)
		}
		seenIDs[app.ID] = struct{}{}

		prefix, err := NormalizePath(app.Path)
		if err != nil {
			return fmt.Errorf("%s.path: %w", where, err)
		}
		if prefix == "/_runtime" || strings.HasPrefix(prefix, "/_runtime/") {
			return fmt.Errorf("%s.path must not use the reserved /_runtime prefix", where)
		}
		if other, exists := seenPaths[prefix]; exists {
			return fmt.Errorf("application %q and %q use the same path %q", other, app.ID, prefix)
		}
		seenPaths[prefix] = app.ID
		prefixes = append(prefixes, prefix)

		if app.Dir == "" || !filepath.IsAbs(app.Dir) {
			return fmt.Errorf("%s.dir must be an absolute path", where)
		}
		if app.WebRoot == "" || filepath.IsAbs(app.WebRoot) || strings.Contains(app.WebRoot, "..") {
			return fmt.Errorf("%s.webRoot must be a relative path inside the application directory", where)
		}
		if app.DataDir == "" || !filepath.IsAbs(app.DataDir) {
			return fmt.Errorf("%s.dataDir must be an absolute path", where)
		}
		if app.TmpDir == "" || !filepath.IsAbs(app.TmpDir) {
			return fmt.Errorf("%s.tmpDir must be an absolute path", where)
		}
		for name := range app.Env {
			if name == "" || strings.ContainsAny(name, "=\x00") {
				return fmt.Errorf("%s.env contains an invalid variable name", where)
			}
		}
		if err := validateAppQueue(app.Queue); err != nil {
			return fmt.Errorf("%s.queue: %w", where, err)
		}
	}
	for _, prefix := range prefixes {
		if prefix == "/" {
			continue
		}
		for _, other := range prefixes {
			if other != "/" && other != prefix && strings.HasPrefix(other, prefix+"/") {
				return fmt.Errorf("application paths %q and %q must not be nested", prefix, other)
			}
		}
	}
	return nil
}

func validateAppQueue(queue AppQueue) error {
	if !queue.Enabled {
		return nil
	}
	template := queue.BridgeURLTemplate
	if template == "" {
		return fmt.Errorf("bridgeUrlTemplate is required when enabled")
	}
	if !strings.HasPrefix(template, "/") {
		return fmt.Errorf("bridgeUrlTemplate must start with /")
	}
	if strings.Contains(template, "://") || strings.Contains(template, "..") {
		return fmt.Errorf("bridgeUrlTemplate must be an in-application URL path")
	}
	if strings.ContainsAny(template, " \t\r\n") {
		return fmt.Errorf("bridgeUrlTemplate must not contain whitespace")
	}
	if strings.Count(template, "{endpoint}") != 1 {
		return fmt.Errorf("bridgeUrlTemplate must contain exactly one {endpoint} placeholder")
	}
	return nil
}

func equalApps(left, right []App) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID ||
			left[index].Path != right[index].Path ||
			left[index].Dir != right[index].Dir ||
			left[index].WebRoot != right[index].WebRoot ||
			left[index].DataDir != right[index].DataDir ||
			left[index].TmpDir != right[index].TmpDir ||
			left[index].Enabled != right[index].Enabled ||
			left[index].Queue != right[index].Queue {
			return false
		}
		if len(left[index].Env) != len(right[index].Env) {
			return false
		}
		for name, value := range left[index].Env {
			if right[index].Env[name] != value {
				return false
			}
		}
	}
	return true
}

func RestartRequired(current, candidate Config) bool {
	if candidate.Runtime.NodeID == "" {
		candidate.Runtime.NodeID = current.Runtime.NodeID
	}
	return current.Runtime.DrainTimeout != candidate.Runtime.DrainTimeout ||
		current.Web.Root != candidate.Web.Root ||
		current.Web.Listen != candidate.Web.Listen ||
		current.Web.Threads != candidate.Web.Threads ||
		!equalApps(current.Apps, candidate.Apps) ||
		current.Runtime.ControlSocket != candidate.Runtime.ControlSocket ||
		current.Runtime.PIDFile != candidate.Runtime.PIDFile ||
		current.Runtime.AuditLog != candidate.Runtime.AuditLog ||
		current.Runtime.LogLevel != candidate.Runtime.LogLevel ||
		current.Runtime.LogPath != candidate.Runtime.LogPath ||
		current.Runtime.LogMaxBytes != candidate.Runtime.LogMaxBytes ||
		current.Runtime.LogMaxBackups != candidate.Runtime.LogMaxBackups ||
		current.Runtime.PHPErrorLogMaxBytes != candidate.Runtime.PHPErrorLogMaxBytes ||
		current.Runtime.PHPErrorLogMaxBackups != candidate.Runtime.PHPErrorLogMaxBackups ||
		current.Runtime.AuditLogMaxBytes != candidate.Runtime.AuditLogMaxBytes ||
		current.Runtime.AuditLogMaxBackups != candidate.Runtime.AuditLogMaxBackups ||
		current.Runtime.AuditLogKeepDays != candidate.Runtime.AuditLogKeepDays ||
		current.Runtime.NodeID != candidate.Runtime.NodeID ||
		current.Runtime.ClusterID != candidate.Runtime.ClusterID ||
		current.Queue.Enabled != candidate.Queue.Enabled ||
		current.Queue.BridgeBaseURL != candidate.Queue.BridgeBaseURL ||
		current.Queue.BridgeToken != candidate.Queue.BridgeToken ||
		current.Queue.RequestTimeout != candidate.Queue.RequestTimeout ||
		current.Queue.ClaimBatch != candidate.Queue.ClaimBatch ||
		current.Queue.LeaseSeconds != candidate.Queue.LeaseSeconds ||
		current.Queue.HeartbeatInterval != candidate.Queue.HeartbeatInterval ||
		current.Queue.ReapInterval != candidate.Queue.ReapInterval ||
		current.Queue.ReapBatch != candidate.Queue.ReapBatch ||
		current.Queue.DrainTimeout != candidate.Queue.DrainTimeout ||
		!equalWorkers(current.Queue.Workers, candidate.Queue.Workers) ||
		current.Observability.Enabled != candidate.Observability.Enabled ||
		current.Observability.BridgeListen != candidate.Observability.BridgeListen ||
		current.Observability.DatasetRoot != candidate.Observability.DatasetRoot ||
		current.Observability.SpoolPath != candidate.Observability.SpoolPath ||
		current.Observability.FlushInterval != candidate.Observability.FlushInterval ||
		current.Observability.MaxSpoolBytes != candidate.Observability.MaxSpoolBytes ||
		current.Observability.MaxBatchRows != candidate.Observability.MaxBatchRows ||
		current.Observability.MaxBatchBytes != candidate.Observability.MaxBatchBytes ||
		current.Observability.MinBatchRows != candidate.Observability.MinBatchRows ||
		current.Observability.MinBatchBytes != candidate.Observability.MinBatchBytes ||
		current.Observability.MaxFlushInterval != candidate.Observability.MaxFlushInterval ||
		current.Observability.MetricsDays != candidate.Observability.MetricsDays ||
		current.Observability.LogDays != candidate.Observability.LogDays ||
		current.Observability.JSONLConvertInterval != candidate.Observability.JSONLConvertInterval ||
		current.Observability.JSONLKeepDays != candidate.Observability.JSONLKeepDays ||
		current.Observability.JSONLDeleteAfterConvert != candidate.Observability.JSONLDeleteAfterConvert ||
		!equalStrings(current.Observability.JSONLConvertSources, candidate.Observability.JSONLConvertSources)
}

func equalWorkers(left, right []QueueWorker) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
