package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
	"github.com/dunglas/frankenphp"
	_ "github.com/dunglas/frankenphp/caddy"
	"github.com/vimrus/runtime/internal/config"
	"github.com/vimrus/runtime/internal/control"
	"github.com/vimrus/runtime/internal/diagnostics"
	"github.com/vimrus/runtime/internal/health"
	"github.com/vimrus/runtime/internal/lifecycle"
	"github.com/vimrus/runtime/internal/logging"
	"github.com/vimrus/runtime/internal/observability"
	"github.com/vimrus/runtime/internal/observability/duckdb"
	"github.com/vimrus/runtime/internal/observability/jsonl"
	"github.com/vimrus/runtime/internal/observability/query"
	"github.com/vimrus/runtime/internal/observability/retention"
	queueclient "github.com/vimrus/runtime/internal/queue/client"
	queueengine "github.com/vimrus/runtime/internal/queue/engine"
	"github.com/vimrus/runtime/internal/queue/worker"
	"github.com/vimrus/runtime/internal/upgrade"
	"github.com/vimrus/runtime/internal/web"
)

var (
	runtimeVersion    = "dev"
	frankenPHPVersion = "unknown"
	caddyVersion      = "unknown"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		var exitErr *exitError
		if errors.As(err, &exitErr) {
			fmt.Fprintln(os.Stderr, "zentao-runtime:", exitErr.message)
			os.Exit(exitErr.code)
		}
		fmt.Fprintln(os.Stderr, "zentao-runtime:", err)
		os.Exit(1)
	}
}

type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }

func usageError(format string, args ...any) error {
	return &exitError{code: 2, message: fmt.Sprintf(format, args...)}
}

func notRunningError(format string, args ...any) error {
	return &exitError{code: 3, message: fmt.Sprintf(format, args...)}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError("usage: zentao-runtime <serve|start|status|stop|reload|health|diagnose|upgrade|logs|metrics|flush-observability|clean-observability|convert-jsonl|collect-logs|php-cli|version|run-service>")
	}
	switch args[0] {
	case "serve", "start":
		return serve(args[1:])
	case "run-service":
		return runServiceCommand(args[1:])
	case "status", "stop", "reload", "health", "diagnose":
		return controlCommand(args[0], args[1:])
	case "upgrade":
		return upgradeCommand(args[1:])
	case "logs", "metrics":
		return observabilityCommand(args[0], args[1:])
	case "php-cli":
		return phpCLICommand(args[1:])
	case "flush-observability":
		return flushObservabilityCommand(args[1:])
	case "clean-observability":
		return cleanObservabilityCommand(args[1:])
	case "convert-jsonl":
		return convertJSONLCommand(args[1:])
	case "collect-logs":
		return collectLogsCommand(args[1:])
	case "version":
		return printJSON(versionData())
	default:
		return usageError("unknown command %q", args[0])
	}
}

func convertJSONLCommand(args []string) error {
	flags := flag.NewFlagSet("convert-jsonl", flag.ContinueOnError)
	controlSocket := flags.String("control-socket", config.Default().Runtime.ControlSocket, "Runtime control socket")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	response, err := control.Call(context.Background(), *controlSocket, control.Request{Operation: "convert-jsonl"})
	if err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	return printRawJSON(response.Result)
}

func collectLogsCommand(args []string) error {
	flags := flag.NewFlagSet("collect-logs", flag.ContinueOnError)
	controlSocket := flags.String("control-socket", config.Default().Runtime.ControlSocket, "Runtime control socket")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	response, err := control.Call(context.Background(), *controlSocket, control.Request{Operation: "collect-logs"})
	if err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	return printRawJSON(response.Result)
}

// phpCLICommand runs the bundled PHP CLI with the same Runtime configuration,
// so Web and command-line execution always use the same PHP build, extensions
// and ionCube Loader.
func phpCLICommand(args []string) error {
	flags := flag.NewFlagSet("php-cli", flag.ContinueOnError)
	phpBin := flags.String("php", "", "PHP binary override")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	binary := *phpBin
	if binary == "" {
		binary = os.Getenv("ZENTAO_PHP_BIN")
	}
	if binary == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		dir := filepath.Dir(executable)
		candidates := []string{
			filepath.Join(dir, "php"),
			filepath.Join(dir, "php.exe"),
			"/opt/zentao/php/bin/php",
		}
		for _, candidate := range candidates {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				binary = candidate
				break
			}
		}
	}
	if binary == "" {
		return errors.New("PHP CLI binary not found; set ZENTAO_PHP_BIN or --php")
	}
	command := exec.Command(binary, flags.Args()...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &exitError{code: exitErr.ExitCode(), message: "php-cli exited with " + strconv.Itoa(exitErr.ExitCode())}
		}
		return err
	}
	return nil
}

func cleanObservabilityCommand(args []string) error {
	flags := flag.NewFlagSet("clean-observability", flag.ContinueOnError)
	controlSocket := flags.String("control-socket", config.Default().Runtime.ControlSocket, "Runtime control socket")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	response, err := control.Call(context.Background(), *controlSocket, control.Request{Operation: "observability-clean"})
	if err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	return printRawJSON(response.Result)
}

func flushObservabilityCommand(args []string) error {
	flags := flag.NewFlagSet("flush-observability", flag.ContinueOnError)
	controlSocket := flags.String("control-socket", config.Default().Runtime.ControlSocket, "Runtime control socket")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	response, err := control.Call(context.Background(), *controlSocket, control.Request{Operation: "observability-flush"})
	if err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	return printRawJSON(response.Result)
}

func observabilityCommand(operation string, args []string) error {
	flags := flag.NewFlagSet(operation, flag.ContinueOnError)
	controlSocket := flags.String("control-socket", config.Default().Runtime.ControlSocket, "Runtime control socket")
	since := flags.String("since", "30m", "Query start offset (Go duration)")
	until := flags.String("until", "", "Query end time (RFC3339, default now)")
	node := flags.String("node", "", "Node filter")
	level := flags.String("level", "", "Log level filter")
	metricName := flags.String("metric-name", "", "Metric name filter")
	limit := flags.Int("limit", 100, "Maximum rows")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	payload := map[string]any{"since": *since, "until": *until, "node": *node, "level": *level, "metricName": *metricName, "limit": *limit}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	response, err := control.Call(context.Background(), *controlSocket, control.Request{Operation: operation, Payload: data})
	if err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	return printRawJSON(response.Result)
}

func upgradeCommand(args []string) error {
	flags := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	controlSocket := flags.String("control-socket", config.Default().Runtime.ControlSocket, "Runtime control socket")
	action := flags.String("action", "status", "prepare|apply|rollback|status")
	release := flags.String("release", "", "Absolute release path for prepare")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	payload, err := json.Marshal(map[string]string{"release": *release})
	if err != nil {
		return err
	}
	response, err := control.Call(context.Background(), *controlSocket, control.Request{Operation: "upgrade", Action: *action, Payload: payload})
	if err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	return printRawJSON(response.Result)
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := flags.String("config", "/opt/zentao/config/runtime.json", "Runtime configuration file")
	root := flags.String("root", "", "PHP document root override")
	listen := flags.String("listen", "", "HTTP listen address override")
	pidFile := flags.String("pid-file", "", "PID file override")
	controlSocket := flags.String("control-socket", "", "Control socket override")
	threads := flags.Int("threads", 0, "PHP thread count override")
	installRoot := flags.String("install-root", "/opt/zentao", "Installation root for upgrade transactions")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}

	cfg, err := loadServeConfig(*configPath, *root, *listen, *pidFile, *controlSocket, *threads)
	if err != nil {
		return err
	}
	info, err := os.Stat(cfg.Web.Root)
	if err != nil {
		return fmt.Errorf("document root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("document root %q is not a directory", cfg.Web.Root)
	}

	nodeID, err := resolveNodeID(cfg, *installRoot)
	if err != nil {
		return err
	}
	cfg.Runtime.NodeID = nodeID
	logsDir := filepath.Dir(cfg.Runtime.LogPath)
	if cfg.Runtime.LogPath == "" {
		logsDir = "/opt/zentao/logs"
	}
	effectiveLogPath := filepath.Join(logsDir, "runtime-"+nodeID+".jsonl")
	effectiveAuditLog := cfg.Runtime.AuditLog
	if effectiveAuditLog == "" {
		effectiveAuditLog = filepath.Join(logsDir, "audit-"+nodeID+".jsonl")
	}
	effectiveAccessLog := filepath.Join(logsDir, "access-"+nodeID+".jsonl")

	logger, err := logging.New(logging.Options{
		Level:        slog.LevelInfo,
		OutputPath:   effectiveLogPath,
		MaxBytes:     cfg.Runtime.LogMaxBytes,
		MaxBackups:   cfg.Runtime.LogMaxBackups,
		RollInterval: cfg.Observability.JSONLConvertInterval,
		KeepDays:     cfg.Observability.JSONLKeepDays,
		NodeID:       nodeID,
		InstanceID:   instanceID(),
		Component:    "runtime",
	})
	if err != nil {
		return fmt.Errorf("initialize logging: %w", err)
	}
	defer logger.Close()

	auditor, err := control.NewFileAuditor(effectiveAuditLog)
	if err != nil {
		return fmt.Errorf("initialize audit log: %w", err)
	}
	defer auditor.Close()

	registry := newHealthRegistry(cfg)
	obsPublisher, err := newObservabilityPublisher(cfg, nodeID)
	if err != nil {
		return err
	}
	if obsPublisher != nil {
		registry.Register("observability", health.KindShared, false, true, func(ctx context.Context) health.Result {
			if err := obsPublisher.Healthy(); err != nil {
				return health.Result{Name: "observability", Kind: health.KindShared, Status: health.StatusFailed, Message: "observability spool is not writable"}
			}
			return health.Result{Name: "observability", Kind: health.KindShared, Status: health.StatusOK}
		})
	}
	queryEngine, err := newObservabilityQueryEngine(cfg)
	if err != nil {
		return err
	}
	queueEngine, err := newQueueEngine(cfg, logger, nodeID)
	if err != nil {
		return err
	}
	if queueEngine != nil {
		registry.Register("queue", health.KindDependency, false, false, func(context.Context) health.Result { return queueEngine.Health() })
	}
	upgradeController, err := upgrade.NewController(*installRoot)
	if err != nil {
		return fmt.Errorf("initialize upgrade controller: %w", err)
	}
	var jsonlConverter *jsonl.Converter
	if obsPublisher != nil {
		clusterID := cfg.Runtime.ClusterID
		if clusterID == "" {
			clusterID = "standalone"
		}
		jsonlConverter, err = jsonl.New(jsonl.Config{
			LogsDir:   logsDir,
			NodeID:    nodeID,
			ClusterID: clusterID,
			BootID:    instanceID(),
			Sources:   cfg.Observability.JSONLConvertSources,
			Publisher: obsPublisher,
			Interval:  cfg.Observability.JSONLConvertInterval,
		})
		if err != nil {
			return fmt.Errorf("initialize jsonl converter: %w", err)
		}
	}
	host := newHost(*configPath, cfg, logger, registry, auditor, upgradeController, queryEngine, obsPublisher, jsonlConverter, effectiveAccessLog, cfg.Observability.JSONLKeepDays)
	host.collector = &diagnostics.Collector{
		LogPaths: []string{
			effectiveLogPath,
			effectiveAuditLog,
			effectiveAccessLog,
			"/opt/zentao/logs/php-error.log",
		},
		OutputDir: logsDir,
		Summary: func() map[string]any {
			return map[string]any{"version": versionData(), "config": host.configSummary()}
		},
	}
	web.SetHealthProvider(hostStatusProvider{registry: registry, machine: host.machine})
	if err := host.machine.Transition(lifecycle.ConfigLoaded, "configuration validated"); err != nil {
		return err
	}
	if err := createPIDFile(cfg.Runtime.PIDFile); err != nil {
		return err
	}
	defer os.Remove(cfg.Runtime.PIDFile)
	if err := host.machine.Transition(lifecycle.DependenciesStarting, "no managed dependencies enabled"); err != nil {
		return err
	}
	if err := host.machine.Transition(lifecycle.CaddyStarting, "loading Caddy and FrankenPHP"); err != nil {
		return err
	}
	if err := caddy.Run(webConfig(cfg, effectiveAccessLog, cfg.Observability.JSONLKeepDays)); err != nil {
		host.machine.Fail("Caddy startup failed")
		return fmt.Errorf("start Caddy: %w", err)
	}
	controlServer, err := control.ListenWithOptions(cfg.Runtime.ControlSocket, host, control.Options{
		Authorizer: controlAuthorizer(),
		Auditor:    auditor,
	})
	if err != nil {
		_ = caddy.Stop()
		host.machine.Fail("control plane startup failed")
		return err
	}
	defer controlServer.Close()
	if err := host.machine.Transition(lifecycle.Ready, "Caddy and FrankenPHP started"); err != nil {
		_ = caddy.Stop()
		return err
	}
	logger.Info("runtime ready", slog.String("listen", cfg.Web.Listen), slog.String("root", cfg.Web.Root), slog.String("mode", "classic"))
	serveCtx, serveCancel := context.WithCancel(context.Background())
	defer serveCancel()
	go runLogPruneLoop(serveCtx, logger)
	if obsPublisher != nil {
		clusterID := cfg.Runtime.ClusterID
		if clusterID == "" {
			clusterID = "standalone"
		}
		_ = obsPublisher.Add(serveCtx, observability.Event{
			ClusterID: clusterID,
			NodeID:    nodeID,
			BootID:    instanceID(),
			Source:    "runtime",
			Kind:      observability.KindLogs,
			Level:     "info",
			Message:   "runtime started",
			EventTime: time.Now().UTC(),
		})
	}
	if queueEngine != nil {
		if err := queueEngine.Start(serveCtx); err != nil {
			logger.Warn("queue engine failed to start", slog.Any("error", err))
		} else {
			logger.Info("queue engine started")
		}
	}
	if obsPublisher != nil {
		go func() { _ = obsPublisher.Recover(serveCtx) }()
		go obsPublisher.Run(serveCtx)
		go runRetentionLoop(serveCtx, cfg, logger, nodeID)
		go jsonlConverter.Run(serveCtx)
		logger.Info("observability publisher started")
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case received := <-signals:
		host.requestStop("received " + received.String())
	case <-host.stop:
	}
	if err := host.machine.Transition(lifecycle.Draining, "graceful stop"); err != nil && host.machine.Snapshot().State != lifecycle.Draining {
		return err
	}
	if queueEngine != nil {
		if err := queueEngine.Stop(); err != nil {
			logger.Warn("queue engine drain failed", slog.Any("error", err))
		}
	}
	if obsPublisher != nil {
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := obsPublisher.FlushAll(flushCtx); err != nil {
			logger.Warn("observability final flush failed", slog.Any("error", err))
		}
		flushCancel()
	}
	serveCancel()
	if err := stopCaddyBounded(cfg.Runtime.DrainTimeout); err != nil {
		host.machine.Fail("Caddy shutdown timed out")
		return err
	}
	if err := host.machine.Transition(lifecycle.Stopped, "Caddy stopped"); err != nil {
		return err
	}
	logger.Info("runtime stopped")
	return nil
}

func runLogPruneLoop(ctx context.Context, logger *logging.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logger.Prune()
		}
	}
}

func newQueueEngine(cfg config.Config, logger *logging.Logger, nodeID string) (*queueengine.Engine, error) {
	if !cfg.Queue.Enabled {
		return nil, nil
	}
	if cfg.Queue.BridgeToken == "" {
		return nil, errors.New("queue is enabled but ZENTAO_QUEUE_TOKEN or queue.bridgeToken is missing")
	}
	bridgeClient, err := queueclient.New(queueclient.Config{
		BaseURL:        cfg.Queue.BridgeBaseURL,
		Token:          cfg.Queue.BridgeToken,
		RequestTimeout: cfg.Queue.RequestTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize queue bridge: %w", err)
	}
	workers := make([]worker.QueueConfig, 0, len(cfg.Queue.Workers))
	for _, configured := range cfg.Queue.Workers {
		workers = append(workers, worker.QueueConfig{
			Name: configured.Name, Concurrency: configured.Concurrency,
			MinPoll: configured.MinPoll, MaxPoll: configured.MaxPoll,
		})
	}
	instance := instanceID()
	return queueengine.New(queueengine.Config{
		NodeID:            nodeID,
		InstanceID:        instance,
		WorkerID:          "worker-" + instance,
		ClaimBatch:        cfg.Queue.ClaimBatch,
		LeaseSeconds:      cfg.Queue.LeaseSeconds,
		HeartbeatInterval: cfg.Queue.HeartbeatInterval,
		ReapInterval:      cfg.Queue.ReapInterval,
		ReapBatch:         cfg.Queue.ReapBatch,
		DrainTimeout:      cfg.Queue.DrainTimeout,
		Workers:           workers,
		Logger:            logger.Slog(),
	}, bridgeClient)
}

func newObservabilityPublisher(cfg config.Config, nodeID string) (*observability.Publisher, error) {
	if !cfg.Observability.Enabled {
		return nil, nil
	}
	if !duckdb.Supported() {
		return nil, errors.New("observability is enabled but DuckDB support is not compiled in")
	}
	writer, err := duckdb.NewWriter("256MB", 2)
	if err != nil {
		return nil, fmt.Errorf("initialize DuckDB writer: %w", err)
	}
	instance := instanceID()
	clusterID := cfg.Runtime.ClusterID
	if clusterID == "" {
		clusterID = "standalone"
	}
	return observability.NewPublisher(observability.Config{
		DatasetRoot:   cfg.Observability.DatasetRoot,
		SpoolPath:     cfg.Observability.SpoolPath,
		NodeID:        nodeID,
		ClusterID:     clusterID,
		BootID:        instance,
		MaxSpoolBytes: cfg.Observability.MaxSpoolBytes,
		MaxBatchRows:  cfg.Observability.MaxBatchRows,
		MaxBatchBytes: cfg.Observability.MaxBatchBytes,
		FlushInterval: cfg.Observability.FlushInterval,
	}, writer)
}

func newObservabilityQueryEngine(cfg config.Config) (query.Engine, error) {
	if !cfg.Observability.Enabled || !duckdb.Supported() {
		return nil, nil
	}
	return duckdb.NewEngine(2, "256MB")
}

func runRetentionLoop(ctx context.Context, cfg config.Config, logger *logging.Logger, nodeID string) {
	cleaner, err := retention.New(retention.Config{
		DatasetRoot: cfg.Observability.DatasetRoot,
		NodeID:      nodeID,
		MetricsDays: cfg.Observability.MetricsDays,
		LogDays:     cfg.Observability.LogDays,
	})
	if err != nil {
		logger.Warn("observability retention disabled", slog.Any("error", err))
		return
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := cleaner.CleanOwn(ctx)
			if err != nil {
				logger.Warn("observability retention failed", slog.Any("error", err))
				continue
			}
			if result.RemovedFiles > 0 {
				logger.Info("observability retention cleaned", slog.Int("files", result.RemovedFiles), slog.Int64("bytes", result.RemovedBytes))
			}
		}
	}
}

func stopCaddyBounded(timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- caddy.Stop() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("graceful shutdown exceeded %s", timeout)
	}
}

func newHealthRegistry(cfg config.Config) *health.Registry {
	registry := health.NewRegistry()
	registry.Register("runtime", health.KindRuntime, true, false, func(ctx context.Context) health.Result {
		return health.Result{Name: "runtime", Kind: health.KindRuntime, Status: health.StatusOK, Message: "event loop active"}
	})
	registry.Register("php", health.KindPHP, true, false, func(ctx context.Context) health.Result {
		php := frankenphp.Config()
		if !php.ZTS || php.Version.Version == "" {
			return health.Result{Name: "php", Kind: health.KindPHP, Status: health.StatusFailed, Message: "FrankenPHP/PHP is not initialized"}
		}
		return health.Result{Name: "php", Kind: health.KindPHP, Status: health.StatusOK, Message: "PHP " + php.Version.Version + " ZTS classic"}
	})
	registry.Register("app-root", health.KindApp, true, false, func(ctx context.Context) health.Result {
		info, err := os.Stat(cfg.Web.Root)
		if err != nil || !info.IsDir() {
			return health.Result{Name: "app-root", Kind: health.KindApp, Status: health.StatusFailed, Message: "document root is not available"}
		}
		return health.Result{Name: "app-root", Kind: health.KindApp, Status: health.StatusOK}
	})
	return registry
}

type hostStatusProvider struct {
	registry *health.Registry
	machine  *lifecycle.Machine
}

func (p hostStatusProvider) HTTPStatus() web.HTTPStatus {
	state := p.machine.Snapshot().State
	return web.HTTPStatus{
		Live:  state != lifecycle.Stopped && state != lifecycle.Failed,
		Ready: state == lifecycle.Ready,
	}
}

func loadServeConfig(path, root, listen, pidFile, controlSocket string, threads int) (config.Config, error) {
	cfg, err := config.Load(path, os.Environ())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) || root == "" {
			return config.Config{}, err
		}
		cfg = config.Default()
	}
	overrides := config.Overrides{}
	if root != "" {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return config.Config{}, fmt.Errorf("resolve document root: %w", err)
		}
		overrides.Root = &absolute
	}
	if listen != "" {
		overrides.Listen = &listen
	}
	if pidFile != "" {
		absolute, err := filepath.Abs(pidFile)
		if err != nil {
			return config.Config{}, fmt.Errorf("resolve PID file: %w", err)
		}
		overrides.PIDFile = &absolute
	}
	if controlSocket != "" {
		absolute, err := filepath.Abs(controlSocket)
		if err != nil {
			return config.Config{}, fmt.Errorf("resolve control socket: %w", err)
		}
		overrides.ControlSocket = &absolute
	}
	if threads != 0 {
		overrides.Threads = &threads
	}
	return cfg.Apply(overrides)
}

type host struct {
	configPath        string
	machine           *lifecycle.Machine
	stop              chan struct{}
	stopOnce          sync.Once
	mu                sync.RWMutex
	config            config.Config
	logger            *logging.Logger
	health            *health.Registry
	auditor           control.Auditor
	upgrades          *upgrade.Controller
	queryEngine       query.Engine
	obsPublisher      *observability.Publisher
	jsonlConverter    *jsonl.Converter
	accessLogPath     string
	accessLogKeepDays int
	collector         *diagnostics.Collector
}

func newHost(configPath string, cfg config.Config, logger *logging.Logger, registry *health.Registry, auditor control.Auditor, upgrades *upgrade.Controller, queryEngine query.Engine, obsPublisher *observability.Publisher, jsonlConverter *jsonl.Converter, accessLogPath string, accessLogKeepDays int) *host {
	return &host{
		configPath:        configPath,
		config:            cfg,
		machine:           lifecycle.New(),
		stop:              make(chan struct{}),
		logger:            logger,
		health:            registry,
		auditor:           auditor,
		upgrades:          upgrades,
		queryEngine:       queryEngine,
		obsPublisher:      obsPublisher,
		jsonlConverter:    jsonlConverter,
		accessLogPath:     accessLogPath,
		accessLogKeepDays: accessLogKeepDays,
	}
}

func (h *host) requestStop(reason string) {
	h.stopOnce.Do(func() {
		h.logger.Info("stop requested", slog.String("reason", reason))
		close(h.stop)
	})
}

func (h *host) HandleControl(_ context.Context, request control.Request) control.Response {
	switch request.Operation {
	case "status":
		return control.Success(h.status())
	case "health":
		return control.Success(h.healthReport(request.Deep))
	case "version":
		return control.Success(versionData())
	case "stop":
		h.requestStop("control plane request")
		return control.Success(map[string]string{"result": "stopping"})
	case "reload":
		return h.reload()
	case "diagnose":
		return control.Success(map[string]any{
			"health":  h.healthReport(true),
			"version": versionData(),
			"config":  h.configSummary(),
		})
	case "upgrade":
		return h.upgrade(request)
	case "logs", "metrics":
		return h.observabilityQuery(request)
	case "observability-flush":
		return h.observabilityFlush()
	case "observability-clean":
		return h.observabilityClean()
	case "convert-jsonl":
		return h.convertJSONL()
	case "collect-logs":
		return h.collectLogs()
	default:
		return control.Failure("unknown_operation", "unsupported control operation")
	}
}

func (h *host) convertJSONL() control.Response {
	if h.jsonlConverter == nil {
		return control.Failure("unavailable", "jsonl converter is not available")
	}
	result, err := h.jsonlConverter.ConvertOnce(context.Background())
	if err != nil {
		return control.Failure("convert_failed", "jsonl to parquet conversion failed")
	}
	return control.Success(result)
}

func (h *host) collectLogs() control.Response {
	if h.collector == nil {
		return control.Failure("unavailable", "diagnostics collector is not available")
	}
	path, err := h.collector.Collect()
	if err != nil {
		return control.Failure("collect_failed", "diagnostics bundle failed")
	}
	return control.Success(map[string]string{"path": path})
}

func (h *host) observabilityClean() control.Response {
	h.mu.RLock()
	cfg := h.config
	h.mu.RUnlock()
	nodeID := cfg.Runtime.NodeID
	if nodeID == "" {
		nodeID = "node-" + instanceID()
	}
	cleaner, err := retention.New(retention.Config{
		DatasetRoot: cfg.Observability.DatasetRoot,
		NodeID:      nodeID,
		MetricsDays: cfg.Observability.MetricsDays,
		LogDays:     cfg.Observability.LogDays,
	})
	if err != nil {
		return control.Failure("clean_failed", err.Error())
	}
	result, err := cleaner.CleanOwn(context.Background())
	if err != nil {
		return control.Failure("clean_failed", "observability retention failed")
	}
	return control.Success(result)
}

func (h *host) observabilityFlush() control.Response {
	if h.obsPublisher == nil {
		return control.Failure("unavailable", "observability publisher is not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.obsPublisher.FlushAll(ctx); err != nil {
		return control.Failure("flush_failed", "observability flush failed")
	}
	return control.Success(map[string]any{"dropped": h.obsPublisher.Dropped()})
}

func (h *host) observabilityQuery(request control.Request) control.Response {
	if h.queryEngine == nil {
		return control.Failure("unavailable", "observability query engine is not available")
	}
	var payload struct {
		Since      string `json:"since"`
		Until      string `json:"until"`
		Node       string `json:"node"`
		Level      string `json:"level"`
		MetricName string `json:"metricName"`
		Limit      int    `json:"limit"`
	}
	if len(request.Payload) > 0 {
		if err := json.Unmarshal(request.Payload, &payload); err != nil {
			return control.Failure("invalid_request", "invalid observability query payload")
		}
	}
	now := time.Now().UTC()
	since := now.Add(-30 * time.Minute)
	until := now
	if payload.Since != "" {
		offset, err := time.ParseDuration(payload.Since)
		if err != nil {
			return control.Failure("invalid_request", "invalid since duration")
		}
		since = now.Add(-offset)
	}
	if payload.Until != "" {
		parsed, err := time.Parse(time.RFC3339, payload.Until)
		if err != nil {
			return control.Failure("invalid_request", "invalid until timestamp")
		}
		until = parsed
	}
	kind := observability.KindLogs
	if request.Operation == "metrics" {
		kind = observability.KindMetrics
	}
	h.mu.RLock()
	datasetRoot := h.config.Observability.DatasetRoot
	maxDays := h.config.Observability.LogDays
	if kind == observability.KindMetrics {
		maxDays = h.config.Observability.MetricsDays
	}
	h.mu.RUnlock()
	template, params, _, err := query.Build(query.Config{
		DatasetRoot: datasetRoot,
		MaxDays:     maxDays,
		MaxRows:     1000,
		Threads:     2,
		MemoryLimit: "256MB",
		Timeout:     10 * time.Second,
	}, query.Options{
		Kind:       kind,
		Start:      since,
		End:        until,
		Node:       payload.Node,
		Level:      payload.Level,
		MetricName: payload.MetricName,
		Limit:      payload.Limit,
	})
	if err != nil {
		return control.Failure("invalid_request", err.Error())
	}
	rows, err := h.queryEngine.Query(context.Background(), template, 1000, params)
	if err != nil {
		return control.Failure("query_failed", "observability query failed")
	}
	return control.Success(rows)
}

func (h *host) upgrade(request control.Request) control.Response {
	result, err := h.upgrades.Handle(request.Action, request.Payload)
	if err != nil {
		return control.Failure("upgrade_failed", err.Error())
	}
	return control.Success(result)
}

func (h *host) healthReport(deep bool) map[string]any {
	snapshot := h.machine.Snapshot()
	live := snapshot.State != lifecycle.Stopped && snapshot.State != lifecycle.Failed
	ready := snapshot.State == lifecycle.Ready
	report := map[string]any{
		"live":      live,
		"ready":     ready,
		"lifecycle": snapshot,
	}
	var probes health.Snapshot
	if deep {
		probes = h.health.DeepProbe(context.Background())
	} else {
		probes = h.health.Probe(context.Background())
	}
	report["components"] = probes.Components
	report["deep"] = "not_requested"
	if deep {
		if ready && probes.Ready() {
			report["deep"] = "ok"
		} else {
			report["deep"] = "not_ready"
		}
	}
	return report
}

func (h *host) status() map[string]any {
	h.mu.RLock()
	cfg := h.config
	h.mu.RUnlock()
	return map[string]any{
		"lifecycle": h.machine.Snapshot(),
		"web": map[string]any{
			"listen":  cfg.Web.Listen,
			"root":    cfg.Web.Root,
			"threads": cfg.Web.Threads,
		},
		"identity": map[string]any{
			"nodeID":    cfg.Runtime.NodeID,
			"clusterID": cfg.Runtime.ClusterID,
		},
	}
}

func (h *host) configSummary() map[string]any {
	h.mu.RLock()
	cfg := h.config
	h.mu.RUnlock()
	return map[string]any{
		"schemaVersion": cfg.SchemaVersion,
		"runtime": map[string]any{
			"nodeID":    cfg.Runtime.NodeID,
			"clusterID": cfg.Runtime.ClusterID,
		},
		"web": map[string]any{
			"listen":  cfg.Web.Listen,
			"root":    cfg.Web.Root,
			"threads": cfg.Web.Threads,
		},
	}
}

func (h *host) reload() control.Response {
	if err := h.machine.Transition(lifecycle.Reloading, "control plane reload"); err != nil {
		return control.Failure("invalid_state", err.Error())
	}
	candidate, err := config.Load(h.configPath, os.Environ())
	if err != nil {
		_ = h.machine.Transition(lifecycle.Degraded, "configuration reload failed")
		return control.Failure("invalid_configuration", err.Error())
	}
	h.mu.RLock()
	current := h.config
	h.mu.RUnlock()
	if config.RestartRequired(current, candidate) {
		_ = h.machine.Transition(lifecycle.Ready, "restart-required configuration change not applied")
		return control.Success(map[string]any{"reloaded": false, "restartRequired": true})
	}
	rendered, err := json.Marshal(webConfig(candidate, h.accessLogPath, h.accessLogKeepDays))
	if err != nil {
		_ = h.machine.Transition(lifecycle.Degraded, "Caddy configuration rendering failed")
		return control.Failure("reload_failed", "render Caddy configuration")
	}
	if err := caddy.Load(rendered, true); err != nil {
		_ = h.machine.Transition(lifecycle.Degraded, "Caddy reload failed")
		return control.Failure("reload_failed", "Caddy rejected configuration")
	}
	if candidate.Runtime.NodeID == "" {
		candidate.Runtime.NodeID = current.Runtime.NodeID
	}
	h.mu.Lock()
	h.config = candidate
	h.mu.Unlock()
	if err := h.machine.Transition(lifecycle.Ready, "configuration reloaded"); err != nil {
		return control.Failure("invalid_state", err.Error())
	}
	return control.Success(map[string]any{"reloaded": true, "restartRequired": false})
}

func webConfig(cfg config.Config, accessLogPath string, accessLogKeepDays int) *caddy.Config {
	return web.Config(web.Options{
		Root:              cfg.Web.Root,
		Listen:            cfg.Web.Listen,
		Threads:           cfg.Web.Threads,
		ReadHeaderTimeout: cfg.Web.ReadHeaderTimeout,
		IdleTimeout:       cfg.Web.IdleTimeout,
		MaxHeaderBytes:    cfg.Web.MaxHeaderBytes,
		AccessLogPath:     accessLogPath,
		AccessLogKeepDays: accessLogKeepDays,
	})
}

func controlCommand(operation string, args []string) error {
	flags := flag.NewFlagSet(operation, flag.ContinueOnError)
	controlSocket := flags.String("control-socket", config.Default().Runtime.ControlSocket, "Runtime control socket")
	pidFile := flags.String("pid-file", config.Default().Runtime.PIDFile, "PID file fallback")
	deep := flags.Bool("deep", operation == "diagnose", "request deep health")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	response, err := control.Call(context.Background(), *controlSocket, control.Request{Operation: operation, Deep: *deep})
	if err == nil {
		if !response.OK {
			return fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
		}
		return printRawJSON(response.Result)
	}
	if operation == "status" || operation == "stop" {
		return processFallback(*pidFile, operation == "stop")
	}
	return err
}

func processFallback(pidFile string, stop bool) error {
	pid, err := readPIDFile(pidFile)
	if err != nil {
		return notRunningError("zentao-runtime is not running: %v", err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return notRunningError("zentao-runtime is not running: %v", err)
	}
	if stop {
		if err := process.Signal(syscall.SIGTERM); err != nil {
			return fmt.Errorf("stop process %d: %w", pid, err)
		}
		return printJSON(map[string]any{"result": "stopping", "pid": pid, "fallback": true})
	}
	return printJSON(map[string]any{"state": "running", "pid": pid, "fallback": true})
}

func createPIDFile(name string) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return fmt.Errorf("create PID directory: %w", err)
	}
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create PID file: %w", err)
	}
	defer file.Close()
	if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
		return fmt.Errorf("write PID file: %w", err)
	}
	return nil
}

func readPIDFile(name string) (int, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return 0, fmt.Errorf("read PID file: %w", err)
	}
	pid, err := strconv.Atoi(string(bytesTrimSpace(data)))
	if err != nil || pid < 1 {
		return 0, fmt.Errorf("invalid PID file %q", name)
	}
	return pid, nil
}

func bytesTrimSpace(value []byte) []byte {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\n' || value[start] == '\r' || value[start] == '\t') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\n' || value[end-1] == '\r' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}

func versionData() map[string]any {
	php := frankenphp.Config()
	return map[string]any{
		"runtime":      runtimeVersion,
		"frankenphp":   frankenPHPVersion,
		"caddy":        caddyVersion,
		"php":          php.Version.Version,
		"php_zts":      php.ZTS,
		"zend_signals": php.ZendSignals,
		"duckdb":       duckdb.Version,
		"mode":         "classic",
	}
}

func printJSON(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func printRawJSON(value json.RawMessage) error {
	if len(value) == 0 {
		return printJSON(map[string]any{})
	}
	fmt.Println(string(value))
	return nil
}

func instanceID() string {
	hostname, err := os.Hostname()
	if err != nil {
		return strconv.Itoa(os.Getpid())
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}

// resolveNodeID returns the configured node ID, or a stable per-installation
// ID persisted in <installRoot>/data/node-id so that log file names and
// Parquet partitions stay stable across restarts.
func resolveNodeID(cfg config.Config, installRoot string) (string, error) {
	if cfg.Runtime.NodeID != "" {
		return cfg.Runtime.NodeID, nil
	}
	dataDir := filepath.Join(installRoot, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("create data directory: %w", err)
	}
	nodeFile := filepath.Join(dataDir, "node-id")
	if data, err := os.ReadFile(nodeFile); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			if !validNodeID(id) {
				return "", fmt.Errorf("node id file contains unsupported characters: %q", id)
			}
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read node id: %w", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate node id: %w", err)
	}
	host := safeHostname(hostname)
	if len(host) > 48 {
		host = host[:48]
	}
	id := fmt.Sprintf("node-%s-%x", host, random)
	if err := os.WriteFile(nodeFile, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write node id: %w", err)
	}
	return id, nil
}

func validNodeID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func safeHostname(hostname string) string {
	var builder strings.Builder
	for _, r := range hostname {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('-')
		}
	}
	if value := strings.Trim(builder.String(), "-"); value != "" {
		return value
	}
	return "host"
}
