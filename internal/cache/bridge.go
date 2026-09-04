package cache

import (
	"bytes"
	"encoding/json"
)

const (
	MaxKeysPerRequest = 128
	MaxRequestBytes   = 64 * 1024
)

type keyRef struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

type itemValue struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Hit       bool   `json:"hit"`
	ValueType string `json:"valueType,omitempty"`
	Value     string `json:"value,omitempty"`
}

type setItem struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	ValueType string `json:"valueType"`
	Value     string `json:"value"`
	TTL       int64  `json:"ttl"`
}

type bridgeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(code, message string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"schema": SchemaVersion,
		"error":  bridgeError{Code: code, Message: message},
	})
}

// Process handles one Cache Bridge operation and returns a JSON response.
func Process(service *Cache, operation string, body []byte) ([]byte, error) {
	if service == nil {
		return writeError("unavailable", "cache service is not available")
	}
	if len(body) > MaxRequestBytes {
		return writeError("request_too_large", "cache request too large")
	}
	switch operation {
	case "capabilities":
		return json.Marshal(map[string]any{
			"schema":            SchemaVersion,
			"backend":           "memory",
			"valueTypes":        []string{"string", "json"},
			"maxValueBytes":     DefaultMaxValueBytes,
			"maxKeysPerRequest": MaxKeysPerRequest,
			"namespaces":        service.Registry(),
		})
	case "get":
		return processGet(service, body)
	case "set":
		return processSet(service, body)
	case "delete":
		return processDelete(service, body)
	default:
		return writeError("invalid_request", "unsupported cache operation")
	}
}

func processGet(service *Cache, body []byte) ([]byte, error) {
	var request struct {
		Schema int      `json:"schema"`
		Items  []keyRef `json:"items"`
	}
	if err := decodeStrict(body, &request); err != nil {
		return writeError("invalid_request", "invalid cache get request")
	}
	if request.Schema != SchemaVersion || len(request.Items) == 0 || len(request.Items) > MaxKeysPerRequest {
		return writeError("invalid_request", "invalid cache get request")
	}
	items := make([]itemValue, 0, len(request.Items))
	for _, ref := range request.Items {
		item := itemValue{Namespace: NormalizeNamespace(ref.Namespace), Key: ref.Key}
		if entry, ok := service.Get(item.Namespace, item.Key); ok {
			item.Hit = true
			item.ValueType = entry.ValueType
			item.Value = entry.Value
		}
		items = append(items, item)
	}
	return json.Marshal(map[string]any{"schema": SchemaVersion, "items": items})
}

func processSet(service *Cache, body []byte) ([]byte, error) {
	var request struct {
		Schema int       `json:"schema"`
		Items  []setItem `json:"items"`
	}
	if err := decodeStrict(body, &request); err != nil {
		return writeError("invalid_request", "invalid cache set request")
	}
	if request.Schema != SchemaVersion || len(request.Items) == 0 || len(request.Items) > MaxKeysPerRequest {
		return writeError("invalid_request", "invalid cache set request")
	}
	ok := true
	for _, item := range request.Items {
		err := service.Set(NormalizeNamespace(item.Namespace), item.Key, item.ValueType, item.Value, item.TTL)
		if err != nil {
			return writeError(errorCode(err), "cache set rejected")
		}
	}
	return json.Marshal(map[string]any{"schema": SchemaVersion, "ok": ok})
}

func processDelete(service *Cache, body []byte) ([]byte, error) {
	var request struct {
		Schema int      `json:"schema"`
		Items  []keyRef `json:"items"`
	}
	if err := decodeStrict(body, &request); err != nil {
		return writeError("invalid_request", "invalid cache delete request")
	}
	if request.Schema != SchemaVersion || len(request.Items) == 0 || len(request.Items) > MaxKeysPerRequest {
		return writeError("invalid_request", "invalid cache delete request")
	}
	for _, ref := range request.Items {
		service.Delete(NormalizeNamespace(ref.Namespace), ref.Key)
	}
	return json.Marshal(map[string]any{"schema": SchemaVersion, "ok": true})
}

func decodeStrict(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func errorCode(err error) string {
	message := err.Error()
	for _, code := range []string{"value_too_large", "quota_exceeded", "invalid_request"} {
		if message == code {
			return code
		}
	}
	return "internal"
}
