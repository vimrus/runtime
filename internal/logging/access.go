package logging

import (
	"encoding/json"

	"github.com/caddyserver/caddy/v2/caddyconfig"
	caddylogging "github.com/caddyserver/caddy/v2/modules/logging"
)

var sensitiveQueryParams = []string{
	"token", "password", "passwd", "secret", "session", "session_id",
	"api_key", "apikey", "access_key", "authorization", "signature", "sign",
}

var sensitiveRequestHeaders = []string{
	"X-Zentao-Runtime-Profile-Token",
	"X-Zentao-Queue-Token",
	"X-Zentao-Observability-Token",
	"X-Zentao-OLAP-Token",
}

// AccessEncoderRaw returns the Caddy access-log encoder used by Runtime. It
// redacts well-known sensitive query parameters and drops private bridge
// tokens before a request is written to disk.
func AccessEncoderRaw() json.RawMessage {
	actions := make([]map[string]any, 0, len(sensitiveQueryParams))
	for _, parameter := range sensitiveQueryParams {
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
	for _, header := range sensitiveRequestHeaders {
		fields["request>headers>"+header] = caddyconfig.JSONModuleObject(caddylogging.DeleteFilter{}, "filter", "delete", nil)
	}
	return caddyconfig.JSONModuleObject(caddylogging.FilterEncoder{
		WrappedRaw: caddyconfig.JSONModuleObject(caddylogging.JSONEncoder{}, "format", "json", nil),
		FieldsRaw:  fields,
	}, "format", "filter", nil)
}
