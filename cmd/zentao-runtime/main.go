package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
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
	"github.com/vimrus/runtime/internal/queue/bridge"
	queueclient "github.com/vimrus/runtime/internal/queue/client"
	queueengine "github.com/vimrus/runtime/internal/queue/engine"
	"github.com/vimrus/runtime/internal/queue/worker"
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
			fmt.Fprintln(os.Stderr, "zentao:", exitErr.message)
			os.Exit(exitErr.code)
		}
		printError(err)
		os.Exit(1)
	}
}

// printError renders wrapped error chains as indented lines. Caddy and
// FrankenPHP wrap failures with ": " and produce a single very long line,
// which is hard to read in a terminal; splitting the chain keeps the original
// wording while making the module path scannable.
func printError(err error) {
	const maxIndent = 6
	message := err.Error()
	parts := strings.Split(message, ": ")
	if strings.Contains(message, "\n") || len(parts) == 1 {
		fmt.Fprintln(os.Stderr, "zentao: "+message)
		return
	}
	fmt.Fprintln(os.Stderr, "zentao: "+parts[0])
	for index, part := range parts[1:] {
		indent := index + 1
		if indent > maxIndent {
			indent = maxIndent
		}
		fmt.Fprintln(os.Stderr, strings.Repeat("  ", indent)+part)
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
		return usageError("usage: zentao <serve|start|status|stop|reload|health|diagnose|logs|metrics|flush-observability|clean-observability|convert-jsonl|collect-logs|php-cli|version|service|run-service>")
	}
	switch args[0] {
	case "serve", "start":
		return serve(args[1:])
	case "run-service":
		return runServiceCommand(args[1:])
	case "service":
		return serviceCommand(args[1:])
	case "status", "stop", "reload", "health", "diagnose":
		return controlCommand(args[0], args[1:])
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
	controlSocket := flags.String("control-socket", defaultControlSocketPath(), "Runtime control socket")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	response, err := callControl(*controlSocket, control.Request{Operation: "convert-jsonl"})
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
	controlSocket := flags.String("control-socket", defaultControlSocketPath(), "Runtime control socket")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	response, err := callControl(*controlSocket, control.Request{Operation: "collect-logs"})
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
			filepath.Join(defaultInstallRoot(), "php", "bin", "php"),
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
	if err := ensureDMSvcConf(defaultInstallRoot()); err != nil {
		fmt.Fprintf(os.Stderr, "runtime: warning: %v\n", err)
	}
	command := exec.Command(binary, flags.Args()...)
	command.Env = append(os.Environ(), phpConfigurationEnvironment(defaultInstallRoot())...)
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
	controlSocket := flags.String("control-socket", defaultControlSocketPath(), "Runtime control socket")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	response, err := callControl(*controlSocket, control.Request{Operation: "observability-clean"})
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
	controlSocket := flags.String("control-socket", defaultControlSocketPath(), "Runtime control socket")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	response, err := callControl(*controlSocket, control.Request{Operation: "observability-flush"})
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
	controlSocket := flags.String("control-socket", defaultControlSocketPath(), "Runtime control socket")
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
	response, err := callControl(*controlSocket, control.Request{Operation: operation, Payload: data})
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
	root := flags.String("root", "", "PHP document root override")
	listen := flags.String("listen", "", "HTTP listen address override")
	pidFile := flags.String("pid-file", "", "PID file override")
	controlSocket := flags.String("control-socket", "", "Control socket override")
	threads := flags.Int("threads", 0, "PHP thread count override")
	installRoot := flags.String("install-root", defaultInstallRoot(), "Installation root")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	installRootPath, err := filepath.Abs(*installRoot)
	if err != nil {
		return fmt.Errorf("resolve install root: %w", err)
	}
	if err := configurePHPEnvironment(installRootPath); err != nil {
		return err
	}

	configPath := filepath.Join(installRootPath, "config", "runtime.toml")
	if info, statErr := os.Stat(configPath); statErr != nil || info.IsDir() {
		// Configuration is mandatory: the Runtime must never silently fall back
		// to built-in paths and settings that happen to start.
		return fmt.Errorf("configuration file not found: %s\ncreate it first: cp %s %s",
			configPath,
			filepath.Join(installRootPath, "config", "runtime.example.toml"),
			configPath)
	}
	cfg, err := loadServeConfig(configPath, *root, *listen, *pidFile, *controlSocket, *threads)
	if err != nil {
		return err
	}
	// Applications are pluggable: a missing or broken application must not
	// prevent the Runtime from starting. It only degrades readiness until the
	// application is attached again.
	if err := prepareApps(cfg); err != nil {
		return err
	}

	nodeID, err := resolveNodeID(cfg, installRootPath)
	if err != nil {
		return err
	}
	cfg.Runtime.NodeID = nodeID
	clusterID := cfg.Runtime.ClusterID
	if clusterID == "" {
		clusterID = "standalone"
	}
	identityEnv := map[string]string{
		"ZENTAO_NODE_ID":    nodeID,
		"ZENTAO_CLUSTER_ID": clusterID,
		"ZENTAO_BOOT_ID":    instanceID(),
	}
	for name, value := range identityEnv {
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set %s: %w", name, err)
		}
	}
	logsDir := filepath.Dir(cfg.Runtime.LogPath)
	if cfg.Runtime.LogPath == "" {
		logsDir = filepath.Join(installRootPath, "logs")
	}
	effectiveLogPath := filepath.Join(logsDir, "runtime-"+nodeID+".jsonl")
	effectiveAuditLog := cfg.Runtime.AuditLog
	if effectiveAuditLog == "" {
		effectiveAuditLog = filepath.Join(logsDir, "audit-"+nodeID+".jsonl")
	}
	effectiveAccessLog := filepath.Join(logsDir, "access-"+nodeID+".jsonl")

	logger, err := logging.New(logging.Options{
		Level:        parseLogLevel(cfg.Runtime.LogLevel),
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

	auditor, err := control.NewFileAuditorWithRetention(effectiveAuditLog, cfg.Runtime.AuditLogMaxBytes, cfg.Runtime.AuditLogMaxBackups, cfg.Runtime.AuditLogKeepDays)
	if err != nil {
		return fmt.Errorf("initialize audit log: %w", err)
	}
	defer auditor.Close()

	registry := newHealthRegistry(cfg)
	obsPublisher, err := newObservabilityPublisher(cfg, cfg.Observability.DatasetRoot, cfg.Observability.SpoolPath, nodeID)
	if err != nil {
		return err
	}
	registerObservabilityHealth(registry, "observability", obsPublisher)
	queryEngine, err := newObservabilityQueryEngine(cfg)
	if err != nil {
		return err
	}
	obsApps, err := prepareObservabilityApps(&cfg, nodeID, queryEngine != nil)
	if err != nil {
		return err
	}
	for _, app := range obsApps {
		registerObservabilityHealth(registry, "observability:"+app.id, app.publisher)
	}
	queueEngines, err := prepareQueueEngines(&cfg, logger, nodeID)
	if err != nil {
		return err
	}
	for _, entry := range queueEngines {
		engine := entry.engine
		registry.Register(entry.name, health.KindDependency, false, false, func(context.Context) health.Result {
			result := engine.Health()
			result.Name = entry.name
			return result
		})
	}
	var jsonlConverter *jsonl.Converter
	if obsPublisher != nil {
		clusterID := cfg.Runtime.ClusterID
		if clusterID == "" {
			clusterID = "standalone"
		}
		jsonlConverter, err = jsonl.New(jsonl.Config{
			LogsDir:            logsDir,
			NodeID:             nodeID,
			ClusterID:          clusterID,
			BootID:             instanceID(),
			Sources:            cfg.Observability.JSONLConvertSources,
			Publisher:          obsPublisher,
			Interval:           cfg.Observability.JSONLConvertInterval,
			DeleteAfterConvert: cfg.Observability.JSONLDeleteAfterConvert,
		})
		if err != nil {
			return fmt.Errorf("initialize jsonl converter: %w", err)
		}
	}
	host := newHost(configPath, cfg, logger, registry, auditor, queryEngine, obsPublisher, obsApps, jsonlConverter, effectiveAccessLog, cfg.Observability.JSONLKeepDays)
	host.logsDir = logsDir
	host.jsonlSources = cfg.Observability.JSONLConvertSources
	host.collector = &diagnostics.Collector{
		LogPaths: []string{
			effectiveLogPath,
			effectiveAuditLog,
			effectiveAccessLog,
			filepath.Join(installRootPath, "logs", "php-error.log"),
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
	observabilityBridge := web.ObservabilityBridgeOptions{}
	if len(obsApps) > 0 {
		tokens := make([]string, 0, len(obsApps))
		for _, app := range obsApps {
			tokens = append(tokens, app.token)
			web.SetObservabilitySinkFor(app.token, app.publisher)
			if queryEngine != nil {
				// D7：只读查询桥可用时才进入 flush 模式，避免与本地 JSONL 双写。
				web.SetObservabilityQueryHandlerFor(app.token, host.appQueryBridge(app.dir))
			}
		}
		observabilityBridge = web.ObservabilityBridgeOptions{
			Enabled: true,
			Listen:  cfg.Observability.BridgeListen,
			Tokens:  tokens,
		}
	}
	if err := caddy.Run(webConfig(cfg, effectiveAccessLog, cfg.Observability.JSONLKeepDays, observabilityBridge)); err != nil {
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
	fmt.Fprintf(os.Stdout, "zentao: listening on %s\n", cfg.Web.Listen)
	logger.Info("runtime ready", slog.String("listen", cfg.Web.Listen), slog.String("root", cfg.Web.Root), slog.String("mode", "classic"))
	serveCtx, serveCancel := context.WithCancel(context.Background())
	defer serveCancel()
	go runLogPruneLoop(serveCtx, logger)
	phpErrorRotator := logging.NewExternalRotator(filepath.Join(installRootPath, "logs", "php-error.log"), cfg.Runtime.PHPErrorLogMaxBytes, cfg.Runtime.PHPErrorLogMaxBackups, cfg.Observability.JSONLKeepDays)
	go runExternalLogLoop(serveCtx, logger, phpErrorRotator)
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
	for _, entry := range queueEngines {
		if err := entry.engine.Start(serveCtx); err != nil {
			logger.Warn("queue engine failed to start", slog.String("app", entry.name), slog.Any("error", err))
		} else {
			logger.Info("queue engine started", slog.String("app", entry.name))
		}
	}
	if obsPublisher != nil {
		go func() { _ = obsPublisher.Recover(serveCtx) }()
		go obsPublisher.Run(serveCtx)
		for _, app := range obsApps {
			publisher := app.publisher
			go func() { _ = publisher.Recover(serveCtx) }()
			go publisher.Run(serveCtx)
		}
		for _, target := range observabilityRoots(cfg, obsApps) {
			go runRetentionLoop(serveCtx, logger, nodeID, target, cfg.Observability)
		}
		go jsonlConverter.Run(serveCtx)
		logger.Info("observability publisher started", slog.Int("applications", len(obsApps)))
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
	for _, entry := range queueEngines {
		if err := entry.engine.Stop(); err != nil {
			logger.Warn("queue engine drain failed", slog.String("app", entry.name), slog.Any("error", err))
		}
	}
	for _, publisher := range observabilityPublishers(obsPublisher, obsApps) {
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := publisher.FlushAll(flushCtx); err != nil {
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

func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func phpConfigurationEnvironment(installRoot string) []string {
	configDir := filepath.Join(installRoot, "config")
	return []string{
		"ZENTAO_RUNTIME_PHP_ROOT=" + installRoot,
		"PHPRC=" + configDir,
		"PHP_INI_SCAN_DIR=" + filepath.Join(configDir, "conf.d"),
		// DM8 客户端从 DM_SVC_PATH 指向的目录读取 dm_svc.conf（默认 /etc，
		// 原生/信创部署没有 root 写 /etc 的权限，故指向安装根下的 config；
		// 文件缺失时由 ensureDMSvcConf 自动写入默认内容）。
		"DM_SVC_PATH=" + configDir,
	}
}

func configurePHPEnvironment(installRoot string) error {
	for _, entry := range phpConfigurationEnvironment(installRoot) {
		name, value, _ := strings.Cut(entry, "=")
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("configure PHP environment: %w", err)
		}
	}
	if err := ensureDMSvcConf(installRoot); err != nil {
		// 不阻塞启动：Docker 镜像另有 /etc/dm_svc.conf，无法写入时 DM
		// 客户端会回退默认路径。
		fmt.Fprintf(os.Stderr, "runtime: warning: %v\n", err)
	}
	return nil
}

// defaultDMSvcConf 是达梦客户端连接所需的默认全局参数：东八区、中文提示、
// UTF-8（缺 CHAR_CODE 会导致中文乱码，缺 TIME_ZONE 会导致时间偏移）。
const defaultDMSvcConf = "TIME_ZONE=(480)\nLANGUAGE=(cn)\nCHAR_CODE=(PG_UTF8)\n"

// ensureDMSvcConf 保证 <installRoot>/config/dm_svc.conf 存在：随包已有或用户
// 自定义的配置保留原样，只有缺失时才写入默认内容，做到物理机部署开箱即用。
func ensureDMSvcConf(installRoot string) error {
	configDir := filepath.Join(installRoot, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("create config directory %s: %w", configDir, err)
	}
	path := filepath.Join(configDir, "dm_svc.conf")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(defaultDMSvcConf), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
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

// runExternalLogLoop keeps php-error.log bounded. The file is written by the
// embedded PHP error_log, so it is rotated with copytruncate at a fixed check
// interval; day-based retention reuses observability.jsonlKeepDays.
func runExternalLogLoop(ctx context.Context, logger *logging.Logger, rotator *logging.ExternalRotator) {
	maintain := func() {
		rotated, err := rotator.RotateIfNeeded()
		if err != nil {
			logger.Warn("php error log rotation failed", slog.Any("error", err))
		} else if rotated {
			logger.Info("php error log rotated")
		}
		if err := rotator.Prune(); err != nil {
			logger.Warn("php error log prune failed", slog.Any("error", err))
		}
	}

	maintain()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			maintain()
		}
	}
}

// queueEngineEntry pairs one application with its queue engine for start/stop
// and health reporting.
type queueEngineEntry struct {
	name   string
	engine *queueengine.Engine
}

// prepareQueueEngines builds one queue engine per configured application.
// Legacy single-application configurations keep the previous behavior: one
// process-wide token and one flat bridge. Explicit [[apps]] configurations
// get one engine per queue-enabled application with its own token, its own
// namespaced loopback path and its own in-application bridge URL.
func prepareQueueEngines(cfg *config.Config, logger *logging.Logger, nodeID string) ([]queueEngineEntry, error) {
	if !cfg.Queue.Enabled {
		return nil, nil
	}
	if cfg.LegacyApp() || len(cfg.Apps) == 0 {
		// The bridge credential is generated per Runtime start: it is exposed
		// to PHP through the environment and used by the Go bridge client.
		tokenBytes := make([]byte, 32)
		if _, err := rand.Read(tokenBytes); err != nil {
			return nil, fmt.Errorf("generate queue bridge token: %w", err)
		}
		token := hex.EncodeToString(tokenBytes)
		if err := os.Setenv("ZENTAO_QUEUE_TOKEN", token); err != nil {
			return nil, fmt.Errorf("set queue bridge token: %w", err)
		}
		cfg.Queue.BridgeToken = token
		engine, err := newQueueEngine(*cfg, logger, nodeID)
		if err != nil {
			return nil, err
		}
		if engine == nil {
			return nil, nil
		}
		return []queueEngineEntry{{name: "queue", engine: engine}}, nil
	}

	entries := make([]queueEngineEntry, 0, len(cfg.Apps))
	for index := range cfg.Apps {
		app := &cfg.Apps[index]
		if !app.Enabled || !app.Queue.Enabled {
			continue
		}
		token := app.Queue.Token
		if token == "" && app.Env != nil {
			token = app.Env["ZENTAO_QUEUE_TOKEN"]
		}
		if token == "" {
			tokenBytes := make([]byte, 32)
			if _, err := rand.Read(tokenBytes); err != nil {
				return nil, fmt.Errorf("generate queue bridge token for %s: %w", app.ID, err)
			}
			token = hex.EncodeToString(tokenBytes)
		}
		app.Queue.Token = token
		if app.Env == nil {
			app.Env = make(map[string]string)
		}
		setEnvDefault(app.Env, "ZENTAO_QUEUE_TOKEN", token)

		engine, err := newAppQueueEngine(*cfg, *app, logger, nodeID)
		if err != nil {
			return nil, err
		}
		if engine != nil {
			entries = append(entries, queueEngineEntry{name: "queue:" + app.ID, engine: engine})
		}
	}
	return entries, nil
}

func newAppQueueEngine(cfg config.Config, app config.App, logger *logging.Logger, nodeID string) (*queueengine.Engine, error) {
	if app.Queue.Token == "" {
		return nil, fmt.Errorf("queue bridge token for application %s is missing", app.ID)
	}
	return newQueueEngineWithBridge(cfg, "http://"+queueBridgeListen(cfg)+bridge.AppBasePath(app.ID), app.Queue.Token, logger, nodeID)
}

func queueBridgeListen(cfg config.Config) string {
	listen := "127.0.0.1:8081"
	if parsed, err := url.Parse(cfg.Queue.BridgeBaseURL); err == nil && parsed.Host != "" {
		listen = parsed.Host
	}
	return listen
}

func newQueueEngine(cfg config.Config, logger *logging.Logger, nodeID string) (*queueengine.Engine, error) {
	if !cfg.Queue.Enabled {
		return nil, nil
	}
	if cfg.Queue.BridgeToken == "" {
		return nil, errors.New("queue is enabled but ZENTAO_QUEUE_TOKEN or queue.bridgeToken is missing")
	}
	return newQueueEngineWithBridge(cfg, cfg.Queue.BridgeBaseURL, cfg.Queue.BridgeToken, logger, nodeID)
}

func newQueueEngineWithBridge(cfg config.Config, baseURL, token string, logger *logging.Logger, nodeID string) (*queueengine.Engine, error) {
	bridgeClient, err := queueclient.New(queueclient.Config{
		BaseURL:        baseURL,
		Token:          token,
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

func newObservabilityPublisher(cfg config.Config, datasetRoot, spoolPath, nodeID string) (*observability.Publisher, error) {
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
		DatasetRoot:      datasetRoot,
		SpoolPath:        spoolPath,
		NodeID:           nodeID,
		ClusterID:        clusterID,
		BootID:           instance,
		MaxSpoolBytes:    cfg.Observability.MaxSpoolBytes,
		MaxBatchRows:     cfg.Observability.MaxBatchRows,
		MaxBatchBytes:    cfg.Observability.MaxBatchBytes,
		MinBatchRows:     cfg.Observability.MinBatchRows,
		MinBatchBytes:    cfg.Observability.MinBatchBytes,
		FlushInterval:    cfg.Observability.FlushInterval,
		MaxFlushInterval: cfg.Observability.MaxFlushInterval,
	}, writer)
}

// observabilityApp binds one application to its bridge token and Parquet
// publisher; each application writes only its own observability dataset
// (<DataDir>/observability).
type observabilityApp struct {
	id        string
	dir       string
	token     string
	publisher *observability.Publisher
}

// observabilityRoot names one dataset root handled by retention and cleanup.
type observabilityRoot struct {
	name string
	root string
}

// prepareObservabilityApps creates one publisher per enabled application,
// generates per-application bridge tokens and injects the per-application
// observability environment. Explicit application environment values win over
// the generated defaults.
func prepareObservabilityApps(cfg *config.Config, nodeID string, flush bool) ([]observabilityApp, error) {
	if !cfg.Observability.Enabled {
		return nil, nil
	}
	apps := make([]observabilityApp, 0, len(cfg.Apps))
	for index := range cfg.Apps {
		app := &cfg.Apps[index]
		if !app.Enabled {
			continue
		}
		token := ""
		if app.Env != nil {
			token = app.Env["ZENTAO_OBSERVABILITY_TOKEN"]
		}
		if token == "" {
			tokenBytes := make([]byte, 32)
			if _, err := rand.Read(tokenBytes); err != nil {
				return nil, fmt.Errorf("generate observability bridge token for %s: %w", app.ID, err)
			}
			token = hex.EncodeToString(tokenBytes)
		}
		if app.Env == nil {
			app.Env = make(map[string]string)
		}
		setEnvDefault(app.Env, "ZENTAO_OBSERVABILITY_URL", "http://"+cfg.Observability.BridgeListen+"/internal/runtime/observability/v1/events")
		setEnvDefault(app.Env, "ZENTAO_OBSERVABILITY_TOKEN", token)
		setEnvDefault(app.Env, "ZENTAO_OBSERVABILITY_SCHEMA", strconv.Itoa(observability.SchemaVersion))
		if flush {
			// 只读查询桥可用时才进入 flush 模式，避免与本地 JSONL 双写。
			setEnvDefault(app.Env, "ZENTAO_OBSERVABILITY_QUERY_URL", "http://"+cfg.Observability.BridgeListen+"/internal/runtime/observability/v1/query")
			setEnvDefault(app.Env, "ZENTAO_OBSERVABILITY_QUERY_TOKEN", token)
			setEnvDefault(app.Env, "ZENTAO_LOG_MODE", "flush")
		}
		publisher, err := newObservabilityPublisher(*cfg, app.ObservabilityDir(), filepath.Join(cfg.Observability.SpoolPath, app.ID), nodeID)
		if err != nil {
			return nil, fmt.Errorf("initialize observability for application %s: %w", app.ID, err)
		}
		if publisher != nil {
			apps = append(apps, observabilityApp{id: app.ID, dir: app.ObservabilityDir(), token: token, publisher: publisher})
		}
	}
	return apps, nil
}

func registerObservabilityHealth(registry *health.Registry, name string, publisher *observability.Publisher) {
	if publisher == nil {
		return
	}
	registry.Register(name, health.KindShared, false, true, func(context.Context) health.Result {
		if err := publisher.Healthy(); err != nil {
			return health.Result{Name: name, Kind: health.KindShared, Status: health.StatusFailed, Message: "observability spool is not writable"}
		}
		return health.Result{Name: name, Kind: health.KindShared, Status: health.StatusOK}
	})
}

func observabilityRoots(cfg config.Config, apps []observabilityApp) []observabilityRoot {
	roots := []observabilityRoot{{name: "runtime", root: cfg.Observability.DatasetRoot}}
	for _, app := range apps {
		roots = append(roots, observabilityRoot{name: app.id, root: app.dir})
	}
	return roots
}

func observabilityPublishers(runtime *observability.Publisher, apps []observabilityApp) []*observability.Publisher {
	publishers := make([]*observability.Publisher, 0, len(apps)+1)
	if runtime != nil {
		publishers = append(publishers, runtime)
	}
	for _, app := range apps {
		publishers = append(publishers, app.publisher)
	}
	return publishers
}

func newObservabilityQueryEngine(cfg config.Config) (query.Engine, error) {
	if !cfg.Observability.Enabled || !duckdb.Supported() {
		return nil, nil
	}
	return duckdb.NewEngine(2, "256MB")
}

func runRetentionLoop(ctx context.Context, logger *logging.Logger, nodeID string, target observabilityRoot, cfg config.Observability) {
	cleaner, err := retention.New(retention.Config{
		DatasetRoot: target.root,
		NodeID:      nodeID,
		MetricsDays: cfg.MetricsDays,
		LogDays:     cfg.LogDays,
	})
	if err != nil {
		logger.Warn("observability retention disabled", slog.String("dataset", target.name), slog.Any("error", err))
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
				logger.Warn("observability retention failed", slog.String("dataset", target.name), slog.Any("error", err))
				continue
			}
			if result.RemovedFiles > 0 {
				logger.Info("observability retention cleaned", slog.String("dataset", target.name), slog.Int("files", result.RemovedFiles), slog.Int64("bytes", result.RemovedBytes))
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
	for _, app := range cfg.Apps {
		if !app.Enabled {
			continue
		}
		app := app
		root := app.DocumentRoot()
		registry.Register("app-root:"+app.ID, health.KindApp, true, false, func(ctx context.Context) health.Result {
			info, err := os.Stat(root)
			if err != nil || !info.IsDir() {
				return health.Result{Name: "app-root:" + app.ID, Kind: health.KindApp, Status: health.StatusFailed, Message: "application " + app.ID + " document root is not available"}
			}
			return health.Result{Name: "app-root:" + app.ID, Kind: health.KindApp, Status: health.StatusOK}
		})
		registry.Register("app:"+app.ID, health.KindApp, false, true, func(ctx context.Context) health.Result {
			prefix, err := config.NormalizePath(app.Path)
			if err != nil {
				prefix = app.Path
			}
			if prefix == "/" {
				prefix = ""
			}
			appHealthURL := "http://" + cfg.Web.Listen + prefix + "/health.php"
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, appHealthURL, nil)
			if err != nil {
				return health.Result{Name: "app:" + app.ID, Kind: health.KindApp, Status: health.StatusFailed, Message: "invalid app health url"}
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return health.Result{Name: "app:" + app.ID, Kind: health.KindApp, Status: health.StatusFailed, Message: "app health unreachable"}
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			if err != nil {
				return health.Result{Name: "app:" + app.ID, Kind: health.KindApp, Status: health.StatusFailed, Message: "app health read failed"}
			}
			var probe struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(body, &probe) != nil || probe.Status == "" {
				return health.Result{Name: "app:" + app.ID, Kind: health.KindApp, Status: health.StatusFailed, Message: "invalid app health response"}
			}
			status := health.StatusFailed
			if probe.Status == "ok" {
				status = health.StatusOK
			} else if probe.Status == "degraded" {
				status = health.StatusDegraded
			}
			return health.Result{Name: "app:" + app.ID, Kind: health.KindApp, Status: status}
		})
	}
	return registry
}

type hostStatusProvider struct {
	registry *health.Registry
	machine  *lifecycle.Machine
}

func (p hostStatusProvider) HTTPStatus() web.HTTPStatus {
	state := p.machine.Snapshot().State
	probes := p.registry.Probe(context.Background())
	return web.HTTPStatus{
		Live:     state != lifecycle.Stopped && state != lifecycle.Failed,
		Ready:    state == lifecycle.Ready && probes.Ready(),
		Degraded: probes.AnyDegraded || probes.AnyFailed,
	}
}

func loadServeConfig(path, root, listen, pidFile, controlSocket string, threads int) (config.Config, error) {
	cfg, err := config.Load(path, os.Environ())
	if err != nil {
		return config.Config{}, err
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
	queryEngine       query.Engine
	obsPublisher      *observability.Publisher
	obsApps           []observabilityApp
	jsonlConverter    *jsonl.Converter
	accessLogPath     string
	accessLogKeepDays int
	logsDir           string
	jsonlSources      []string
	collector         *diagnostics.Collector
}

func newHost(configPath string, cfg config.Config, logger *logging.Logger, registry *health.Registry, auditor control.Auditor, queryEngine query.Engine, obsPublisher *observability.Publisher, obsApps []observabilityApp, jsonlConverter *jsonl.Converter, accessLogPath string, accessLogKeepDays int) *host {
	return &host{
		configPath:        configPath,
		config:            cfg,
		machine:           lifecycle.New(),
		stop:              make(chan struct{}),
		logger:            logger,
		health:            registry,
		auditor:           auditor,
		queryEngine:       queryEngine,
		obsPublisher:      obsPublisher,
		obsApps:           obsApps,
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

func (h *host) HandleControl(ctx context.Context, request control.Request) control.Response {
	switch request.Operation {
	case "status":
		return control.Success(h.status())
	case "health":
		return control.Success(h.healthReport(ctx, request.Deep))
	case "version":
		return control.Success(versionData())
	case "stop":
		h.requestStop("control plane request")
		return control.Success(map[string]string{"result": "stopping"})
	case "reload":
		return h.reload()
	case "diagnose":
		diagnose := map[string]any{
			"health":  h.healthReport(ctx, true),
			"version": versionData(),
			"config":  h.configSummary(),
		}
		if stats := h.observabilityStats(); stats != nil {
			diagnose["observability"] = stats
		}
		return control.Success(diagnose)
	case "logs", "metrics":
		return h.observabilityQuery(ctx, request)
	case "observability-flush":
		return h.observabilityFlush(ctx)
	case "observability-clean":
		return h.observabilityClean(ctx)
	case "convert-jsonl":
		return h.convertJSONL(ctx)
	case "collect-logs":
		return h.collectLogs()
	default:
		return control.Failure("unknown_operation", "unsupported control operation")
	}
}

func (h *host) convertJSONL(ctx context.Context) control.Response {
	if h.jsonlConverter == nil {
		return control.Failure("unavailable", "jsonl converter is not available")
	}
	result, err := h.jsonlConverter.ConvertOnce(ctx)
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

func (h *host) observabilityClean(ctx context.Context) control.Response {
	h.mu.RLock()
	cfg := h.config
	apps := append([]observabilityApp(nil), h.obsApps...)
	h.mu.RUnlock()
	nodeID := cfg.Runtime.NodeID
	if nodeID == "" {
		nodeID = "node-" + instanceID()
	}
	cleaned := map[string]any{}
	for _, target := range observabilityRoots(cfg, apps) {
		cleaner, err := retention.New(retention.Config{
			DatasetRoot: target.root,
			NodeID:      nodeID,
			MetricsDays: cfg.Observability.MetricsDays,
			LogDays:     cfg.Observability.LogDays,
		})
		if err != nil {
			return control.Failure("clean_failed", err.Error())
		}
		result, err := cleaner.CleanOwn(ctx)
		if err != nil {
			return control.Failure("clean_failed", "observability retention failed")
		}
		cleaned[target.name] = result
	}
	if len(cleaned) == 0 {
		return control.Failure("unavailable", "observability is not enabled")
	}
	return control.Success(cleaned)
}

func (h *host) observabilityFlush(ctx context.Context) control.Response {
	if h.obsPublisher == nil && len(h.obsApps) == 0 {
		return control.Failure("unavailable", "observability publisher is not available")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stats := map[string]any{}
	if h.obsPublisher != nil {
		if err := h.obsPublisher.FlushAll(ctx); err != nil {
			return control.Failure("flush_failed", "observability flush failed")
		}
		stats["runtime"] = h.obsPublisher.Stats()
	}
	for _, app := range h.obsApps {
		if err := app.publisher.FlushAll(ctx); err != nil {
			return control.Failure("flush_failed", "observability flush failed")
		}
		stats[app.id] = app.publisher.Stats()
	}
	return control.Success(map[string]any{"stats": stats})
}

// observabilityStats returns the pipeline statistics of the runtime dataset
// and every application dataset, keyed by dataset name.
func (h *host) observabilityStats() map[string]any {
	if h.obsPublisher == nil && len(h.obsApps) == 0 {
		return nil
	}
	stats := map[string]any{}
	if h.obsPublisher != nil {
		stats["runtime"] = h.obsPublisher.Stats()
	}
	for _, app := range h.obsApps {
		stats[app.id] = app.publisher.Stats()
	}
	return stats
}

func (h *host) observabilityQuery(ctx context.Context, request control.Request) control.Response {
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
	template, params, limit, err := query.Build(query.Config{
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
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := h.queryEngine.Query(queryCtx, template, 1000, params)
	if err != nil {
		return control.Failure("query_failed", "observability query failed")
	}
	if kind == observability.KindLogs && h.logsDir != "" && h.config.Observability.JSONLDeleteAfterConvert {
		rows = h.mergeRecentJSONLLogs(rows, since, until, payload.Node, payload.Level, limit)
	}
	return control.Success(rows)
}

func (h *host) mergeRecentJSONLLogs(parquetRows []query.Row, since, until time.Time, node, level string, limit int) []query.Row {
	clusterID := h.config.Runtime.ClusterID
	if clusterID == "" {
		clusterID = "standalone"
	}
	events, _, err := jsonl.ReadRecent(jsonl.Config{
		LogsDir:   h.logsDir,
		NodeID:    h.config.Runtime.NodeID,
		ClusterID: clusterID,
		BootID:    instanceID(),
		Sources:   h.jsonlSources,
	})
	if err != nil {
		h.logger.Warn("read recent jsonl logs failed", slog.Any("error", err))
		return parquetRows
	}
	rows := make([]query.Row, 0, len(parquetRows)+len(events))
	rows = append(rows, parquetRows...)
	for _, event := range events {
		if event.EventTime.Before(since) || !event.EventTime.Before(until) {
			continue
		}
		if node != "" && event.NodeID != node {
			continue
		}
		if level != "" && !strings.EqualFold(event.Level, level) {
			continue
		}
		rows = append(rows, query.Row{
			"eventTime": event.EventTime,
			"eventID":   event.EventID,
			"node":      event.NodeID,
			"source":    event.Source,
			"level":     event.Level,
			"message":   event.Message,
			"traceID":   event.TraceID,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rowEventTimeNanos(rows[i]) > rowEventTimeNanos(rows[j])
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func rowEventTimeNanos(row query.Row) int64 {
	value := row["eventTime"]
	switch typed := value.(type) {
	case time.Time:
		return typed.UnixNano()
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if parsed, err := time.Parse(layout, typed); err == nil {
				return parsed.UnixNano()
			}
		}
	case float64:
		return int64(typed * float64(time.Second))
	}
	return 0
}

func (h *host) healthReport(ctx context.Context, deep bool) map[string]any {
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
		probes = h.health.DeepProbe(ctx)
	} else {
		probes = h.health.Probe(ctx)
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
	result := map[string]any{
		"lifecycle": h.machine.Snapshot(),
		"web": map[string]any{
			"listen":  cfg.Web.Listen,
			"threads": cfg.Web.Threads,
			"apps":    appSummary(cfg),
		},
		"identity": map[string]any{
			"nodeID":    cfg.Runtime.NodeID,
			"clusterID": cfg.Runtime.ClusterID,
		},
	}
	if stats := h.observabilityStats(); stats != nil {
		result["observability"] = stats
	}
	return result
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
			"threads": cfg.Web.Threads,
			"apps":    appSummary(cfg),
		},
	}
}

// appSummary returns the non-sensitive application view used by status and
// diagnostic output. Per-application environment values are never exposed.
func appSummary(cfg config.Config) []map[string]any {
	apps := make([]map[string]any, 0, len(cfg.Apps))
	for _, app := range cfg.Apps {
		prefix, err := config.NormalizePath(app.Path)
		if err != nil {
			prefix = app.Path
		}
		apps = append(apps, map[string]any{
			"id":      app.ID,
			"path":    prefix,
			"dir":     app.Dir,
			"enabled": app.Enabled,
		})
	}
	return apps
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
	observabilityBridge := web.ObservabilityBridgeOptions{}
	if len(h.obsApps) > 0 && candidate.Observability.Enabled {
		tokens := make([]string, 0, len(h.obsApps))
		for _, app := range h.obsApps {
			tokens = append(tokens, app.token)
		}
		observabilityBridge = web.ObservabilityBridgeOptions{
			Enabled: true,
			Listen:  candidate.Observability.BridgeListen,
			Tokens:  tokens,
		}
	}
	rendered, err := json.Marshal(webConfig(candidate, h.accessLogPath, h.accessLogKeepDays, observabilityBridge))
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

func webConfig(cfg config.Config, accessLogPath string, accessLogKeepDays int, observabilityBridge web.ObservabilityBridgeOptions) *caddy.Config {
	queueBridge := web.QueueBridgeOptions{}
	if cfg.Queue.Enabled {
		bridgeListen := queueBridgeListen(cfg)
		if cfg.LegacyApp() || len(cfg.Apps) == 0 {
			queueBridge = web.QueueBridgeOptions{Enabled: true, Listen: bridgeListen, Token: cfg.Queue.BridgeToken}
		} else {
			queueApps := make([]web.QueueBridgeApp, 0, len(cfg.Apps))
			for _, app := range cfg.Apps {
				if !app.Enabled || !app.Queue.Enabled {
					continue
				}
				prefix, err := config.NormalizePath(app.Path)
				if err != nil {
					prefix = app.Path
				}
				queueApps = append(queueApps, web.QueueBridgeApp{ID: app.ID, Prefix: prefix, Token: app.Queue.Token, Template: app.Queue.BridgeURLTemplate})
			}
			if len(queueApps) > 0 {
				queueBridge = web.QueueBridgeOptions{Enabled: true, Listen: bridgeListen, Apps: queueApps}
			}
		}
	}
	return web.Config(web.Options{
		Apps:                appRoutes(cfg),
		Listen:              cfg.Web.Listen,
		Threads:             cfg.Web.Threads,
		ReadHeaderTimeout:   cfg.Web.ReadHeaderTimeout,
		IdleTimeout:         cfg.Web.IdleTimeout,
		MaxHeaderBytes:      cfg.Web.MaxHeaderBytes,
		AccessLogPath:       accessLogPath,
		AccessLogKeepDays:   accessLogKeepDays,
		QueueBridge:         queueBridge,
		ObservabilityBridge: observabilityBridge,
	})
}

// appRoutes converts configured applications into web routes and injects the
// per-application data, session, temporary and log roots. Explicit
// application environment always wins over the generated defaults.
func appRoutes(cfg config.Config) []web.App {
	apps := make([]web.App, 0, len(cfg.Apps))
	for _, app := range cfg.Apps {
		if !app.Enabled {
			continue
		}
		env := make(map[string]string, len(app.Env)+6)
		for name, value := range app.Env {
			env[name] = value
		}
		prefix, err := config.NormalizePath(app.Path)
		if err != nil {
			prefix = app.Path
		}
		setEnvDefault(env, "ZT_WEB_ROOT", strings.Trim(prefix, "/"))
		setEnvDefault(env, "ZT_APP_ID", app.ID)
		setEnvDefault(env, "ZT_DATA_ROOT", app.DataDir)
		setEnvDefault(env, "ZT_TMP_ROOT", app.TmpDir)
		setEnvDefault(env, "ZT_SESSION_PATH", filepath.Join(app.DataDir, "session"))
		setEnvDefault(env, "ZT_APISESSION_PATH", filepath.Join(app.DataDir, "apisession"))
		setEnvDefault(env, "ZT_BACKUP_PATH", filepath.Join(app.DataDir, "backup"))
		setEnvDefault(env, "ZENTAO_LOG_DIR", filepath.Join(app.ObservabilityDir(), "raw-logs"))
		apps = append(apps, web.App{
			ID:        app.ID,
			Prefix:    prefix,
			Root:      app.DocumentRoot(),
			Env:       env,
			Available: appAvailable(app),
		})
	}
	return apps
}

// appAvailable reports whether the application document root is present. An
// unavailable application is served as a 503 placeholder; without this check
// the FrankenPHP handler would fail Caddy provisioning and abort startup.
func appAvailable(app config.App) bool {
	info, err := os.Stat(app.DocumentRoot())
	return err == nil && info.IsDir()
}

func setEnvDefault(env map[string]string, name, value string) {
	if _, exists := env[name]; exists {
		return
	}
	env[name] = value
}

// prepareApps creates the per-application data and temp roots. Missing
// application code is reported but does not abort startup.
func prepareApps(cfg config.Config) error {
	for _, app := range cfg.Apps {
		if !app.Enabled {
			continue
		}
		for _, dir := range []string{
			filepath.Join(app.DataDir, "upload"),
			filepath.Join(app.DataDir, "session"),
			filepath.Join(app.DataDir, "apisession"),
			filepath.Join(app.DataDir, "backup"),
			app.ObservabilityDir(),
			app.TmpDir,
		} {
			if err := os.MkdirAll(dir, 0o770); err != nil {
				return fmt.Errorf("prepare application %q directory %s: %w", app.ID, dir, err)
			}
		}
		root := app.DocumentRoot()
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			// Readiness reports the missing application; keep the Runtime up so
			// the application can be attached without a restart of the host.
			continue
		}
	}
	return nil
}

func callControl(socket string, request control.Request) (control.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return control.Call(ctx, socket, request)
}

func controlCommand(operation string, args []string) error {
	flags := flag.NewFlagSet(operation, flag.ContinueOnError)
	controlSocket := flags.String("control-socket", defaultControlSocketPath(), "Runtime control socket")
	pidFile := flags.String("pid-file", defaultPIDFilePath(), "PID file fallback")
	deep := flags.Bool("deep", operation == "diagnose", "request deep health")
	if err := flags.Parse(args); err != nil {
		return usageError("%v", err)
	}
	response, err := callControl(*controlSocket, control.Request{Operation: operation, Deep: *deep})
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
		return notRunningError("zentao is not running: %v", err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return notRunningError("zentao is not running: %v", err)
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
	if err := clearStalePIDFile(name); err != nil {
		return err
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

// clearStalePIDFile removes a PID file whose process is gone. A hard-killed
// Runtime (SIGKILL, power loss, timeout) cannot clean up after itself, and the
// leftover file must not block the next start. A live process keeps the file:
// the Runtime refuses to start twice from the same installation.
func clearStalePIDFile(name string) error {
	pid, err := readPIDFile(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		// Unreadable or corrupt PID file: treat it as stale.
		if removeErr := os.Remove(name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("remove unreadable PID file: %w", removeErr)
		}
		fmt.Fprintf(os.Stderr, "zentao: removed unreadable PID file %s\n", name)
		return nil
	}
	if processAlive(pid) {
		return fmt.Errorf("runtime is already running: pid %d\nstop it first, or use another installation root", pid)
	}
	if removeErr := os.Remove(name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fmt.Errorf("remove stale PID file: %w", removeErr)
	}
	fmt.Fprintf(os.Stderr, "zentao: removed stale PID file %s (pid %d is not running)\n", name, pid)
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
// ID persisted in <installRoot>/run/node-id so that log file names and
// Parquet partitions stay stable across restarts.
func resolveNodeID(cfg config.Config, installRoot string) (string, error) {
	if cfg.Runtime.NodeID != "" {
		return cfg.Runtime.NodeID, nil
	}
	runDir := filepath.Join(installRoot, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return "", fmt.Errorf("create run directory: %w", err)
	}
	nodeFile := filepath.Join(runDir, "node-id")
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
