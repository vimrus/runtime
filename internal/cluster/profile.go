package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/vimrus/runtime/internal/config"
)

const ProfileSchemaVersion = 1

const ObservabilityStorageKey = "__observability__"

// DigestSet separates critical semantics, behavioral defaults and capacity.
type DigestSet struct {
	Critical   string `json:"critical,omitempty"`
	Behavioral string `json:"behavioral,omitempty"`
	Capacity   string `json:"capacity,omitempty"`
}

// NodeProfile identifies one Runtime boot.
type NodeProfile struct {
	NodeID         string `json:"nodeID"`
	InstanceID     string `json:"instanceID"`
	BootID         string `json:"bootID"`
	RuntimeVersion string `json:"runtimeVersion"`
	ReleaseID      string `json:"releaseID"`
}

// RuntimeDomains contains only the Runtime-owned part of the configuration.
type RuntimeDomains struct {
	Domains      map[string]DigestSet `json:"domains"`
	Summary      map[string]any       `json:"summary"`
	Capabilities map[string]bool      `json:"capabilities"`
}

// AppProfile is the Runtime-visible portion of one hosted application.
type AppProfile struct {
	AppID          string               `json:"appID"`
	Path           string               `json:"path"`
	Enabled        bool                 `json:"enabled"`
	QueueEnabled   bool                 `json:"queueEnabled"`
	StorageID      string               `json:"storageID"`
	Domains        map[string]DigestSet `json:"domains"`
	RuntimeSummary map[string]any       `json:"runtimeSummary,omitempty"`
}

// Profile is sent to the ZenTao Runtime Profile Bridge.
type Profile struct {
	SchemaVersion  int                   `json:"schemaVersion"`
	ProfileVersion int                   `json:"profileVersion"`
	Node           NodeProfile           `json:"node"`
	Runtime        RuntimeDomains        `json:"runtime"`
	Apps           map[string]AppProfile `json:"apps"`
}

// BuildProfile creates deterministic Runtime and app digests. Database,
// session, cache and PHP extension domains are filled by the PHP side.
func BuildProfile(cfg config.Config, nodeID, instanceID, bootID, runtimeVersion string, storage map[string]StorageIdentity) Profile {
	apps := make(map[string]AppProfile)
	appIDs := make([]string, 0, len(cfg.Apps))
	for _, app := range cfg.ActiveApps() {
		appIDs = append(appIDs, app.ID)
		appPath, _ := config.NormalizePath(app.Path)
		identity := storage[app.ID]
		apps[app.ID] = AppProfile{
			AppID:        app.ID,
			Path:         appPath,
			Enabled:      app.Enabled,
			QueueEnabled: app.Queue.Enabled,
			StorageID:    identity.StorageID,
			Domains: map[string]DigestSet{
				"storage": digestSet(
					map[string]any{
						"storageID":  identity.StorageID,
						"purpose":    identity.Purpose,
						"generation": identity.Generation,
					},
					nil,
					nil,
				),
				"processes": digestSet(
					appProcessProfile(app.Processes),
					appProcessBehavior(app.Processes),
					map[string]any{
						"maxPending": processCapacity(app.Processes),
					},
				),
			},
			RuntimeSummary: map[string]any{
				"path":         appPath,
				"queueEnabled": app.Queue.Enabled,
				"processes":    processNames(app.Processes),
			},
		}
	}
	sort.Strings(appIDs)

	runtimeDomains := map[string]DigestSet{
		"cluster": digestSet(
			map[string]any{
				"configSchema":      cfg.SchemaVersion,
				"releaseID":         cfg.Cluster.ReleaseID,
				"runtimeVersion":    runtimeVersion,
				"requireSharedData": cfg.Cluster.RequireSharedData,
				"apps":              appIDs,
			},
			map[string]any{
				"registrationTimeout": cfg.Cluster.RegistrationTimeout.String(),
				"heartbeatInterval":   cfg.Cluster.HeartbeatInterval.String(),
				"profileInterval":     cfg.Cluster.ProfileInterval.String(),
				"staleTimeout":        cfg.Cluster.StaleTimeout.String(),
			},
			nil,
		),
		"runtime": digestSet(
			map[string]any{
				"runtimeVersion": runtimeVersion,
				"logLevel":       cfg.Runtime.LogLevel,
			},
			map[string]any{
				"drainTimeout":          cfg.Runtime.DrainTimeout.String(),
				"readHeaderTimeout":     cfg.Web.ReadHeaderTimeout.String(),
				"idleTimeout":           cfg.Web.IdleTimeout.String(),
				"maxHeaderBytes":        cfg.Web.MaxHeaderBytes,
				"logMaxBytes":           cfg.Runtime.LogMaxBytes,
				"logMaxBackups":         cfg.Runtime.LogMaxBackups,
				"phpErrorLogMaxBytes":   cfg.Runtime.PHPErrorLogMaxBytes,
				"phpErrorLogMaxBackups": cfg.Runtime.PHPErrorLogMaxBackups,
				"auditLogMaxBytes":      cfg.Runtime.AuditLogMaxBytes,
				"auditLogMaxBackups":    cfg.Runtime.AuditLogMaxBackups,
				"auditLogKeepDays":      cfg.Runtime.AuditLogKeepDays,
			},
			map[string]any{"threads": cfg.Web.Threads},
		),
		"queue": digestSet(
			map[string]any{
				"enabled":      cfg.Queue.Enabled,
				"appQueueURLs": appQueueTemplates(cfg.ActiveApps()),
			},
			map[string]any{
				"requestTimeout":    cfg.Queue.RequestTimeout.String(),
				"claimBatch":        cfg.Queue.ClaimBatch,
				"leaseSeconds":      cfg.Queue.LeaseSeconds,
				"heartbeatInterval": cfg.Queue.HeartbeatInterval.String(),
				"reapInterval":      cfg.Queue.ReapInterval.String(),
				"reapBatch":         cfg.Queue.ReapBatch,
				"drainTimeout":      cfg.Queue.DrainTimeout.String(),
				"workerPoll":        queuePollBehavior(cfg.Queue.Workers),
			},
			queueCapacity(cfg.Queue.Workers),
		),
		"observability": observabilityDigest(cfg.Observability, storage[ObservabilityStorageKey]),
		"olap":          olapDigest(cfg.OLAP),
		"security":      digestSet(map[string]any{}, nil, nil),
	}

	return Profile{
		SchemaVersion:  ProfileSchemaVersion,
		ProfileVersion: 1,
		Node: NodeProfile{
			NodeID:         nodeID,
			InstanceID:     instanceID,
			BootID:         bootID,
			RuntimeVersion: runtimeVersion,
			ReleaseID:      cfg.Cluster.ReleaseID,
		},
		Runtime: RuntimeDomains{
			Domains: runtimeDomains,
			Summary: map[string]any{
				"webThreads":         cfg.Web.Threads,
				"queueEnabled":       cfg.Queue.Enabled,
				"observability":      cfg.Observability.Enabled,
				"olapEnabled":        cfg.OLAP.Enabled,
				"releaseID":          cfg.Cluster.ReleaseID,
				"runtimeVersion":     runtimeVersion,
				"sharedDataRequired": cfg.Cluster.RequireSharedData,
			},
			Capabilities: map[string]bool{
				"duckdb":     true,
				"queue":      cfg.Queue.Enabled,
				"olap":       cfg.OLAP.Enabled,
				"logs":       cfg.Observability.Enabled,
				"sharedData": cfg.Cluster.RequireSharedData,
				"processes":  configuredProcessCount(cfg.ActiveApps()) > 0,
			},
		},
		Apps: apps,
	}
}

func appProcessBehavior(processes []config.AppProcess) map[string]any {
	result := make(map[string]any, len(processes))
	for _, process := range processes {
		if !process.Enabled {
			continue
		}
		result[process.Name] = map[string]any{
			"workDir":        process.WorkDir,
			"readyTimeout":   process.ReadyTimeout.String(),
			"requestTimeout": process.RequestTimeout.String(),
			"stopTimeout":    process.StopTimeout.String(),
			"restart":        process.Restart,
			"maxRestarts":    process.MaxRestarts,
			"restartWindow":  process.RestartWindow.String(),
			"minBackoff":     process.MinBackoff.String(),
			"maxBackoff":     process.MaxBackoff.String(),
			"args":           append([]string(nil), process.Args...),
		}
	}
	return result
}

func appProcessProfile(processes []config.AppProcess) map[string]any {
	result := make([]string, 0, len(processes))
	for _, process := range processes {
		if !process.Enabled {
			continue
		}
		result = append(result, process.Name+":"+process.Script+":"+strconv.FormatBool(process.Required))
	}
	sort.Strings(result)
	return map[string]any{"configured": result}
}

func processNames(processes []config.AppProcess) []string {
	result := make([]string, 0, len(processes))
	for _, process := range processes {
		if process.Enabled {
			result = append(result, process.Name)
		}
	}
	sort.Strings(result)
	return result
}

func processCapacity(processes []config.AppProcess) map[string]int {
	result := make(map[string]int, len(processes))
	for _, process := range processes {
		if process.Enabled {
			result[process.Name] = process.MaxPending
		}
	}
	return result
}

func configuredProcessCount(apps []config.App) int {
	count := 0
	for _, app := range apps {
		for _, process := range app.Processes {
			if process.Enabled {
				count++
			}
		}
	}
	return count
}

func queuePollBehavior(workers []config.QueueWorker) map[string]any {
	result := make(map[string]any, len(workers))
	for _, worker := range workers {
		result[worker.Name] = map[string]string{
			"minPoll": worker.MinPoll.String(),
			"maxPoll": worker.MaxPoll.String(),
		}
	}
	return result
}

func observabilityDigest(value config.Observability, identity StorageIdentity) DigestSet {
	return digestSet(
		map[string]any{
			"enabled":           value.Enabled,
			"logDays":           value.LogDays,
			"metricsDays":       value.MetricsDays,
			"convertSources":    stringSlice(value.JSONLConvertSources),
			"storageID":         identity.StorageID,
			"storagePurpose":    identity.Purpose,
			"storageGeneration": identity.Generation,
		},
		map[string]any{
			"maxBatchRows":            value.MaxBatchRows,
			"maxBatchBytes":           value.MaxBatchBytes,
			"minBatchRows":            value.MinBatchRows,
			"minBatchBytes":           value.MinBatchBytes,
			"flushInterval":           value.FlushInterval.String(),
			"maxFlushInterval":        value.MaxFlushInterval.String(),
			"jsonlConvertInterval":    value.JSONLConvertInterval.String(),
			"jsonlKeepDays":           value.JSONLKeepDays,
			"jsonlDeleteAfterConvert": value.JSONLDeleteAfterConvert,
		},
		map[string]any{
			"maxSpoolBytes": value.MaxSpoolBytes,
		},
	)
}

func olapDigest(value config.OLAP) DigestSet {
	return digestSet(
		map[string]any{
			"enabled":      value.Enabled,
			"maxRows":      value.MaxRows,
			"queryTimeout": value.QueryTimeout.String(),
		},
		map[string]any{
			"maxConcurrentQueries": value.MaxConcurrentQueries,
		},
		map[string]any{
			"queryThreads":     value.QueryThreads,
			"queryMemoryLimit": value.QueryMemoryLimit,
		},
	)
}

func digestSet(critical, behavioral, capacity map[string]any) DigestSet {
	return DigestSet{
		Critical:   digestValue(critical),
		Behavioral: digestValue(behavioral),
		Capacity:   digestValue(capacity),
	}
}

func digestValue(value map[string]any) string {
	if len(value) == 0 {
		return ""
	}
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func appQueueTemplates(apps []config.App) []string {
	result := make([]string, 0, len(apps))
	for _, app := range apps {
		if app.Queue.Enabled {
			result = append(result, app.ID+":"+app.Queue.BridgeURLTemplate)
		}
	}
	sort.Strings(result)
	return result
}

func queueCapacity(workers []config.QueueWorker) map[string]any {
	result := make(map[string]int, len(workers))
	for _, worker := range workers {
		result[worker.Name] = worker.Concurrency
	}
	return map[string]any{"workers": result}
}

func stringSlice(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

// ProfileAge is used by callers that cache the last successful registration.
func ProfileAge(now, last time.Time) time.Duration {
	if last.IsZero() {
		return 0
	}
	return now.Sub(last)
}
