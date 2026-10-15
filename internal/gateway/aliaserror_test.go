package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
