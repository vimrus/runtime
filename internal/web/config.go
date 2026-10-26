package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/fileserver"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/rewrite"
	"github.com/caddyserver/caddy/v2/modules/logging"
	frankencaddy "github.com/dunglas/frankenphp/caddy"

	"github.com/vimrus/runtime/internal/queue/bridge"
)

type Options struct {
	Apps                []App
	Listen              string
	Threads             int
	ReadHeaderTimeout   time.Duration
	IdleTimeout         time.Duration
	MaxHeaderBytes      int
	AccessLogPath       string
	AccessLogKeepDays   int
	QueueBridge         QueueBridgeOptions
	ObservabilityBridge ObservabilityBridgeOptions
}

// App is one application route served by the Runtime. Applications are
// pluggable: the Runtime only needs the document root, the URL prefix and
// the per-application PHP environment.
type App struct {
	ID     string
	Prefix string
	Root   string
	Env    map[string]string
	// Available is false when the application document root does not exist or
	// is not a directory. Unavailable applications are served as a 503
	// placeholder instead of PHP/file handlers: registering a handler with a
	// missing root would abort Caddy provisioning and with it the whole
	// Runtime, which must stay usable while an application is detached.
	Available bool
}

// QueueBridgeOptions configures the private loopback listener that exposes
// the PHP Queue Bridge to the Runtime worker engine.
type QueueBridgeOptions struct {
	Enabled bool
	Listen  string
	// Apps declares per-application bridges: namespaced loopback paths, one
	// token per application and the in-application URL template the bridge
	// endpoint names are rewritten into.
	Apps []QueueBridgeApp
}

// QueueBridgeApp describes one application's private Queue Bridge.
type QueueBridgeApp struct {
	ID       string
	Prefix   string
	Token    string
	Template string
}

// ObservabilityBridgeOptions configures the private loopback listener that
// receives bounded, versioned telemetry events from the PHP application.
type ObservabilityBridgeOptions struct {
	Enabled bool
	Listen  string
	// Token is the legacy single bridge token. Tokens carries one token per
	// application so that each application's telemetry is attributed to its
	// own dataset.
	Token  string
	Tokens []string
	Sink   EventSink
}

// Config builds the smallest useful Classic-mode Caddy configuration.
func Config(options Options) *caddy.Config {
	apps := options.Apps
	healthRoute := func(kind string, paths ...string) caddyhttp.Route {
		return caddyhttp.Route{
			MatcherSetsRaw: []caddy.ModuleMap{{
				"path": caddyconfig.JSON(caddyhttp.MatchPath(paths), nil),
			}},
			HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(RuntimeHealth{Kind: kind}, "handler", "zentao_runtime_health", nil)},
			Terminal:    true,
		}
	}

	routes := caddyhttp.RouteList{
		healthRoute("liveness", "/_runtime/healthz", "/_runtime/liveness"),
		healthRoute("readiness", "/_runtime/readiness"),
	}
	// Sub-path applications must be matched before the root application so
	// that the root prefix remains the fallback.
	for _, app := range orderApps(apps) {
		if !app.Available {
			routes = append(routes, unavailableApplicationRoute(app))
			continue
		}
		routes = append(routes, applicationRoute(app))
	}
	if len(apps) == 0 {
		routes = append(routes, staticResponseRoute(503, `{"status":"unavailable","reason":"no application configured"}`))
	} else if !hasRootApp(apps) {
		routes = append(routes, staticResponseRoute(404, `{"status":"not_found"}`))
	}

	server := &caddyhttp.Server{
		Listen:            []string{options.Listen},
		ReadHeaderTimeout: caddy.Duration(options.ReadHeaderTimeout),
		IdleTimeout:       caddy.Duration(options.IdleTimeout),
		MaxHeaderBytes:    options.MaxHeaderBytes,
		Routes:            routes,
	}

	servers := map[string]*caddyhttp.Server{"zentao": server}
	if options.QueueBridge.Enabled {
		bridgeListen := options.QueueBridge.Listen
		if bridgeListen == "" {
			bridgeListen = "127.0.0.1:8081"
		}
		bridgeServer := &caddyhttp.Server{
			Listen:            []string{bridgeListen},
			ReadHeaderTimeout: caddy.Duration(options.ReadHeaderTimeout),
			IdleTimeout:       caddy.Duration(options.IdleTimeout),
			MaxHeaderBytes:    options.MaxHeaderBytes,
		}
		/* Each application gets its own namespace, token and URL template. */
		bridgeRoutes := caddyhttp.RouteList{}
		for _, queueApp := range options.QueueBridge.Apps {
			bridgeTarget, found := findApp(apps, queueApp.ID)
			if !found || !bridgeTarget.Available {
				continue
			}
			appRoutes := caddyhttp.RouteList{}
			for _, endpoint := range queueBridgeEndpoints {
				target := strings.ReplaceAll(queueApp.Template, "{endpoint}", endpoint)
				appRoutes = append(appRoutes, bridgeRewriteRoute(bridge.AppBasePath(queueApp.ID)+"/"+endpoint, joinBridgePath(queueApp.Prefix, target)))
			}
			appRoutes = append(appRoutes, applicationInnerRoutes(bridgeTarget)...)
			bridgeRoutes = append(bridgeRoutes, caddyhttp.Route{
				MatcherSetsRaw: []caddy.ModuleMap{{
					"path": caddyconfig.JSON(caddyhttp.MatchPath{bridge.AppBasePath(queueApp.ID), bridge.AppBasePath(queueApp.ID) + "/*"}, nil),
				}},
				HandlersRaw: []json.RawMessage{
					caddyconfig.JSONModuleObject(QueueAuth{Token: queueApp.Token}, "handler", "zentao_queue_auth", nil),
					caddyconfig.JSONModuleObject(caddyhttp.Subroute{Routes: appRoutes}, "handler", "subroute", nil),
				},
			})
		}
		if len(bridgeRoutes) == 0 {
			bridgeRoutes = append(bridgeRoutes, staticResponseRoute(503, `{"status":"unavailable","reason":"queue bridge has no application"}`))
		}
		bridgeRoutes = append(bridgeRoutes, staticResponseRoute(404, `{"status":"not_found"}`))
		bridgeServer.Routes = bridgeRoutes
		servers["queuebridge"] = bridgeServer
	}
	if options.ObservabilityBridge.Enabled {
		observabilityListen := options.ObservabilityBridge.Listen
		if observabilityListen == "" {
			observabilityListen = "127.0.0.1:8082"
		}
		observabilityServer := &caddyhttp.Server{
			Listen:            []string{observabilityListen},
			ReadHeaderTimeout: caddy.Duration(options.ReadHeaderTimeout),
			IdleTimeout:       caddy.Duration(options.IdleTimeout),
			MaxHeaderBytes:    options.MaxHeaderBytes,
			Routes: caddyhttp.RouteList{
				{
					MatcherSetsRaw: []caddy.ModuleMap{{
						"path": caddyconfig.JSON(caddyhttp.MatchPath{"/internal/runtime/observability/v1/events"}, nil),
					}},
					HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(ObservabilityIngest{
						Token:  options.ObservabilityBridge.Token,
						Tokens: options.ObservabilityBridge.Tokens,
						Sink:   options.ObservabilityBridge.Sink,
					}, "handler", "zentao_observability_ingest", nil)},
				},
				{
					MatcherSetsRaw: []caddy.ModuleMap{{
						"path": caddyconfig.JSON(caddyhttp.MatchPath{"/internal/runtime/observability/v1/query"}, nil),
					}},
					HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(ObservabilityQuery{
						Token:  options.ObservabilityBridge.Token,
						Tokens: options.ObservabilityBridge.Tokens,
					}, "handler", "zentao_observability_query", nil)},
				},
			},
		}
		servers["observability"] = observabilityServer
	}

	persist := false
	if options.AccessLogPath != "" {
		server.Logs = &caddyhttp.ServerLogConfig{DefaultLoggerName: "access"}
	}
	config := &caddy.Config{
		Admin: &caddy.AdminConfig{
			Disabled: true,
			Config:   &caddy.ConfigSettings{Persist: &persist},
		},
		// Caddy emits informational warnings during normal HTTP startup when
		// TLS is not configured. Keep those out of the user's console while
		// retaining real errors.
		Logging: &caddy.Logging{
			Logs: map[string]*caddy.CustomLog{
				caddy.DefaultLoggerName: {
					BaseLog: caddy.BaseLog{Level: "ERROR"},
				},
			},
		},
		AppsRaw: caddy.ModuleMap{
			"http": caddyconfig.JSON(caddyhttp.App{Servers: servers}, nil),
			"frankenphp": caddyconfig.JSON(frankencaddy.FrankenPHPApp{
				NumThreads: options.Threads,
				MaxThreads: options.Threads,
			}, nil),
		},
	}
	if options.AccessLogPath != "" {
		roll := true
		keepDays := options.AccessLogKeepDays
		if keepDays <= 0 {
			keepDays = 7
		}
		// Hourly rolling creates many small segments; keep enough backups to
		// honor the day-based retention before MaxBackups trims them.
		rollKeep := keepDays*24 + 1
		config.Logging.Logs["access"] = &caddy.CustomLog{
			BaseLog: caddy.BaseLog{
				WriterRaw: caddyconfig.JSONModuleObject(logging.FileWriter{
					Filename:         options.AccessLogPath,
					Roll:             &roll,
					RollSizeMB:       64,
					RollAtMinutes:    []int{0},
					RollKeepDays:     keepDays,
					RollKeep:         rollKeep,
					RollCompression:  "none",
					BackupTimeFormat: "2006-01-02T15-04-05.000",
				}, "output", "file", nil),
				EncoderRaw: accessLogEncoderRaw(),
				Level:      "DEBUG",
			},
			Include: []string{"http.log.access", "http.log.error"},
		}
	}
	return config
}

// orderApps returns the applications with sub-path prefixes first so that a
// root application, if any, stays the final fallback.
func orderApps(apps []App) []App {
	ordered := make([]App, 0, len(apps))
	for _, app := range apps {
		if app.Prefix != "/" {
			ordered = append(ordered, app)
		}
	}
	for _, app := range apps {
		if app.Prefix == "/" {
			ordered = append(ordered, app)
		}
	}
	return ordered
}

func hasRootApp(apps []App) bool {
	for _, app := range apps {
		if app.Prefix == "/" {
			return true
		}
	}
	return false
}

// unavailableApplicationRoute answers every request for one application
// prefix with a 503 placeholder. It is used instead of PHP/file handlers when
// the application is detached so that Caddy provisioning cannot fail.
func unavailableApplicationRoute(app App) caddyhttp.Route {
	prefix := app.Prefix
	if prefix == "" || prefix == "/" {
		return staticResponseRoute(503, `{"status":"unavailable","reason":"application `+app.ID+` is not installed"}`)
	}
	return caddyhttp.Route{
		MatcherSetsRaw: []caddy.ModuleMap{{
			"path": caddyconfig.JSON(caddyhttp.MatchPath{prefix, prefix + "/*"}, nil),
		}},
		HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(caddyhttp.StaticResponse{
			StatusCode: caddyhttp.WeakString("503"),
			Headers: http.Header{
				"Content-Type":  []string{"application/json"},
				"Cache-Control": []string{"no-store"},
			},
			Body: `{"status":"unavailable","reason":"application ` + app.ID + ` is not installed"}`,
		}, "handler", "static_response", nil)},
		Terminal: true,
	}
}

var queueBridgeEndpoints = []string{"capabilities", "claim", "execute", "heartbeat", "reap", "stats", "control", "schedule"}

// bridgeRewriteRoute matches one loopback bridge path and rewrites it to the
// application-internal target URL.
func bridgeRewriteRoute(matchPath, target string) caddyhttp.Route {
	return caddyhttp.Route{
		MatcherSetsRaw: []caddy.ModuleMap{{
			"path": caddyconfig.JSON(caddyhttp.MatchPath{matchPath}, nil),
		}},
		HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(rewrite.Rewrite{URI: target}, "handler", "rewrite", nil)},
	}
}

// joinBridgePath prefixes an in-application bridge URL with the application's
// public path prefix; the root application keeps the URL unchanged.
func joinBridgePath(prefix, target string) string {
	if prefix == "" || prefix == "/" {
		return target
	}
	return strings.TrimRight(prefix, "/") + target
}

func findApp(apps []App, id string) (App, bool) {
	for _, app := range apps {
		if app.ID == id {
			return app, true
		}
	}
	return App{}, false
}

func applicationRoute(app App) caddyhttp.Route {
	prefix := app.Prefix
	if prefix == "" {
		prefix = "/"
	}
	inner := applicationInnerRoutes(app)
	routes := caddyhttp.RouteList{}
	if prefix != "/" {
		// Requests for the bare prefix (no trailing slash) are served as the
		// application root. Stripping the prefix here would leave an empty
		// path, so rewrite it to "/" explicitly.
		routes = append(routes, caddyhttp.Route{
			MatcherSetsRaw: []caddy.ModuleMap{{
				"path": caddyconfig.JSON(caddyhttp.MatchPath{prefix}, nil),
			}},
			HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(rewrite.Rewrite{URI: "/"}, "handler", "rewrite", nil)},
		})

		// The prefix only affects file lookup and PATH_INFO/SCRIPT_NAME:
		// FrankenPHP still hands PHP the original REQUEST_URI, and
		// ZT_WEB_ROOT tells the application about its public prefix.
		routes = append(routes, caddyhttp.Route{HandlersRaw: []json.RawMessage{
			caddyconfig.JSONModuleObject(rewrite.Rewrite{StripPathPrefix: prefix}, "handler", "rewrite", nil),
		}})
	}
	routes = append(routes, inner...)
	subroute := caddyhttp.Subroute{Routes: routes}
	handler := json.RawMessage(caddyconfig.JSONModuleObject(subroute, "handler", "subroute", nil))
	if prefix == "/" {
		return caddyhttp.Route{HandlersRaw: []json.RawMessage{handler}}
	}
	return caddyhttp.Route{
		MatcherSetsRaw: []caddy.ModuleMap{{
			"path": caddyconfig.JSON(caddyhttp.MatchPath{prefix, prefix + "/*"}, nil),
		}},
		HandlersRaw: []json.RawMessage{handler},
	}
}

// applicationInnerRoutes builds the classic-mode file, PHP and static
// handlers for one application document root.
func applicationInnerRoutes(app App) caddyhttp.RouteList {
	root := app.Root
	env := app.Env
	rewriteRoute := caddyhttp.Route{
		MatcherSetsRaw: []caddy.ModuleMap{{
			"file": caddyconfig.JSON(fileserver.MatchFile{
				Root:      root,
				TryFiles:  []string{"{http.request.uri.path}", "{http.request.uri.path}/index.php", "index.php"},
				SplitPath: []string{".php"},
			}, nil),
		}},
		HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(rewrite.Rewrite{
			URI: "{http.matchers.file.relative}{http.matchers.file.remainder}",
		}, "handler", "rewrite", nil)},
	}
	phpRoute := caddyhttp.Route{
		MatcherSetsRaw: []caddy.ModuleMap{{
			"path": caddyconfig.JSON(caddyhttp.MatchPath{"*.php", "*.php/*"}, nil),
		}},
		HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(frankencaddy.FrankenPHPModule{
			Root:      root,
			SplitPath: []string{".php"},
			Env:       env,
		}, "handler", "php", nil)},
	}
	fileRoute := caddyhttp.Route{
		HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(fileserver.FileServer{
			Root: root,
		}, "handler", "file_server", nil)},
	}
	return caddyhttp.RouteList{rewriteRoute, phpRoute, fileRoute}
}

func staticResponseRoute(status int, body string) caddyhttp.Route {
	return caddyhttp.Route{
		HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(caddyhttp.StaticResponse{
			StatusCode: caddyhttp.WeakString(strconv.Itoa(status)),
			Headers: http.Header{
				"Content-Type":  []string{"application/json"},
				"Cache-Control": []string{"no-store"},
			},
			Body: body,
		}, "handler", "static_response", nil)},
		Terminal: true,
	}
}

// accessLogEncoderRaw returns a Caddy filter encoder that keeps the JSON
// access format but redacts well-known sensitive query parameters from
// request.uri before the line is written.
func accessLogEncoderRaw() json.RawMessage {
	redactedQueryParams := []string{
		"token", "password", "passwd", "secret", "session", "session_id",
		"api_key", "apikey", "access_key", "authorization", "signature", "sign",
	}
	actions := make([]map[string]any, 0, len(redactedQueryParams))
	for _, parameter := range redactedQueryParams {
		actions = append(actions, map[string]any{
			"type":      "replace",
			"parameter": parameter,
			"value":     "[REDACTED]",
		})
	}
	fields := map[string]json.RawMessage{
		"request>uri": caddyconfig.JSONModuleObject(struct {
			Actions []map[string]any `json:"actions"`
		}{Actions: actions}, "filter", "query", nil),
	}
	return caddyconfig.JSONModuleObject(logging.FilterEncoder{
		WrappedRaw: caddyconfig.JSONModuleObject(logging.JSONEncoder{}, "format", "json", nil),
		FieldsRaw:  fields,
	}, "format", "filter", nil)
}
