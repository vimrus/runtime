package appprocess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vimrus/runtime/internal/config"
)

type processWorkerOptions struct {
	binary    string
	phpArgs   []string
	sdkPath   string
	bridgeURL string
	logPath   string
	logger    *slog.Logger
}

type processWorker struct {
	appID   string
	app     config.App
	config  config.AppProcess
	broker  *broker
	options processWorkerOptions

	initialDone chan struct{}
	initialOnce sync.Once
	stopCh      chan struct{}
	stopOnce    sync.Once
	runDone     chan struct{}
	restartCh   chan struct{}

	mu               sync.Mutex
	command          *exec.Cmd
	processDone      chan struct{}
	processExit      processExit
	restartRequested bool
	stopping         atomic.Bool
	log              *os.File
	platformCleanup  func()
}

type processExit struct {
	code int
	err  error
	at   time.Time
}

func newProcessWorker(app config.App, process config.AppProcess, options processWorkerOptions) *processWorker {
	if options.logger == nil {
		options.logger = slog.Default()
	}
	return &processWorker{
		appID:       app.ID,
		app:         app,
		config:      process,
		broker:      newBroker(app.ID, process),
		options:     options,
		initialDone: make(chan struct{}),
		stopCh:      make(chan struct{}),
		runDone:     make(chan struct{}),
		restartCh:   make(chan struct{}, 1),
	}
}

func (w *processWorker) key() string { return w.appID + "/" + w.config.Name }

func (w *processWorker) notifyInitial() {
	w.initialOnce.Do(func() { close(w.initialDone) })
}

func (w *processWorker) run(ctx context.Context) {
	defer close(w.runDone)
	defer w.broker.setState(StateStopped)

	var restartTimes []time.Time
	for {
		if w.isStopped() {
			return
		}
		result := w.runOnce(ctx)
		w.broker.failAttempt(result.instanceID, StateBackoff, result.exit.code, exitMessage(result.exit), result.exit.at)
		w.notifyInitial()
		if w.isStopped() {
			w.broker.setState(StateStopped)
			return
		}
		if w.takeRestartRequest() {
			restartTimes = nil
			continue
		}
		if !shouldRestart(w.config.Restart, result.exit) {
			if result.exit.err == nil && result.exit.code == 0 {
				w.broker.setState(StateStopped)
			} else {
				w.broker.setState(StateFailed)
			}
			if !w.waitForRestart(ctx) {
				return
			}
			restartTimes = nil
			continue
		}

		now := time.Now()
		windowStart := now.Add(-w.config.RestartWindow)
		kept := restartTimes[:0]
		for _, timestamp := range restartTimes {
			if timestamp.After(windowStart) {
				kept = append(kept, timestamp)
			}
		}
		restartTimes = kept
		if len(restartTimes) >= w.config.MaxRestarts {
			w.broker.setState(StateFailed)
			w.options.logger.Error("resident process restart limit reached",
				slog.String("app", w.appID),
				slog.String("process", w.config.Name),
				slog.Int("restarts", len(restartTimes)),
			)
			if !w.waitForRestart(ctx) {
				return
			}
			restartTimes = nil
			continue
		}
		restartTimes = append(restartTimes, now)
		w.broker.incrementRestarts()
		attempt := len(restartTimes)
		w.broker.setState(StateBackoff)
		delay := restartDelay(w.config, attempt)
		w.options.logger.Warn("resident process restarting",
			slog.String("app", w.appID),
			slog.String("process", w.config.Name),
			slog.Int("attempt", attempt),
			slog.Duration("backoff", delay),
			slog.Int("exitCode", result.exit.code),
			slog.Any("error", result.exit.err),
		)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-w.stopCh:
			timer.Stop()
			return
		case <-w.restartCh:
			timer.Stop()
			w.takeRestartRequest()
			restartTimes = nil
			continue
		case <-timer.C:
		}
	}
}

type attemptResult struct {
	instanceID string
	exit       processExit
}

func (w *processWorker) runOnce(ctx context.Context) attemptResult {
	if w.isStopped() {
		return attemptResult{exit: processExit{at: time.Now().UTC()}}
	}
	w.broker.setState(StateStarting)
	script, workDir, err := w.securePaths()
	if err != nil {
		w.options.logger.Error("resident process path validation failed",
			slog.String("app", w.appID),
			slog.String("process", w.config.Name),
			slog.Any("error", err),
		)
		return attemptResult{exit: processExit{code: -1, err: err, at: time.Now().UTC()}}
	}
	instanceID, err := randomToken()
	if err != nil {
		return attemptResult{exit: processExit{code: -1, err: err, at: time.Now().UTC()}}
	}
	workerToken, err := randomToken()
	if err != nil {
		return attemptResult{exit: processExit{code: -1, err: err, at: time.Now().UTC()}}
	}
	logFile, err := os.OpenFile(w.options.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return attemptResult{exit: processExit{code: -1, err: fmt.Errorf("open process log: %w", err), at: time.Now().UTC()}}
	}
	args := append([]string(nil), w.options.phpArgs...)
	args = append(args, "-d", "max_execution_time=0", script)
	args = append(args, w.config.Args...)
	command := exec.Command(w.options.binary, args...)
	command.Dir = workDir
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = w.environment(instanceID, workerToken)
	prepareCommand(command)
	w.options.logger.Info("starting resident process",
		slog.String("app", w.appID),
		slog.String("process", w.config.Name),
		slog.String("script", w.config.Script),
	)
	if err := command.Start(); err != nil {
		logFile.Close()
		return attemptResult{instanceID: instanceID, exit: processExit{code: -1, err: fmt.Errorf("start PHP process: %w", err), at: time.Now().UTC()}}
	}
	cleanup, err := afterStart(command)
	if err != nil {
		killProcessTree(command)
		_ = command.Wait()
		logFile.Close()
		return attemptResult{instanceID: instanceID, exit: processExit{code: -1, err: fmt.Errorf("isolate PHP process: %w", err), at: time.Now().UTC()}}
	}
	processDone := make(chan struct{})
	w.mu.Lock()
	w.command = command
	w.processDone = processDone
	w.processExit = processExit{}
	w.log = logFile
	w.platformCleanup = cleanup
	restartRequested := w.restartRequested
	w.mu.Unlock()
	if restartRequested {
		killProcessTree(command)
	}
	go func() {
		waitErr := command.Wait()
		exit := processExit{code: command.ProcessState.ExitCode(), err: waitErr, at: time.Now().UTC()}
		w.mu.Lock()
		w.processExit = exit
		close(processDone)
		w.mu.Unlock()
	}()
	ready := w.broker.beginAttempt(instanceID, workerToken, command.Process.Pid, time.Now().UTC())
	w.options.logger.Info("resident process spawned",
		slog.String("app", w.appID),
		slog.String("process", w.config.Name),
		slog.Int("pid", command.Process.Pid),
		slog.String("instanceID", instanceID),
	)

	readyTimer := time.NewTimer(w.config.ReadyTimeout)
	select {
	case <-ctx.Done():
		readyTimer.Stop()
		killProcessTree(command)
	case <-w.stopCh:
		readyTimer.Stop()
		killProcessTree(command)
	case <-processDone:
		readyTimer.Stop()
	case <-readyTimer.C:
		w.options.logger.Error("resident process ready timeout",
			slog.String("app", w.appID),
			slog.String("process", w.config.Name),
		)
		killProcessTree(command)
	case <-ready:
		readyTimer.Stop()
		w.notifyInitial()
		select {
		case <-ctx.Done():
			killProcessTree(command)
		case <-w.stopCh:
			wait := time.NewTimer(w.config.StopTimeout)
			select {
			case <-processDone:
				wait.Stop()
			case <-wait.C:
				killProcessTree(command)
			}
		case <-processDone:
		}
	}

	select {
	case <-processDone:
	case <-time.After(5 * time.Second):
		killProcessTree(command)
		<-processDone
	}
	w.mu.Lock()
	exit := w.processExit
	if exit.at.IsZero() {
		exit = processExit{code: -1, err: errors.New("process exited without wait status"), at: time.Now().UTC()}
	}
	cleanupFn := w.platformCleanup
	log := w.log
	w.command = nil
	w.processDone = nil
	w.platformCleanup = nil
	w.log = nil
	w.mu.Unlock()
	if cleanupFn != nil {
		cleanupFn()
	}
	if log != nil {
		_ = log.Close()
	}
	w.options.logger.Warn("resident process exited",
		slog.String("app", w.appID),
		slog.String("process", w.config.Name),
		slog.Int("exitCode", exit.code),
		slog.Any("error", exit.err),
	)
	return attemptResult{instanceID: instanceID, exit: exit}
}

func (w *processWorker) stop(ctx context.Context) error {
	w.stopOnce.Do(func() {
		w.stopping.Store(true)
		w.broker.beginDrain()
		close(w.stopCh)
		signal(w.restartCh)
	})
	select {
	case <-w.runDone:
		return nil
	case <-ctx.Done():
		w.mu.Lock()
		command := w.command
		w.mu.Unlock()
		if command != nil {
			killProcessTree(command)
		}
		select {
		case <-w.runDone:
		case <-time.After(5 * time.Second):
		}
		return ctx.Err()
	}
}

func (w *processWorker) restart() error {
	select {
	case <-w.stopCh:
		return errors.New("resident process manager is stopping")
	default:
	}
	w.mu.Lock()
	w.restartRequested = true
	command := w.command
	w.mu.Unlock()
	signal(w.restartCh)
	if command != nil {
		killProcessTree(command)
	}
	return nil
}

func (w *processWorker) isStopped() bool {
	if w.stopping.Load() {
		return true
	}
	select {
	case <-w.stopCh:
		return true
	default:
		return false
	}
}

func (w *processWorker) takeRestartRequest() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	requested := w.restartRequested
	w.restartRequested = false
	return requested
}

func (w *processWorker) waitForRestart(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-w.stopCh:
		return false
	case <-w.restartCh:
		w.takeRestartRequest()
		return true
	}
}

func (w *processWorker) securePaths() (string, string, error) {
	root, err := filepath.EvalSymlinks(w.app.Dir)
	if err != nil {
		return "", "", fmt.Errorf("resolve application root: %w", err)
	}
	script, err := filepath.EvalSymlinks(w.app.ScriptPath(w.config))
	if err != nil {
		return "", "", fmt.Errorf("resolve process script: %w", err)
	}
	workDir, err := filepath.EvalSymlinks(w.app.ProcessWorkingDir(w.config))
	if err != nil {
		return "", "", fmt.Errorf("resolve process working directory: %w", err)
	}
	for label, path := range map[string]string{"script": script, "workDir": workDir} {
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", "", fmt.Errorf("process %s escapes application root", label)
		}
	}
	return script, workDir, nil
}

func (w *processWorker) environment(instanceID, workerToken string) []string {
	values := make(map[string]string, len(w.app.Env)+16)
	for name, value := range w.app.Env {
		values[name] = value
	}
	prefix, _ := config.NormalizePath(w.app.Path)
	setDefault(values, "ZT_WEB_ROOT", strings.Trim(prefix, "/"))
	setDefault(values, "ZT_APP_ID", w.app.ID)
	setDefault(values, "ZT_DATA_ROOT", w.app.DataDir)
	setDefault(values, "ZT_TMP_ROOT", w.app.TmpDir)
	setDefault(values, "ZT_SESSION_PATH", filepath.Join(w.app.DataDir, "session"))
	setDefault(values, "ZT_APISESSION_PATH", filepath.Join(w.app.DataDir, "apisession"))
	setDefault(values, "ZT_BACKUP_PATH", filepath.Join(w.app.DataDir, "backup"))
	setDefault(values, "ZENTAO_LOG_DIR", filepath.Join(w.app.ObservabilityDir(), "raw-logs"))

	// The web process receives the app-level call token, while the child gets
	// only its own instance token.
	delete(values, "ZENTAO_PROCESS_CALL_TOKEN")
	values["ZENTAO_PROCESS_BRIDGE_URL"] = strings.TrimRight(w.options.bridgeURL, "/")
	values["ZENTAO_PROCESS_APP_ID"] = w.app.ID
	values["ZENTAO_PROCESS_SDK"] = w.options.sdkPath
	values["ZENTAO_PROCESS_SCHEMA"] = strconv.Itoa(SchemaVersion)
	values["ZENTAO_PROCESS_NAME"] = w.config.Name
	values["ZENTAO_PROCESS_INSTANCE_ID"] = instanceID
	values["ZENTAO_PROCESS_WORKER_TOKEN"] = workerToken
	environment := mergeEnvironment(os.Environ(), values)
	filtered := environment[:0]
	for _, entry := range environment {
		if strings.HasPrefix(entry, "ZENTAO_PROCESS_CALL_TOKEN=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func setDefault(values map[string]string, name, value string) {
	if _, exists := values[name]; !exists {
		values[name] = value
	}
}

func shouldRestart(policy string, exit processExit) bool {
	switch policy {
	case "always":
		return true
	case "never":
		return false
	default:
		return exit.err != nil || exit.code != 0
	}
}

func restartDelay(config config.AppProcess, attempt int) time.Duration {
	delay := config.MinBackoff
	for count := 1; count < attempt && delay < config.MaxBackoff; count++ {
		delay *= 2
	}
	if delay > config.MaxBackoff {
		delay = config.MaxBackoff
	}
	if delay <= 0 {
		return time.Second
	}
	jitter := time.Duration(rand.Int64N(int64(delay/4) + 1))
	return delay + jitter
}

func exitMessage(exit processExit) string {
	if exit.err != nil {
		return exit.err.Error()
	}
	if exit.code != 0 {
		return "process exited with code " + strconv.Itoa(exit.code)
	}
	return ""
}
