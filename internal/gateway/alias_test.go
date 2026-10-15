package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opencode2api/internal/config"
	"opencode2api/internal/telemetry"
)

// testGateway builds a gateway whose only upstream is the supplied test
// server, reached directly through the Go key tier. Catalog discovery is
// deliberately empty so the configured key alone decides routability, exactly
// as it does before the first refresh.
func testGateway(t *testing.T, upstreamURL string, models config.ModelsConfig) *Gateway {
	t.Helper()
	cfg := config.Config{
		Listen:     "127.0.0.1:0",
		ServerKeys: []string{"sk-test"},
		GoKeys:     []string{"go-key-test"},
		Proxies:    []string{"direct"},
		Upstream:   config.UpstreamConfig{Zen: upstreamURL, Go: upstreamURL},
		Retry:      config.RetryConfig{MaxAttempts: 1, TimeoutSeconds: 30},
		Models:     models,
		Performance: config.PerformanceConfig{
			MaxIdleConns: 8, MaxIdleConnsPerHost: 8, IdleConnTimeoutSeconds: 30,
			ConnectTimeoutSeconds: 5, FailureCooldownSeconds: 15,
		},
		Logging: config.LoggingConfig{Level: "error", RingSize: 100},
		Prefer:  config.TierGo,
	}
	normalized, err := config.Normalize("", cfg)
	if err != nil {
		t.Fatalf("normalize config: %v", err)
	}
	gateway, err := New(normalized, slog.New(slog.NewTextHandler(io.Discard, nil)), telemetry.NewMonitor())
	if err != nil {
		t.Fatalf("new gateway: %v", err)
	}
	return gateway
}

// aliasTestGateway is the aliases-only shorthand the alias tests need.
func aliasTestGateway(t *testing.T, upstreamURL string, aliases map[string]string) *Gateway {
	t.Helper()
	return testGateway(t, upstreamURL, config.ModelsConfig{
		RefreshSeconds: 300, Protocols: map[string]string{}, Aliases: aliases,
	})
}

// getModelList fetches /v1/models and returns the raw body.
func getModelList(t *testing.T, gateway *Gateway) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer sk-test")
	recorder := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

func postInference(t *testing.T, gateway *Gateway, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer sk-test")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestAliasRoutesToRealModelAndEchoesClientName(t *testing.T) {
	var seenModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		seenModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-5.6","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	gateway := aliasTestGateway(t, upstream.URL, map[string]string{"gpt-5.5": "gpt-5.6"})
	recorder := postInference(t, gateway, "/v1/chat/completions", `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if seenModel != "gpt-5.6" {
		t.Fatalf("upstream must receive the real model ID, got %q", seenModel)
	}
	if !strings.Contains(recorder.Body.String(), `"model":"gpt-5.5"`) {
		t.Fatalf("response must echo the client name: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "gpt-5.6") {
		t.Fatalf("real model ID leaked to the client: %s", recorder.Body.String())
	}
}

func TestAliasStillServesTheRealModelName(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"gpt-5.6","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	defer upstream.Close()

	gateway := aliasTestGateway(t, upstream.URL, map[string]string{"gpt-5.5": "gpt-5.6"})
	recorder := postInference(t, gateway, "/v1/chat/completions", `{"model":"gpt-5.6","messages":[{"role":"user","content":"hi"}]}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("the real model must stay addressable: %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"model":"gpt-5.6"`) {
		t.Fatalf("unaliased request must keep the real name: %s", recorder.Body.String())
	}
}

func TestAliasStreamRewritesEveryFrame(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		frames := []string{
			`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-5.6","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-5.6","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-5.6","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, frame := range frames {
			_, _ = io.WriteString(w, frame)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	gateway := aliasTestGateway(t, upstream.URL, map[string]string{"gpt-5.5": "gpt-5.6"})
	recorder := postInference(t, gateway, "/v1/chat/completions", `{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "gpt-5.6") {
		t.Fatalf("real model ID leaked into the stream: %s", body)
	}
	if strings.Count(body, `"model":"gpt-5.5"`) != 3 {
		t.Fatalf("expected 3 rewritten frames, got %d: %s", strings.Count(body, `"model":"gpt-5.5"`), body)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("terminator lost: %q", body)
	}
	if !strings.Contains(body, `"content":"ok"`) {
		t.Fatalf("content lost: %s", body)
	}
}

func TestAliasAppliesAcrossProtocolConversion(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"gpt-5.6","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	defer upstream.Close()

	gateway := aliasTestGateway(t, upstream.URL, map[string]string{"gpt-5.5": "gpt-5.6"})
	// The client speaks Responses while the upstream speaks Chat, so the reply is
	// rebuilt by the bridge before the alias rewrite runs.
	recorder := postInference(t, gateway, "/v1/responses", `{"model":"gpt-5.5","input":"hi"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"model":"gpt-5.5"`) {
		t.Fatalf("converted response must carry the client name: %s", body)
	}
	if strings.Contains(body, "gpt-5.6") {
		t.Fatalf("real model ID leaked: %s", body)
	}
}

func TestAliasAppearsInModelList(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	gateway := aliasTestGateway(t, upstream.URL, map[string]string{"gpt-5.5": "gpt-5.6"})
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer sk-test")
	recorder := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"id":"gpt-5.5"`) {
		t.Fatalf("alias missing from discovery: %s", body)
	}
	if !strings.Contains(body, `"upstream_model":"gpt-5.6"`) {
		t.Fatalf("alias entry must name the real model: %s", body)
	}
}

// A deployment without aliases, and any request that does not name one, must
// stay byte-transparent: the gateway is a proxy, not a normalizer.
func TestNoAliasLeavesResponseBytesUntouched(t *testing.T) {
	// Deliberately unusual but valid JSON: key order, spacing and the large
	// integer must all survive, because the body is never re-serialized.
	const upstreamBody = `{"choices": [],"model"  :  "glm-5.3","created":9007199254740993}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer upstream.Close()

	gateway := aliasTestGateway(t, upstream.URL, map[string]string{"gpt-5.5": "gpt-5.6"})
	recorder := postInference(t, gateway, "/v1/chat/completions", `{"model":"glm-5.3","messages":[]}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != upstreamBody {
		t.Fatalf("unaliased response was re-encoded:\n got %s\nwant %s", recorder.Body.String(), upstreamBody)
	}
}

// The same holds for a stream that names no alias: frames are forwarded as-is.
func TestNoAliasLeavesStreamBytesUntouched(t *testing.T) {
	const frames = "data: {\"model\":\"glm-5.3\",\"choices\":[]}\n\ndata: [DONE]\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, frames)
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()

	gateway := aliasTestGateway(t, upstream.URL, map[string]string{"gpt-5.5": "gpt-5.6"})
	recorder := postInference(t, gateway, "/v1/chat/completions", `{"model":"glm-5.3","stream":true,"messages":[]}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != frames {
		t.Fatalf("unaliased stream was re-encoded:\n got %q\nwant %q", recorder.Body.String(), frames)
	}
}
