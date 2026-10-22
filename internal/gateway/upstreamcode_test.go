package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"opencode2api/internal/config"
)

// errorBody decodes the error envelope of a reply.
func errorBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	value, _ := body["error"].(map[string]any)
	if value == nil {
		t.Fatalf("no error object in %q", recorder.Body.String())
	}
	return value
}

// An upstream error code must reach the client: it is the code of the compact
// body, and it fills the code slot of the OpenAI envelope, which was empty
// before.
func TestUpstreamErrorCodeReachesTheClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid request body: json: unknown field \"max_output_tokens\"","type":"unknown_parameter"}}`)
	}))
	defer upstream.Close()
	gateway := testGateway(t, upstream.URL, config.ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{}})

	recorder := postInference(t, gateway, "/v1/chat/completions", `{"model":"m","messages":[]}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	errorValue := errorBody(t, recorder)
	if errorValue["code"] != "unknown_parameter" {
		t.Fatalf("code %v, want unknown_parameter", errorValue["code"])
	}
	if errorValue["type"] != "upstream_error" {
		t.Fatalf("type %v, want upstream_error", errorValue["type"])
	}

	gateway.cfg.Errors.SimpleBody = true
	recorder = postInference(t, gateway, "/v1/chat/completions", `{"model":"m","messages":[]}`)
	errorValue = errorBody(t, recorder)
	if errorValue["code"] != "unknown_parameter" {
		t.Fatalf("compact code %v, want unknown_parameter", errorValue["code"])
	}
}

// An upstream that names no code keeps the generic one.
func TestUpstreamErrorWithoutACodeKeepsTheGenericOne(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"model not found"}}`)
	}))
	defer upstream.Close()
	gateway := testGateway(t, upstream.URL, config.ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{}})

	recorder := postInference(t, gateway, "/v1/chat/completions", `{"model":"m","messages":[]}`)
	errorValue := errorBody(t, recorder)
	if errorValue["code"] != nil {
		t.Fatalf("code %v, want null", errorValue["code"])
	}
	if errorValue["type"] != "upstream_error" {
		t.Fatalf("type %v, want upstream_error", errorValue["type"])
	}

	gateway.cfg.Errors.SimpleBody = true
	recorder = postInference(t, gateway, "/v1/chat/completions", `{"model":"m","messages":[]}`)
	errorValue = errorBody(t, recorder)
	if errorValue["code"] != "upstream_error" {
		t.Fatalf("compact code %v, want upstream_error", errorValue["code"])
	}
}

// Free text where a code is expected is not a code and must not be echoed as
// one.
func TestUpstreamErrorTypeThatIsNotACodeIsIgnored(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"nope","type":"service unavailable right now"}}`)
	}))
	defer upstream.Close()
	gateway := testGateway(t, upstream.URL, config.ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{}})

	recorder := postInference(t, gateway, "/v1/chat/completions", `{"model":"m","messages":[]}`)
	errorValue := errorBody(t, recorder)
	if errorValue["code"] != nil {
		t.Fatalf("code %v, want null", errorValue["code"])
	}
}
