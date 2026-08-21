package web

import (
	"encoding/json"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/fileserver"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/rewrite"
	"github.com/caddyserver/caddy/v2/modules/logging"
	frankencaddy "github.com/dunglas/frankenphp/caddy"
)

type Options struct {
	Root              string
	Listen            string
	Threads           int
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	AccessLogPath     string
	AccessLogKeepDays int
	QueueBridge       QueueBridgeOptions
}

// QueueBridgeOptions configures the private loopback listener that exposes
// the PHP Queue Bridge to the Runtime worker engine.
type QueueBridgeOptions struct {
	Enabled bool
	Listen  string
}

// Config builds the smallest useful Classic-mode Caddy configuration.
func Config(options Options) *caddy.Config {
	root := options.Root
	healthRoute := func(kind string, paths ...string) caddyhttp.Route {
		return caddyhttp.Route{
			MatcherSetsRaw: []caddy.ModuleMap{{
				"path": caddyconfig.JSON(caddyhttp.MatchPath(paths), nil),
			}},
			HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(RuntimeHealth{Kind: kind}, "handler", "zentao_runtime_health", nil)},
			Terminal:    true,
		}
	}

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
		}, "handler", "php", nil)},
	}

	fileRoute := caddyhttp.Route{
		HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(fileserver.FileServer{
			Root: root,
		}, "handler", "file_server", nil)},
	}

	subroute := caddyhttp.Subroute{Routes: caddyhttp.RouteList{rewriteRoute, phpRoute, fileRoute}}
	server := &caddyhttp.Server{
		Listen:            []string{options.Listen},
		ReadHeaderTimeout: caddy.Duration(options.ReadHeaderTimeout),
		IdleTimeout:       caddy.Duration(options.IdleTimeout),
		MaxHeaderBytes:    options.MaxHeaderBytes,
		Routes: caddyhttp.RouteList{
			healthRoute("liveness", "/_runtime/healthz", "/_runtime/liveness"),
			healthRoute("readiness", "/_runtime/readiness"),
			{HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(subroute, "handler", "subroute", nil)}},
		},
	}

	servers := map[string]*caddyhttp.Server{"zentao": server}
	if options.QueueBridge.Enabled {
		bridgeListen := options.QueueBridge.Listen
		if bridgeListen == "" {
			bridgeListen = "127.0.0.1:8081"
		}
		bridgeRoutes := caddyhttp.RouteList{}
		for _, endpoint := range []string{"capabilities", "claim", "execute", "heartbeat", "reap", "stats", "control"} {
			bridgeRoutes = append(bridgeRoutes, caddyhttp.Route{
				MatcherSetsRaw: []caddy.ModuleMap{{
					"path": caddyconfig.JSON(caddyhttp.MatchPath{"/internal/runtime/queue/v1/" + endpoint}, nil),
				}},
				HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(rewrite.Rewrite{
					URI: "/index.php/cron-" + endpoint,
				}, "handler", "rewrite", nil)},
			})
		}
		bridgeSubroute := caddyhttp.Subroute{Routes: append(bridgeRoutes, phpRoute, fileRoute)}
		bridgeServer := &caddyhttp.Server{
			Listen:            []string{bridgeListen},
			ReadHeaderTimeout: caddy.Duration(options.ReadHeaderTimeout),
			IdleTimeout:       caddy.Duration(options.IdleTimeout),
			MaxHeaderBytes:    options.MaxHeaderBytes,
			Routes: caddyhttp.RouteList{
				{HandlersRaw: []json.RawMessage{caddyconfig.JSONModuleObject(bridgeSubroute, "handler", "subroute", nil)}},
			},
		}
		servers["queuebridge"] = bridgeServer
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
		config.Logging = &caddy.Logging{
			Logs: map[string]*caddy.CustomLog{
				"access": {
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
						EncoderRaw: caddyconfig.JSONModuleObject(logging.JSONEncoder{}, "format", "json", nil),
						Level:      "DEBUG",
					},
					Include: []string{"http.log.access", "http.log.error"},
				},
			},
		}
	}
	return config
}
