package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"opencode2api/internal/config"
	wire "opencode2api/internal/protocol"
)

// An upstream rejection routinely names the model it was asked for, and that
// text is forwarded as the client-visible error message. For an aliased request
// it must be rewritten to the name the client used, exactly like a successful
// reply, or the client learns the real upstream ID from the failure.
func TestMuseSparkAliasRewritesRealModelInUpstreamError(t *testing.T) {
	const upstreamError = `{"error":{"message":"model muse-spark-1.3-contributor-free is not available on this plan","type":"invalid_request_error"},"model":"muse-spark-1.3-contributor-free"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, upstreamError)
	}))
	defer upstream.Close()

	gateway := museSparkGateway(t, upstream.URL, wire.Chat)
	recorder := postAliasInference(t, gateway, "/v1/chat/completions",
		`{"model":"`+museSparkAlias+`","messages":[{"role":"user","content":"hi"}]}`)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("the upstream status must be relayed: %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, museSparkModel) {
		t.Fatalf("real model ID leaked to the client through the error message: %s", body)
	}
	if !strings.Contains(body, museSparkAlias) {
		t.Fatalf("the error must name the model the client asked for: %s", body)
	}
}

// A request that names the real ID directly must keep the upstream's own error
// text untouched: the rewrite applies only when an alias actually resolved.
func TestNoAliasLeavesUpstreamErrorTextUntouched(t *testing.T) {
	const upstreamError = `{"error":{"message":"model muse-spark-1.3-contributor-free is not available on this plan","type":"invalid_request_error"}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, upstreamError)
	}))
	defer upstream.Close()

	gateway := museSparkGateway(t, upstream.URL, wire.Chat)
	recorder := postAliasInference(t, gateway, "/v1/chat/completions",
		`{"model":"`+museSparkModel+`","messages":[{"role":"user","content":"hi"}]}`)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "muse-spark-1.3-contributor-free is not available") {
		t.Fatalf("an unaliased request must keep the upstream error text: %s", recorder.Body.String())
	}
}

// A routing error raised before any upstream call embeds the real model ID in
// its message. An aliased request must not learn that ID from the 400 either,
// so the same substitution applies on this path.
func TestMuseSparkAliasRewritesRealModelInRoutingError(t *testing.T) {
	// A real ID that is not free, with free_only enabled, is the cheapest way to
	// make catalog.Route() reject the request with a message naming the model.
	// The real ID here has no "-free" suffix on purpose: the requirement's own
	// target ends in "-free" and is therefore eligible, so it cannot exercise
	// this rejection.
	const paidModel = "muse-spark-1.3-contributor"
	gateway := testGateway(t, "http://127.0.0.1:1", config.ModelsConfig{
		RefreshSeconds: 300,
		Protocols:      map[string]string{paidModel: string(wire.Chat)},
		Aliases:        map[string]string{museSparkAlias: paidModel},
		FreeOnly:       true,
	})
	recorder := postAliasInference(t, gateway, "/v1/chat/completions",
		`{"model":"`+museSparkAlias+`","messages":[{"role":"user","content":"hi"}]}`)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("a routing rejection must be a 400: %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, paidModel) {
		t.Fatalf("real model ID leaked to the client through the routing error: %s", body)
	}
	if !strings.Contains(body, museSparkAlias) {
		t.Fatalf("the routing error must name the model the client asked for: %s", body)
	}
}

// substituteModelName must rewrite whole model names only. A longer ID that
// merely embeds the real one has to survive untouched, or the message would
// advertise a model that does not exist.
func TestSubstituteModelNameRewritesWholeNamesOnly(t *testing.T) {
	const real, alias = "muse-spark-1.3-contributor-free", "muse-spark-1.3"
	cases := []struct {
		name, in, want string
	}{
		{"whole name", "model " + real + " is unavailable", "model " + alias + " is unavailable"},
		{"longer name embedding the real ID is preserved",
			"try " + real + "-pro instead", "try " + real + "-pro instead"},
		{"prefixed longer name is preserved",
			"try x" + real, "try x" + real},
		{"repeated whole names",
			real + " and " + real, alias + " and " + alias},
		{"quoted name", `model "` + real + `" rejected`, `model "` + alias + `" rejected`},
		{"no match", "nothing to rewrite", "nothing to rewrite"},
		{"empty message", "", ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := substituteModelName(test.in, alias, real); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
	// An unaliased request must come through byte-identical.
	if got := substituteModelName("model "+real+" is unavailable", real, real); got != "model "+real+" is unavailable" {
		t.Fatalf("an identical pair must not rewrite: %q", got)
	}
}
