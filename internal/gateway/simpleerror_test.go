package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opencode2api/internal/config"
)

// postUnauthenticated sends an inference request without a server key, so the
// gateway answers with the 401 error rather than reaching an upstream.
func postUnauthenticated(t *testing.T, gateway *Gateway, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"any-model"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(recorder, request)
	return recorder
}

// errorObject decodes the error envelope of a reply into a generic map, so a
// test can assert on the fields that are present and on the ones that are not.
func errorObject(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
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

// With errors.simple_body enabled every protocol answers an error with the
// same compact shape: the error code and the message, and nothing else.
func TestSimpleErrorBodyIsCompactOnEveryProtocol(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	gateway := testGateway(t, upstream.URL, config.ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{}})
	gateway.cfg.Errors.SimpleBody = true

	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/systemone"} {
		recorder := postUnauthenticated(t, gateway, path)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d, want 401: %s", path, recorder.Code, recorder.Body.String())
		}
		errorValue := errorObject(t, recorder)
		if errorValue["code"] != "authentication_error" {
			t.Fatalf("%s: code %v, want authentication_error", path, errorValue["code"])
		}
		if errorValue["message"] != "invalid local API key" {
			t.Fatalf("%s: message %v", path, errorValue["message"])
		}
		if _, ok := errorValue["type"]; ok {
			t.Fatalf("%s: the compact body must not carry type: %s", path, recorder.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if _, ok := body["type"]; ok {
			t.Fatalf("%s: the compact body must not carry the Anthropic type: %s", path, recorder.Body.String())
		}
	}
}

// With errors.simple_body disabled the error keeps the envelope its protocol
// expects: the OpenAI one for Chat and Responses, the Anthropic one for
// /v1/messages.
func TestSimpleErrorBodyOffKeepsProtocolEnvelopes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	gateway := testGateway(t, upstream.URL, config.ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{}})

	recorder := postUnauthenticated(t, gateway, "/v1/chat/completions")
	errorValue := errorObject(t, recorder)
	if errorValue["type"] != "authentication_error" {
		t.Fatalf("chat: type %v, want authentication_error", errorValue["type"])
	}
	if errorValue["code"] != nil {
		t.Fatalf("chat: code %v, want null", errorValue["code"])
	}

	recorder = postUnauthenticated(t, gateway, "/v1/messages")
	errorValue = errorObject(t, recorder)
	if errorValue["type"] != "authentication_error" {
		t.Fatalf("anthropic: type %v, want authentication_error", errorValue["type"])
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("anthropic: %v", err)
	}
	if body["type"] != "error" {
		t.Fatalf("anthropic: the envelope must keep its type: %s", recorder.Body.String())
	}
}
