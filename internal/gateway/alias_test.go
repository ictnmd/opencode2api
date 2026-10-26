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
	wire "opencode2api/internal/protocol"
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
	// A nil WARP pool keeps these tests on the plain transport path; WARP is
	// opt-in and orthogonal to aliasing.
	gateway, err := New(normalized, slog.New(slog.NewTextHandler(io.Discard, nil)), telemetry.Monitor(), nil)
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

// museSparkAlias is the client-facing name from the requirement and
// museSparkModel the real upstream ID it must route to:
//
//	O2A_MODEL_ALIASES={"muse-spark-1.3":"muse-spark-1.3-contributor-free"}
//
// The requested name is a strict prefix of the real ID, so assertions match the
// whole quoted JSON value rather than a bare substring.
const (
	museSparkAlias = "muse-spark-1.3"
	museSparkModel = "muse-spark-1.3-contributor-free"
)

// museSparkGateway is the aliases-only gateway for the exact pair above, with
// the real model pinned to one upstream protocol so every lane can be exercised
// without depending on catalog discovery.
func museSparkGateway(t *testing.T, upstreamURL string, protocol wire.Protocol) *Gateway {
	t.Helper()
	return testGateway(t, upstreamURL, config.ModelsConfig{
		RefreshSeconds: 300,
		Protocols:      map[string]string{museSparkModel: string(protocol)},
		Aliases:        map[string]string{museSparkAlias: museSparkModel},
	})
}

// postAliasInference posts an authenticated inference request with the request
// metadata the production stack always installs before the gateway handler runs
// (telemetry.Middleware). A key-tier request for a free model is force-streamed
// upstream and collapsed back for a non-streaming client, and that decision is
// recorded on the metadata, so the gateway must see it here too.
func postAliasInference(t *testing.T, gateway *Gateway, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer sk-test")
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(telemetry.WithRequestMeta(request.Context(), &telemetry.RequestMeta{}))
	recorder := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(recorder, request)
	return recorder
}

// museSparkUpstreamReply is the upstream reply for one protocol. The reply
// shape follows the stream flag the gateway actually sent, not what the client
// asked for, so the force-streaming lane is reproduced faithfully.
func museSparkUpstreamReply(protocol wire.Protocol, stream bool) (contentType, body string) {
	if stream {
		switch protocol {
		case wire.Anthropic:
			return "text/event-stream",
				"event: message_start\ndata: " + anthropicStart(museSparkModel) + "\n\n" +
					"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
					"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
					"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n" +
					"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		case wire.Responses:
			return "text/event-stream",
				"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1,\"status\":\"in_progress\",\"model\":\"" + museSparkModel + "\",\"output\":[]},\"sequence_number\":0}\n\n" +
					"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"ok\",\"sequence_number\":1}\n\n" +
					"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"" + museSparkModel + "\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}},\"sequence_number\":2}\n\n"
		default:
			return "text/event-stream",
				"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"" + museSparkModel + "\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n" +
					"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"" + museSparkModel + "\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
					"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"" + museSparkModel + "\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
					"data: [DONE]\n\n"
		}
	}
	switch protocol {
	case wire.Anthropic:
		return "application/json",
			"{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"" + museSparkModel + "\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"}],\"stop_reason\":\"end_turn\",\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}"
	case wire.Responses:
		return "application/json",
			"{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"" + museSparkModel + "\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}"
	default:
		return "application/json",
			"{\"id\":\"c1\",\"object\":\"chat.completion\",\"created\":1,\"model\":\"" + museSparkModel + "\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}"
	}
}

func anthropicStart(model string) string {
	return "{\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"" + model + "\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}"
}

// The acceptance criterion, on every lane the gateway exposes: a client that
// asks for muse-spark-1.3 must be routed upstream as
// muse-spark-1.3-contributor-free, and the reply it receives must report the
// name it asked for — never the real ID.
//
// The final case pins the upstream to Chat while the client speaks Responses:
// the reply is rebuilt by the protocol bridge from the upstream's own model
// name, so it proves the alias rewrite runs after conversion rather than before.
func TestMuseSparkAliasRoutesRealModelAndEchoesRequestedName(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		upstreamTo wire.Protocol
		request    string
	}{
		{
			name: "chat non-streaming", path: "/v1/chat/completions", upstreamTo: wire.Chat,
			request: `{"model":"` + museSparkAlias + `","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "chat streaming", path: "/v1/chat/completions", upstreamTo: wire.Chat,
			request: `{"model":"` + museSparkAlias + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "responses non-streaming", path: "/v1/responses", upstreamTo: wire.Responses,
			request: `{"model":"` + museSparkAlias + `","input":"hi"}`,
		},
		{
			name: "responses streaming", path: "/v1/responses", upstreamTo: wire.Responses,
			request: `{"model":"` + museSparkAlias + `","stream":true,"input":"hi"}`,
		},
		{
			name: "anthropic non-streaming", path: "/v1/messages", upstreamTo: wire.Anthropic,
			request: `{"model":"` + museSparkAlias + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "anthropic streaming", path: "/v1/messages", upstreamTo: wire.Anthropic,
			request: `{"model":"` + museSparkAlias + `","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "responses client over chat upstream", path: "/v1/responses", upstreamTo: wire.Chat,
			request: `{"model":"` + museSparkAlias + `","input":"hi"}`,
		},
		{
			name: "responses client streaming over chat upstream", path: "/v1/responses", upstreamTo: wire.Chat,
			request: `{"model":"` + museSparkAlias + `","stream":true,"input":"hi"}`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var seenModel string
			var seenStream bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				seenModel, _ = payload["model"].(string)
				seenStream, _ = payload["stream"].(bool)
				contentType, reply := museSparkUpstreamReply(test.upstreamTo, seenStream)
				w.Header().Set("Content-Type", contentType)
				_, _ = io.WriteString(w, reply)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}))
			defer upstream.Close()

			gateway := museSparkGateway(t, upstream.URL, test.upstreamTo)
			recorder := postAliasInference(t, gateway, test.path, test.request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
			}
			if seenModel != museSparkModel {
				t.Fatalf("upstream must receive the real model ID %q, got %q", museSparkModel, seenModel)
			}
			body := recorder.Body.String()
			if !strings.Contains(body, `"model":"`+museSparkAlias+`"`) {
				t.Fatalf("response must echo the requested name: %s", body)
			}
			if strings.Contains(body, museSparkModel) {
				t.Fatalf("real model ID leaked to the client: %s", body)
			}
			for name, values := range recorder.Header() {
				for _, value := range values {
					if strings.Contains(value, museSparkModel) {
						t.Fatalf("header %s leaks the real model ID: %s", name, value)
					}
				}
			}
		})
	}
}

// The alias rewrite must hold for every client/upstream protocol pairing the
// bridge supports, streaming and not. A gap in any single conversion would let
// the real ID reach a client on that lane only.
func TestMuseSparkAliasAcrossEveryProtocolPairing(t *testing.T) {
	type lane struct {
		name    string
		path    string
		request string
	}
	clientLanes := []lane{
		{
			name: "chat client", path: "/v1/chat/completions",
			request: `{"model":"` + museSparkAlias + `","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "chat client streaming", path: "/v1/chat/completions",
			request: `{"model":"` + museSparkAlias + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "responses client", path: "/v1/responses",
			request: `{"model":"` + museSparkAlias + `","input":"hi"}`,
		},
		{
			name: "responses client streaming", path: "/v1/responses",
			request: `{"model":"` + museSparkAlias + `","stream":true,"input":"hi"}`,
		},
		{
			name: "anthropic client", path: "/v1/messages",
			request: `{"model":"` + museSparkAlias + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "anthropic client streaming", path: "/v1/messages",
			request: `{"model":"` + museSparkAlias + `","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		},
	}
	for _, client := range clientLanes {
		for _, upstreamTo := range []wire.Protocol{wire.Chat, wire.Responses, wire.Anthropic} {
			t.Run(client.name+" over "+string(upstreamTo)+" upstream", func(t *testing.T) {
				var seenModel string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var payload map[string]any
					_ = json.NewDecoder(r.Body).Decode(&payload)
					seenModel, _ = payload["model"].(string)
					seenStream, _ := payload["stream"].(bool)
					contentType, reply := museSparkUpstreamReply(upstreamTo, seenStream)
					w.Header().Set("Content-Type", contentType)
					_, _ = io.WriteString(w, reply)
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}))
				defer upstream.Close()

				gateway := museSparkGateway(t, upstream.URL, upstreamTo)
				recorder := postAliasInference(t, gateway, client.path, client.request)

				if recorder.Code != http.StatusOK {
					t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
				}
				if seenModel != museSparkModel {
					t.Fatalf("upstream must receive the real model ID %q, got %q", museSparkModel, seenModel)
				}
				body := recorder.Body.String()
				if !strings.Contains(body, `"model":"`+museSparkAlias+`"`) {
					t.Fatalf("response must echo the requested name: %s", body)
				}
				if strings.Contains(body, museSparkModel) {
					t.Fatalf("real model ID leaked to the client: %s", body)
				}
			})
		}
	}
}

// The real ID must keep working when it is addressed directly: the alias is an
// additional name, not a replacement for the model it points at.
//
// The real ID ends in "-free", so the gateway shapes this request to the
// free-tier wire form (stream plus core tools) and collapses the reply back for
// a non-streaming client. The upstream handler therefore answers with the shape
// the gateway actually asked for, not the shape the client sent.
func TestMuseSparkAliasKeepsRealModelAddressable(t *testing.T) {
	var seenModel string
	var seenStream bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		seenModel, _ = payload["model"].(string)
		seenStream, _ = payload["stream"].(bool)
		contentType, reply := museSparkUpstreamReply(wire.Chat, seenStream)
		w.Header().Set("Content-Type", contentType)
		_, _ = io.WriteString(w, reply)
	}))
	defer upstream.Close()

	gateway := museSparkGateway(t, upstream.URL, wire.Chat)
	recorder := postAliasInference(t, gateway, "/v1/chat/completions",
		`{"model":"`+museSparkModel+`","messages":[{"role":"user","content":"hi"}]}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("the real model must stay addressable: %d %s", recorder.Code, recorder.Body.String())
	}
	if seenModel != museSparkModel {
		t.Fatalf("upstream must receive the real model ID, got %q", seenModel)
	}
	if !seenStream {
		t.Fatal("the free-tier real model must be shaped to the streaming wire form")
	}
	if !strings.Contains(recorder.Body.String(), `"model":"`+museSparkModel+`"`) {
		t.Fatalf("an unaliased request must keep the real name: %s", recorder.Body.String())
	}
}

// The System One lane relays its payload verbatim, so it needs its own proof
// that an aliased decision request is routed by the real ID and answered under
// the requested name.
func TestMuseSparkAliasOnSystemOneEchoesRequestedName(t *testing.T) {
	var seenModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		seenModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"`+museSparkModel+`","answers":[{"id":"q1","answer":"yes"}]}`)
	}))
	defer upstream.Close()

	gateway := museSparkGateway(t, upstream.URL, wire.SystemOne)
	recorder := postAliasInference(t, gateway, "/v1/systemone",
		`{"model":"`+museSparkAlias+`","state":{"task":"x"},"questions":[{"id":"q1","question":"proceed?"}]}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if seenModel != museSparkModel {
		t.Fatalf("upstream must receive the real model ID, got %q", seenModel)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"model":"`+museSparkAlias+`"`) {
		t.Fatalf("response must echo the requested name: %s", body)
	}
	if strings.Contains(body, museSparkModel) {
		t.Fatalf("real model ID leaked to the client: %s", body)
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
