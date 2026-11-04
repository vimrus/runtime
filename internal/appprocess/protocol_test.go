package appprocess

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeStrictRejectsWrongMediaTypeAndOversizedBody(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		code        string
	}{
		{
			name:        "media type",
			contentType: "text/plain",
			body:        `{"schema":1}`,
			code:        "invalid_request",
		},
		{
			name:        "oversized",
			contentType: "application/json",
			body:        `{"schema":1,"method":"` + strings.Repeat("x", MaxRequestBytes) + `"}`,
			code:        "request_too_large",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, BasePath, strings.NewReader(test.body))
			request.Header.Set(SchemaHeader, "1")
			request.Header.Set("Content-Type", test.contentType)
			recorder := httptest.NewRecorder()
			var payload CallRequest
			err := decodeStrict(recorder, request, &payload)
			assertProtocolError(t, err, test.code, "")
		})
	}
}
