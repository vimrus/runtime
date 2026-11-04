package appprocess

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vimrus/runtime/internal/config"
	"github.com/vimrus/runtime/internal/health"
)

type Options struct {
	PHPBinary string
	SDKPath   string
	NodeID    string
	PHPArgs   []string
	Logger    *slog.Logger
}

// Manager owns the private Process Bridge and all configured resident PHP
// processes for one Runtime boot.
type Manager struct {
	url       string
	listener  net.Listener
	server    *http.Server
	workers   map[string]*processWorker
	callToken map[string]string

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// Prepare validates runtime-owned process prerequisites, creates the loopback
// bridge and injects application-side SDK credentials before Caddy is
// rendered. Application or script absence remains an application health
// failure rather than a Host startup failure.
func Prepare(cfg *config.Config, options Options) (*Manager, error) {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	manager := &Manager{
		workers:   make(map[string]*processWorker),
		callToken: make(map[string]string),
	}
	count := 0
	for index := range cfg.Apps {
		app := &cfg.Apps[index]
		if !app.Enabled {
			continue
		}
		for _, process := range app.Processes {
			if process.Enabled {
				count++
			}
		}
	}
	if count == 0 {
		return manager, nil
	}
	if options.PHPBinary == "" {
		return nil, errors.New("PHP CLI binary is required when application processes are configured")
	}
	if !filepath.IsAbs(options.PHPBinary) {
		return nil, errors.New("PHP CLI binary path must be absolute")
	}
	if options.SDKPath == "" || !filepath.IsAbs(options.SDKPath) {
		return nil, errors.New("resident process SDK path must be absolute")
	}
	if info, err := os.Stat(options.SDKPath); err != nil || info.IsDir() {
		return nil, fmt.Errorf("resident process SDK is not available at %s", options.SDKPath)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on process bridge: %w", err)
	}
	manager.listener = listener
	manager.url = "http://" + listener.Addr().String() + BasePath

	for index := range cfg.Apps {
		app := &cfg.Apps[index]
		if !app.Enabled {
			continue
		}
		hasProcess := false
		for _, process := range app.Processes {
			if process.Enabled {
				hasProcess = true
				break
			}
		}
		if !hasProcess {
			continue
		}
		token, err := randomToken()
		if err != nil {
			listener.Close()
			return nil, fmt.Errorf("generate process call token for %s: %w", app.ID, err)
		}
		manager.callToken[app.ID] = token
		if app.Env == nil {
			app.Env = make(map[string]string)
		}
		app.Env["ZENTAO_PROCESS_BRIDGE_URL"] = manager.url
		app.Env["ZENTAO_PROCESS_APP_ID"] = app.ID
		app.Env["ZENTAO_PROCESS_CALL_TOKEN"] = token
		app.Env["ZENTAO_PROCESS_SDK"] = options.SDKPath
		app.Env["ZENTAO_PROCESS_SCHEMA"] = strconv.Itoa(SchemaVersion)

		for _, process := range app.Processes {
			if !process.Enabled {
				continue
			}
			logPath := filepath.Join(
				app.ObservabilityDir(),
				"raw-logs",
				"processes",
				safeFilePart(app.ID)+"-"+safeFilePart(process.Name)+"-"+safeFilePart(options.NodeID)+".log",
			)
			if err := os.MkdirAll(filepath.Dir(logPath), 0o770); err != nil {
				listener.Close()
				return nil, fmt.Errorf("create process log directory for %s/%s: %w", app.ID, process.Name, err)
			}
			worker := newProcessWorker(*app, process, processWorkerOptions{
				binary:    options.PHPBinary,
				phpArgs:   append([]string(nil), options.PHPArgs...),
				sdkPath:   options.SDKPath,
				bridgeURL: manager.url,
				logPath:   logPath,
				logger:    options.Logger,
			})
			manager.workers[worker.key()] = worker
		}
	}

	manager.server = &http.Server{
		Handler:           manager.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	return manager, nil
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	if len(m.workers) == 0 {
		m.started = true
		m.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.started = true
	m.mu.Unlock()

	serverErr := make(chan error, 1)
	go func() {
		if err := m.server.Serve(m.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()
	for _, worker := range m.workers {
		worker := worker
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			worker.run(runCtx)
		}()
	}

	initial := make(chan struct{})
	var initialWG sync.WaitGroup
	for _, worker := range m.workers {
		worker := worker
		if !worker.config.Required {
			continue
		}
		initialWG.Add(1)
		go func() {
			defer initialWG.Done()
			select {
			case <-worker.initialDone:
			case <-runCtx.Done():
			}
		}()
	}
	go func() {
		initialWG.Wait()
		close(initial)
	}()
	select {
	case err := <-serverErr:
		return fmt.Errorf("serve process bridge: %w", err)
	case <-initial:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop drains every process, waits for protocol-driven shutdown and only then
// force-terminates processes that exceed their configured deadline.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if !m.started || m.stopped {
		m.mu.Unlock()
		return nil
	}
	m.stopped = true
	cancel := m.cancel
	m.mu.Unlock()

	var wg sync.WaitGroup
	errs := make(chan error, len(m.workers))
	for _, worker := range m.workers {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := worker.stop(ctx); err != nil {
				errs <- fmt.Errorf("%s: %w", worker.key(), err)
			}
		}()
	}
	wg.Wait()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
	if m.server == nil || m.listener == nil {
		return nil
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = m.server.Shutdown(closeCtx)
	closeCancel()
	_ = m.listener.Close()
	close(errs)
	var result error
	for err := range errs {
		result = errors.Join(result, err)
	}
	return result
}

func (m *Manager) Statuses() []Status {
	statuses := make([]Status, 0, len(m.workers))
	for _, worker := range m.workers {
		statuses = append(statuses, worker.broker.status())
	}
	sortStatuses(statuses)
	return statuses
}

func (m *Manager) LogPaths() []string {
	paths := make([]string, 0, len(m.workers))
	for _, worker := range m.workers {
		paths = append(paths, worker.options.logPath)
	}
	sortStrings(paths)
	return paths
}

func (m *Manager) RegisterHealth(registry *health.Registry) {
	for _, worker := range m.workers {
		worker := worker
		name := "process:" + worker.appID + ":" + worker.config.Name
		registry.Register(name, health.KindProcess, worker.config.Required, false, func(context.Context) health.Result {
			status := worker.broker.status()
			result := health.Result{Name: name, Kind: health.KindProcess, Status: health.StatusOK}
			switch status.State {
			case StateReady, StateBusy:
				if status.Overdue {
					result.Status = health.StatusDegraded
					result.Message = "busy beyond request timeout"
				}
				return result
			case StateStarting, StateHandshaking, StateBackoff, StateDraining:
				result.Status = health.StatusDegraded
				result.Message = string(status.State)
			default:
				result.Status = health.StatusFailed
				result.Message = status.LastError
				if result.Message == "" {
					result.Message = string(status.State)
				}
			}
			return result
		})
	}
}

func (m *Manager) Restart(appID, name string) error {
	worker, ok := m.workers[appID+"/"+name]
	if !ok {
		return errors.New("resident process is not configured")
	}
	return worker.restart()
}

func (m *Manager) handler() http.Handler {
	mux := http.NewServeMux()
	prefix := BasePath + "/apps/{app}/{process}/"
	mux.HandleFunc("POST "+prefix+"call", m.handleCall)
	mux.HandleFunc("POST "+prefix+"ready", m.handleReady)
	mux.HandleFunc("POST "+prefix+"next", m.handleNext)
	mux.HandleFunc("POST "+prefix+"reply", m.handleReply)
	return mux
}

func (m *Manager) handleCall(writer http.ResponseWriter, request *http.Request) {
	appID := request.PathValue("app")
	processName := request.PathValue("process")
	worker, ok := m.workers[appID+"/"+processName]
	if !ok {
		writeError(writer, "", newProtocolError("process_not_found", "process is not configured", "not_delivered", http.StatusNotFound))
		return
	}
	if !tokenMatches(m.callToken[appID], request.Header.Get(CallTokenHeader)) {
		writeError(writer, "", newProtocolError("unauthenticated", "process authentication required", "not_delivered", http.StatusUnauthorized))
		return
	}
	var payload CallRequest
	if err := decodeStrict(writer, request, &payload); err != nil {
		writeError(writer, payload.RequestID, err)
		return
	}
	if err := validateCallRequest(payload); err != nil {
		writeError(writer, payload.RequestID, err)
		return
	}
	response, err := worker.broker.call(request.Context(), payload)
	if err != nil {
		writeError(writer, payload.RequestID, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (m *Manager) handleReady(writer http.ResponseWriter, request *http.Request) {
	worker, token, ok := m.workerFromRequest(request, writer)
	if !ok {
		return
	}
	var payload ReadyRequest
	if err := decodeStrict(writer, request, &payload); err != nil {
		writeError(writer, payload.InstanceID, err)
		return
	}
	if payload.Schema != SchemaVersion || !validProtocolToken(payload.InstanceID, 128) {
		writeError(writer, payload.InstanceID, newProtocolError("invalid_request", "invalid ready request", "", http.StatusBadRequest))
		return
	}
	if err := worker.broker.ready(payload.InstanceID, token, time.Now().UTC()); err != nil {
		writeError(writer, payload.InstanceID, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"schema": SchemaVersion, "ok": true})
}

func (m *Manager) handleNext(writer http.ResponseWriter, request *http.Request) {
	worker, token, ok := m.workerFromRequest(request, writer)
	if !ok {
		return
	}
	var payload NextRequest
	if err := decodeStrict(writer, request, &payload); err != nil {
		writeError(writer, payload.InstanceID, err)
		return
	}
	if payload.Schema != SchemaVersion || !validProtocolToken(payload.InstanceID, 128) {
		writeError(writer, payload.InstanceID, newProtocolError("invalid_request", "invalid next request", "", http.StatusBadRequest))
		return
	}
	response, err := worker.broker.next(request.Context(), payload.InstanceID, token, defaultPollTimeout*time.Millisecond)
	if err != nil {
		writeError(writer, payload.InstanceID, err)
		return
	}
	if response == nil {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (m *Manager) handleReply(writer http.ResponseWriter, request *http.Request) {
	worker, token, ok := m.workerFromRequest(request, writer)
	if !ok {
		return
	}
	var payload ReplyRequest
	if err := decodeStrict(writer, request, &payload); err != nil {
		writeError(writer, payload.RequestID, err)
		return
	}
	if payload.Schema != SchemaVersion || !validProtocolToken(payload.InstanceID, 128) ||
		!validProtocolToken(payload.RequestID, 128) {
		writeError(writer, payload.RequestID, newProtocolError("invalid_request", "invalid reply request", "", http.StatusBadRequest))
		return
	}
	if payload.Error != nil && len(payload.Result) > 0 {
		writeError(writer, payload.RequestID, newProtocolError("invalid_request", "reply must contain result or error, not both", "", http.StatusBadRequest))
		return
	}
	if payload.Error != nil && !validProtocolToken(payload.Error.Code, 128) {
		writeError(writer, payload.RequestID, newProtocolError("invalid_request", "reply error code is required", "", http.StatusBadRequest))
		return
	}
	if err := worker.broker.reply(payload.InstanceID, token, payload); err != nil {
		writeError(writer, payload.RequestID, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"schema": SchemaVersion, "ok": true})
}

func (m *Manager) workerFromRequest(request *http.Request, writer http.ResponseWriter) (*processWorker, string, bool) {
	appID := request.PathValue("app")
	processName := request.PathValue("process")
	worker, ok := m.workers[appID+"/"+processName]
	if !ok {
		writeError(writer, "", newProtocolError("process_not_found", "process is not configured", "", http.StatusNotFound))
		return nil, "", false
	}
	token := request.Header.Get(CallTokenHeader)
	if token == "" {
		writeError(writer, "", newProtocolError("unauthenticated", "process authentication required", "", http.StatusUnauthorized))
		return nil, "", false
	}
	return worker, token, true
}

func validateCallRequest(request CallRequest) error {
	if request.Schema != SchemaVersion {
		return newProtocolError("unsupported_schema", "unsupported process bridge schema", "not_delivered", http.StatusBadRequest)
	}
	if !validProtocolToken(request.RequestID, 128) ||
		!validProtocolToken(request.Method, 128) {
		return newProtocolError("invalid_request", "requestId and method are required", "not_delivered", http.StatusBadRequest)
	}
	if request.TimeoutMS < 0 || request.TimeoutMS > int((24*time.Hour)/time.Millisecond) {
		return newProtocolError("invalid_request", "timeoutMs is out of range", "not_delivered", http.StatusBadRequest)
	}
	return nil
}

func validProtocolToken(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func randomToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func sortStatuses(statuses []Status) {
	// Keep the control output deterministic without pulling process order into
	// the broker map.
	for index := 1; index < len(statuses); index++ {
		for current := index; current > 0; current-- {
			left := statuses[current-1]
			right := statuses[current]
			if left.AppID < right.AppID || (left.AppID == right.AppID && left.Name <= right.Name) {
				break
			}
			statuses[current-1], statuses[current] = right, left
		}
	}
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for current := index; current > 0 && values[current] < values[current-1]; current-- {
			values[current], values[current-1] = values[current-1], values[current]
		}
	}
}

func safeFilePart(value string) string {
	if value == "" {
		return "local"
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			builder.WriteRune(character)
			continue
		}
		builder.WriteByte('_')
	}
	return builder.String()
}

func mergeEnvironment(base []string, values map[string]string) []string {
	merged := make(map[string]string, len(base)+len(values))
	for _, entry := range base {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			merged[name] = value
		}
	}
	for name, value := range values {
		merged[name] = value
	}
	result := make([]string, 0, len(merged))
	for name, value := range merged {
		result = append(result, name+"="+value)
	}
	return result
}
