package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"opencode2api/internal/config"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
)

// freeOnlyModels is the models config most of these tests start from: free_only
// on, discovery empty, so the name convention alone decides what is free.
func freeOnlyModels(aliases map[string]string) config.ModelsConfig {
	return config.ModelsConfig{
		RefreshSeconds: 300,
		Protocols:      map[string]string{},
		Aliases:        aliases,
		FreeOnly:       true,
	}
}

// chatCompletionBody is the minimal upstream reply these tests relay back.
const chatCompletionBody = `{"id":"c1","object":"chat.completion","created":1,"model":"glm-4.6-free","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{}}`

// A paid model must be refused before any upstream is contacted, otherwise
// models.free_only would be advisory rather than a limit.
func TestFreeOnlyRejectsPaidModel(t *testing.T) {
	var reached atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
	}))
	defer upstream.Close()

	gateway := testGateway(t, upstream.URL, freeOnlyModels(nil))
	recorder := postInference(t, gateway, "/v1/chat/completions", `{"model":"gpt-5.6","messages":[]}`)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("paid model must be rejected with 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "is not free") {
		t.Fatalf("the rejection must name the cause: %s", recorder.Body.String())
	}
	if reached.Load() {
		t.Fatal("a paid model reached the upstream while models.free_only was enabled")
	}
}

// free_only limits routing to free models; it must not break them.
func TestFreeOnlyStillServesFreeModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatCompletionBody)
	}))
	defer upstream.Close()

	gateway := testGateway(t, upstream.URL, freeOnlyModels(nil))
	recorder := postInference(t, gateway, "/v1/chat/completions", `{"model":"glm-4.6-free","messages":[]}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("a free model must keep working: %d %s", recorder.Code, recorder.Body.String())
	}
}

// Discovery must not advertise a paid model, whether it is addressed directly
// or through an alias.
func TestFreeOnlyHidesPaidModelsFromDiscovery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	gateway := testGateway(t, upstream.URL, freeOnlyModels(map[string]string{"my-fast": "gpt-5.6"}))
	gateway.catalog.ReplaceWithCapabilities(nil, []string{"gpt-5.6", "glm-4.6-free"},
		map[config.Tier]map[string]wire.Protocol{
			config.TierGo: {"gpt-5.6": wire.Chat, "glm-4.6-free": wire.Chat},
		}, nil, nil)

	body := getModelList(t, gateway)

	if strings.Contains(body, `"id":"gpt-5.6"`) {
		t.Fatalf("paid model leaked into discovery: %s", body)
	}
	if strings.Contains(body, `"id":"my-fast"`) {
		t.Fatalf("an alias pointing at a paid model leaked into discovery: %s", body)
	}
	if !strings.Contains(body, `"id":"glm-4.6-free"`) {
		t.Fatalf("the free model must still be advertised: %s", body)
	}
}

// The alias must be refused at inference time too, not merely hidden.
func TestFreeOnlyRejectsAliasPointingAtPaidModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	gateway := testGateway(t, upstream.URL, freeOnlyModels(map[string]string{"my-fast": "gpt-5.6"}))
	recorder := postInference(t, gateway, "/v1/chat/completions", `{"model":"my-fast","messages":[]}`)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("alias to a paid model must be rejected with 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "is not free") {
		t.Fatalf("the rejection must name the cause: %s", recorder.Body.String())
	}
}

// An alias whose target is free stays fully usable, and the client keeps seeing
// the name it asked for.
func TestFreeOnlyKeepsAliasPointingAtFreeModel(t *testing.T) {
	var seenModel atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if model, ok := payload["model"].(string); ok {
			seenModel.Store(model)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatCompletionBody)
	}))
	defer upstream.Close()

	gateway := testGateway(t, upstream.URL, freeOnlyModels(map[string]string{"my-free": "glm-4.6-free"}))

	body := getModelList(t, gateway)
	if !strings.Contains(body, `"id":"my-free"`) {
		t.Fatalf("alias to a free model must be advertised: %s", body)
	}
	if !strings.Contains(body, `"upstream_model":"glm-4.6-free"`) {
		t.Fatalf("alias entry must name the real model: %s", body)
	}

	recorder := postInference(t, gateway, "/v1/chat/completions", `{"model":"my-free","messages":[]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("alias to a free model must be served: %d %s", recorder.Code, recorder.Body.String())
	}
	if got, _ := seenModel.Load().(string); got != "glm-4.6-free" {
		t.Fatalf("upstream must receive the real model ID, got %q", got)
	}
	if !strings.Contains(recorder.Body.String(), `"model":"my-free"`) {
		t.Fatalf("response must echo the client name: %s", recorder.Body.String())
	}
}

func ptr(v float64) *float64 { return &v }

// An alias entry must carry exactly the capabilities the real model entry
// carries. Before the entries shared one builder, the alias path silently
// dropped everything upstream added later (cost, reasoning_efforts, compat), so
// a discovery-driven client saw the alias as a second-class model.
func TestAliasEntryCarriesTheSameCapabilitiesAsTheRealModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	gateway := testGateway(t, upstream.URL, config.ModelsConfig{
		RefreshSeconds: 300,
		Protocols:      map[string]string{},
		Aliases:        map[string]string{"gpt-5.5": "gpt-5.6"},
	})
	gateway.catalog.ReplaceWithCapabilities(nil, []string{"gpt-5.6"},
		map[config.Tier]map[string]wire.Protocol{
			config.TierGo: {"gpt-5.6": wire.Chat},
		}, nil,
		map[config.Tier]map[string]models.Metadata{
			config.TierGo: {
				"gpt-5.6": {
					ContextWindow:    400000,
					MaxInput:         272000,
					MaxOutput:        128000,
					Reasoning:        true,
					ToolCall:         true,
					StructuredOutput: true,
					Cost: &models.Cost{
						Input:     ptr(1.5),
						Output:    ptr(2.5),
						CacheRead: ptr(0.25),
					},
				},
			},
		})

	body := getModelList(t, gateway)

	entries := map[string]map[string]any{}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode /v1/models: %v", err)
	}
	for _, entry := range list.Data {
		if id, ok := entry["id"].(string); ok {
			entries[id] = entry
		}
	}

	real, ok := entries["gpt-5.6"]
	if !ok {
		t.Fatalf("the real model is missing from discovery: %s", body)
	}
	alias, ok := entries["gpt-5.5"]
	if !ok {
		t.Fatalf("the alias is missing from discovery: %s", body)
	}

	// The fields that only exist when the shared builder is used.
	for _, field := range []string{"context_window", "max_input", "max_output", "reasoning_efforts", "compat", "cost"} {
		if _, present := real[field]; !present {
			t.Fatalf("test setup: the real entry lacks %q, so it cannot prove anything: %s", field, body)
		}
		if _, present := alias[field]; !present {
			t.Fatalf("alias entry lacks %q, which the real entry has: %s", field, body)
		}
	}

	// The alias must describe the model it points at, and only its own name
	// must differ.
	if alias["upstream_model"] != "gpt-5.6" {
		t.Fatalf("alias entry must name the real model: %v", alias["upstream_model"])
	}
	if _, present := real["upstream_model"]; present {
		t.Fatalf("a real entry must not claim an upstream_model: %s", body)
	}
	for field, want := range real {
		if field == "id" || field == "upstream_model" {
			continue
		}
		got, _ := json.Marshal(alias[field])
		wantJSON, _ := json.Marshal(want)
		if string(got) != string(wantJSON) {
			t.Fatalf("alias entry differs from the real entry at %q:\n got %s\nwant %s", field, got, wantJSON)
		}
	}
}
