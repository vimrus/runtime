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

	persist := false
	config := &caddy.Config{
		Admin: &caddy.AdminConfig{
			Disabled: true,
			Config:   &caddy.ConfigSettings{Persist: &persist},
		},
		AppsRaw: caddy.ModuleMap{
			"http": caddyconfig.JSON(caddyhttp.App{Servers: map[string]*caddyhttp.Server{"zentao": server}}, nil),
			"frankenphp": caddyconfig.JSON(frankencaddy.FrankenPHPApp{
				NumThreads: options.Threads,
				MaxThreads: options.Threads,
			}, nil),
		},
	}
	if options.AccessLogPath != "" {
		roll := true
		config.Logging = &caddy.Logging{
			Logs: map[string]*caddy.CustomLog{
				"default": {
					BaseLog: caddy.BaseLog{
						WriterRaw:  caddyconfig.JSONModuleObject(logging.FileWriter{Filename: options.AccessLogPath, Roll: &roll, RollSizeMB: 64}, "output", "file", nil),
						EncoderRaw: caddyconfig.JSONModuleObject(logging.JSONEncoder{}, "format", "json", nil),
						Level:      "INFO",
					},
				},
			},
		}
	}
	return config
}
