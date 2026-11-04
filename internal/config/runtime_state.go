package config

var managedAppEnvKeys = []string{
	"ZENTAO_QUEUE_TOKEN",
	"ZENTAO_OBSERVABILITY_URL",
	"ZENTAO_OBSERVABILITY_TOKEN",
	"ZENTAO_OBSERVABILITY_SCHEMA",
	"ZENTAO_OBSERVABILITY_QUERY_URL",
	"ZENTAO_OBSERVABILITY_QUERY_TOKEN",
	"ZENTAO_LOG_MODE",
	"ZENTAO_OLAP_PUBLISH_URL",
	"ZENTAO_OLAP_QUERY_URL",
	"ZENTAO_OLAP_TOKEN",
	"ZENTAO_OLAP_STAGING_DIR",
	"ZENTAO_OLAP_SCHEMA",
	"ZENTAO_OLAP_MAX_ROWS",
	"ZENTAO_RUNTIME_PROFILE_TOKEN",
	"ZENTAO_RUNTIME_PROFILE_SCHEMA",
	"ZENTAO_CLUSTER_RELEASE_ID",
	"ZENTAO_CLUSTER_STORAGE_ID",
	"ZENTAO_CLUSTER_PROFILE_KEY",
	"ZENTAO_CLUSTER_STALE_TIMEOUT",
	"ZENTAO_CLUSTER_TAKEOVER_TIMEOUT",
	"ZENTAO_PROCESS_BRIDGE_URL",
	"ZENTAO_PROCESS_APP_ID",
	"ZENTAO_PROCESS_CALL_TOKEN",
	"ZENTAO_PROCESS_SDK",
	"ZENTAO_PROCESS_SCHEMA",
}

// PreserveRuntimeIdentity carries startup-resolved defaults into a reload
// candidate when the file leaves them unset.
func PreserveRuntimeIdentity(current, candidate Config) Config {
	if candidate.Cluster.ReleaseID == "" {
		candidate.Cluster.ReleaseID = current.Cluster.ReleaseID
	}
	return candidate
}

// PreserveManagedAppState carries runtime-generated bridge credentials from the
// active configuration into a reload candidate. These values are not user
// configuration and must not trigger a restart or drift after a reload.
func PreserveManagedAppState(current, candidate Config) Config {
	currentApps := make(map[string]App, len(current.Apps))
	for _, app := range current.Apps {
		currentApps[app.ID] = app
	}
	for index := range candidate.Apps {
		app := &candidate.Apps[index]
		existing, ok := currentApps[app.ID]
		if !ok {
			continue
		}
		app.Queue.Token = existing.Queue.Token
		if app.Env == nil {
			app.Env = make(map[string]string)
		}
		for _, name := range managedAppEnvKeys {
			if value, exists := existing.Env[name]; exists {
				app.Env[name] = value
			} else {
				delete(app.Env, name)
			}
		}
		if len(app.Env) == 0 {
			app.Env = nil
		}
	}
	return candidate
}
