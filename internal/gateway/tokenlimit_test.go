package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"opencode2api/internal/config"
)

// recordingUpstream is a test upstream that keeps the body it received, so a
// test can assert on the exact keys the gateway sent.
type recordingUpstream struct {
	*httptest.Server
	body map[string]any
}

// newRecordingUpstream starts an upstream that answers with reply and records
// every body it receives.
func newRecordingUpstream(t *testing.T, reply string) *recordingUpstream {
	t.Helper()
	upstream := &recordingUpstream{body: map[string]any{}}
	upstream.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.body = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&upstream.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(upstream.Close)
	return upstream
}

// chatReply is the upstream reply the token-limit tests get back.
const chatReply = `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

// A client that states its output-token limit under the Responses field name
// while talking to a Chat upstream must not make the upstream reject the
// request: the limit moves onto the name the Chat schema knows, and the
// foreign field never leaves the gateway.
func TestChatUpstreamReceivesTheLimitUnderItsOwnName(t *testing.T) {
	upstream := newRecordingUpstream(t, chatReply)
	gateway := testGateway(t, upstream.URL, config.ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{}})

	postInference(t, gateway, "/v1/chat/completions", `{"model":"m","messages":[],"max_output_tokens":512}`)

	if _, ok := upstream.body["max_output_tokens"]; ok {
		t.Fatalf("the upstream must not see the Responses field: %v", upstream.body)
	}
	if upstream.body["max_tokens"] != float64(512) {
		t.Fatalf("the limit must reach the upstream as max_tokens: %v", upstream.body)
	}
}

// The Responses lane names the limit max_output_tokens. A client that states
// it under the Chat name must be understood, and the Responses name must
// survive untouched.
func TestResponsesUpstreamKeepsTheLimitUnderItsOwnName(t *testing.T) {
	upstream := newRecordingUpstream(t, chatReply)
	gateway := testGateway(t, upstream.URL, config.ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{"m": "responses"}})

	postInference(t, gateway, "/v1/responses", `{"model":"m","input":"hi","max_output_tokens":512}`)
	if upstream.body["max_output_tokens"] != float64(512) {
		t.Fatalf("the Responses name must survive: %v", upstream.body)
	}
	if _, ok := upstream.body["max_tokens"]; ok {
		t.Fatalf("the upstream must not see the Chat field: %v", upstream.body)
	}

	postInference(t, gateway, "/v1/responses", `{"model":"m","input":"hi","max_tokens":256}`)
	if upstream.body["max_output_tokens"] != float64(256) {
		t.Fatalf("the Chat name must be understood: %v", upstream.body)
	}
	if _, ok := upstream.body["max_tokens"]; ok {
		t.Fatalf("the upstream must not see the Chat field: %v", upstream.body)
	}
}

// The Anthropic lane names the limit max_tokens as well.
func TestAnthropicUpstreamReceivesTheLimitUnderItsOwnName(t *testing.T) {
	upstream := newRecordingUpstream(t, chatReply)
	gateway := testGateway(t, upstream.URL, config.ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{"m": "anthropic"}})

	postInference(t, gateway, "/v1/messages", `{"model":"m","messages":[],"max_output_tokens":512}`)

	if _, ok := upstream.body["max_output_tokens"]; ok {
		t.Fatalf("the upstream must not see the Responses field: %v", upstream.body)
	}
	if upstream.body["max_tokens"] != float64(512) {
		t.Fatalf("the limit must reach the upstream as max_tokens: %v", upstream.body)
	}
}

// A request that crosses protocols was already rebuilt from the bridge model,
// and must stay that way: the limit is the only field the two spellings
// share.
func TestCrossProtocolRequestKeepsTheLimit(t *testing.T) {
	upstream := newRecordingUpstream(t, chatReply)
	gateway := testGateway(t, upstream.URL, config.ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{}})

	postInference(t, gateway, "/v1/responses", `{"model":"m","input":"hi","max_output_tokens":512}`)

	if _, ok := upstream.body["max_output_tokens"]; ok {
		t.Fatalf("the upstream must not see the Responses field: %v", upstream.body)
	}
	if upstream.body["max_tokens"] != float64(512) {
		t.Fatalf("the limit must reach the upstream as max_tokens: %v", upstream.body)
	}
}
