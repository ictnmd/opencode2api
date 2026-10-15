// Package gateway routes inference requests through managed upstream pools.
package gateway

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
	"opencode2api/internal/jsonutil"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
	"opencode2api/internal/rotation"
	"opencode2api/internal/telemetry"
)

const maxRequestBody = 32 << 20

const anonymousZenKey = "public"

// attemptDeadline is the per-attempt context deadline. It is armed only when
// attempt_timeout_seconds is set: otherwise the request-level budget already
// bounds the single attempt it would cover.
func attemptDeadline(cfg config.Config) time.Duration {
	if cfg.Performance.AttemptTimeoutSeconds <= 0 {
		return 0
	}
	return cfg.Performance.AttemptTimeout(time.Duration(cfg.Retry.TimeoutSeconds) * time.Second)
}

// statusClientClosedRequest is nginx's 499: the client went away before the
// gateway could answer, so the failure is neither upstream nor ours.
const statusClientClosedRequest = 499

type Gateway struct {
	rotation     *rotation.Manager
	cfg          config.Config
	logger       *slog.Logger
	transports   *transportPool
	zenNodes     *nodePool
	goNodes      *nodePool
	anonymous    *anonymousPool
	catalog      *models.Catalog
	monitor      *telemetry.Monitor
	availability *models.AvailabilityStore
	// attemptTimeout bounds a whole upstream attempt (dial + write + headers +
	// first event), not just the header wait. Zero keeps the legacy behavior.
	attemptTimeout time.Duration
}

func New(cfg config.Config, logger *slog.Logger, monitor *telemetry.Monitor) (*Gateway, error) {
	cfg.Rotation.Defaults()
	rotationManager, err := rotation.New("", cfg.Rotation)
	if err != nil {
		return nil, err
	}
	transports, err := newTransportPool(cfg.RuntimeProxies(), cfg.Performance, cfg.Performance.AttemptTimeout(time.Duration(cfg.Retry.TimeoutSeconds)*time.Second))
	if err != nil {
		return nil, err
	}
	cooldown := time.Duration(cfg.Performance.FailureCooldownSeconds) * time.Second
	zenNodes, err := newNodePool(cfg.ZenKeys, transports, cooldown)
	if err != nil {
		return nil, fmt.Errorf("zen node pool: %w", err)
	}
	goNodes, err := newNodePool(cfg.GoKeys, transports, cooldown)
	if err != nil {
		return nil, fmt.Errorf("go node pool: %w", err)
	}
	catalog := models.NewCatalog(cfg.Prefer, cfg.Models.Protocols)
	catalog.SetRefreshInterval(time.Duration(cfg.Models.RefreshSeconds) * time.Second)
	catalog.SetFreeOnly(cfg.Models.FreeOnly)
	return &Gateway{
		cfg:            cfg,
		rotation:       rotationManager,
		logger:         logger,
		transports:     transports,
		zenNodes:       zenNodes,
		goNodes:        goNodes,
		anonymous:      newAnonymousPool(cfg.Anonymous, transports, cooldown),
		catalog:        catalog,
		monitor:        monitor,
		attemptTimeout: attemptDeadline(cfg),
	}, nil
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", g.authenticate(g.handleModels))
	mux.HandleFunc("POST /v1/chat/completions", g.authenticate(g.handleInference(wire.Chat)))
	mux.HandleFunc("POST /v1/responses", g.authenticate(g.handleInference(wire.Responses)))
	mux.HandleFunc("POST /v1/messages", g.authenticate(g.handleInference(wire.Anthropic)))
	mux.HandleFunc("POST /v1/systemone", g.authenticate(g.handleSystemOne))
	mux.HandleFunc("GET /healthz", g.handleHealth)
	return telemetry.Recover(g.logger, mux)
}

func (g *Gateway) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		candidates := []string{strings.TrimSpace(r.Header.Get("x-api-key"))}
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			candidates = append(candidates, strings.TrimSpace(auth[7:]))
		}
		matched := ""
		for _, key := range g.cfg.ServerKeys {
			if !g.cfg.ServerKeyEnabled(key) {
				continue
			}
			for _, candidate := range candidates {
				if len(candidate) == len(key) && subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
					matched = key
				}
			}
		}
		if matched == "" {
			protocol := wire.Chat
			if r.URL.Path == "/v1/messages" {
				protocol = wire.Anthropic
			}
			wire.WriteError(w, protocol, http.StatusUnauthorized, "invalid local API key", "authentication_error", "")
			return
		}
		if meta := telemetry.MetaFromRequest(r); meta != nil {
			// The Playground borrows the first enabled key to pass this check, so
			// its traffic gets its own bucket instead of being billed to that key.
			meta.ServerKeyID = config.Fingerprint(matched)
			if isDiagnosticRequest(r.Context()) {
				meta.ServerKeyID = telemetry.PlaygroundServerKey
			}
		}
		next(w, r)
	}
}

func (g *Gateway) handleInference(external wire.Protocol) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
		if err != nil {
			wire.WriteError(w, external, http.StatusBadRequest, "request body is too large or unreadable", "invalid_request_error", "")
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			wire.WriteError(w, external, http.StatusBadRequest, "request body must be a JSON object", "invalid_request_error", "")
			return
		}
		model := jsonutil.StringAt(payload, "model")
		meta := telemetry.MetaFromRequest(r)
		if meta != nil {
			meta.Model = model
		}
		if model == "" {
			wire.WriteError(w, external, http.StatusBadRequest, "model is required", "invalid_request_error", "model")
			return
		}
		// An operator can publish a model under a different client-facing name.
		// Routing always follows the real model ID, while the name the client
		// used is retained so the reply can report it back unchanged.
		resolved := g.cfg.ResolveModel(model)
		aliased := resolved != model
		if aliased {
			// Every upstream encoding below reads the model from this payload,
			// including the same-protocol clone, so the real ID has to be
			// written back before the body is prepared. The client-facing name
			// stays in `model` for the response rewrite.
			payload["model"] = resolved
			if meta != nil {
				// Keep the two names distinguishable in logs and metrics: the
				// serving model is the real one, the alias is what the client
				// called it. This matches how rotation reports its own alias.
				meta.Model, meta.ModelAlias = resolved, model
			}
		}
		if id := g.rotation.GroupID(resolved); id != "" {
			g.handleRotation(w, r, external, payload, id, model)
			return
		}
		if !g.catalog.Supported(resolved) {
			wire.WriteError(w, external, http.StatusBadRequest, "the model uses an upstream protocol that opencode2api does not expose", "invalid_request_error", "model")
			return
		}
		route, err := g.catalog.Route(resolved, len(g.cfg.ZenKeys) > 0, len(g.cfg.GoKeys) > 0, g.cfg.Anonymous)
		if override, selected := debugKeyOverrideFrom(r.Context()); selected {
			// A per-key diagnostic must not silently be served by another key,
			// another tier or the anonymous lane.
			route, err = g.catalog.RouteForTier(resolved, override.Tier, len(g.cfg.ZenKeys) > 0, len(g.cfg.GoKeys) > 0)
		}
		if err != nil {
			// A routing error names the model it rejected, and that name is the
			// real ID. Report the name the client used instead, or an aliased
			// request would learn the real upstream model from a 400.
			wire.WriteError(w, external, http.StatusBadRequest, substituteModelName(err.Error(), model, resolved), "invalid_request_error", "model")
			return
		}
		if meta != nil {
			meta.Tier = string(route.Tier)
			meta.Protocol = route.Protocol
		}
		// A System One model has no message-shaped equivalent, so a decision
		// payload submitted on a message endpoint is relayed verbatim instead of
		// being converted. This keeps the model reachable for clients that can
		// only address /v1/chat/completions or /v1/responses — for example a
		// gateway whose OpenAI platform pins every request to Responses.
		if route.Protocol == wire.SystemOne {
			if len(jsonutil.MapAt(payload, "questions")) == 0 || payload["state"] == nil {
				wire.WriteError(w, external, http.StatusBadRequest, "this model uses System One decisions, not chat; send state and typed questions to /v1/systemone", "invalid_request_error", "model")
				return
			}
			// System One payloads are relayed verbatim, so the real model ID has
			// to be written back into the body for an aliased request.
			if aliased {
				payload["model"] = resolved
				encoded, encodeErr := json.Marshal(payload)
				if encodeErr != nil {
					wire.WriteError(w, external, http.StatusBadRequest, "request contains unsupported JSON values", "invalid_request_error", "")
					return
				}
				body = encoded
			}
			g.forwardSystemOne(w, r, body, payload, model, route, aliased)
			return
		}
		bodies, err := g.prepareRouteBodies(external, route, payload)
		if err != nil {
			wire.WriteError(w, external, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
			return
		}
		ids := identity.DeriveRequestIDs(r, payload)
		if meta != nil {
			meta.Request = ids.Request
			meta.Session = ids.Session
		}
		stream := jsonutil.BoolAt(payload, "stream")
		requestCtx, cancel := context.WithTimeout(r.Context(), time.Duration(g.cfg.Retry.TimeoutSeconds)*time.Second)
		defer cancel()
		resp, upstreamRoute, err := g.doUpstream(requestCtx, route, bodies, ids)
		if err != nil {
			finalTier := route.Tier
			if meta != nil && meta.Tier != "" {
				finalTier = config.Tier(meta.Tier)
			}
			keyID, channel, anonymous := requestCredential(requestCtx)
			// A downstream disconnect is not an upstream failure: report the
			// real cause so telemetry and clients see a cancel, not a 502.
			if !errors.Is(err, context.DeadlineExceeded) && wire.ClientCanceled(r.Context(), err) {
				g.logger.Debug("client canceled before upstream response", "component", "upstream", "event", "request_canceled", "request_id", ids.Request, "tier", finalTier, "key_id", keyID, "channel", channel, "anonymous", anonymous)
				if meta != nil {
					meta.Outcome = "client_canceled"
				}
				wire.WriteError(w, external, statusClientClosedRequest, "client closed request", "client_canceled", ids.Request)
				return
			}
			g.logger.Warn("all upstream attempts failed", "component", "upstream", "event", "request_failed", "request_id", ids.Request, "tier", finalTier, "key_id", keyID, "channel", channel, "anonymous", anonymous, "error", err)
			// An exhausted request budget is a timeout, not a bad gateway: the
			// distinction matters to clients that retry on 502.
			if errors.Is(err, context.DeadlineExceeded) {
				wire.WriteError(w, external, http.StatusGatewayTimeout, "upstream request timed out", "upstream_timeout", ids.Request)
				return
			}
			wire.WriteError(w, external, http.StatusBadGateway, "all upstream attempts failed", "upstream_error", ids.Request)
			return
		}
		defer resp.Body.Close()
		if meta != nil {
			meta.Tier = string(upstreamRoute.Tier)
			meta.Protocol = upstreamRoute.Protocol
		}
		w.Header().Set("x-request-id", ids.Request)
		if resp.StatusCode/100 != 2 {
			copyErrorResponse(w, external, resp, ids.Request, model, resolved)
			return
		}
		if stream {
			if meta != nil {
				meta.Stream = true
			}
			if g.monitor != nil {
				g.monitor.BeginStream()
				defer g.monitor.EndStream()
			}
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(resp.StatusCode)
			var usage wire.Usage
			var usageReported bool
			if external == upstreamRoute.Protocol {
				// Same-protocol streams are copied byte-for-byte, so an aliased
				// request needs its frames rewritten on the way through.
				var source io.Reader = resp.Body
				if aliased {
					source = wire.NewModelRewriteReader(source, model)
				}
				usage, usageReported, err = wire.ForwardStream(r.Context(), w, source, upstreamRoute.Protocol, model)
			} else {
				usage, usageReported, err = wire.TranscodeStream(r.Context(), w, resp.Body, upstreamRoute.Protocol, external, model, aliased)
			}
			if meta != nil {
				meta.Usage, meta.UsageReported = usage, usageReported
				if err != nil {
					meta.Outcome = "stream_error"
					if wire.ClientCanceled(r.Context(), err) {
						meta.Outcome = "client_canceled"
					}
				}
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				g.logger.Warn("downstream stream ended with an error", "component", "stream", "event", "stream_failed", "request_id", ids.Request, "model", resolved, "tier", upstreamRoute.Tier, "error", err)
			}
			return
		}
		responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			wire.WriteError(w, external, http.StatusBadGateway, "failed to read upstream response", "upstream_error", ids.Request)
			return
		}
		if upstreamRoute.Anonymous || (meta != nil && meta.Shaped) {
			// Key-tier shaped requests were force-streamed like the
			// anonymous lane; collapse the same way.
			// The anonymous lane is served streaming (see prepareAnonymousBody);
			// collapse the events back into the single document this
			// non-streaming client asked for.
			collapsed, err := wire.CollapseStream(bytes.NewReader(responseBody), upstreamRoute.Protocol, model)
			if err != nil {
				g.logger.Warn("anonymous stream collapse failed", "component", "conversion", "event", "anonymous_collapse_failed", "request_id", ids.Request, "model", resolved, "source_protocol", upstreamRoute.Protocol, "error", err)
				wire.WriteError(w, external, http.StatusBadGateway, "unsupported upstream response", "upstream_error", ids.Request)
				return
			}
			responseBody = collapsed
		}
		if usage, reported := wire.ResponseUsage(upstreamRoute.Protocol, responseBody); meta != nil {
			meta.Usage, meta.UsageReported = usage, reported
		}
		if external != upstreamRoute.Protocol {
			responseBody, err = wire.ConvertResponse(upstreamRoute.Protocol, external, responseBody)
			if err != nil {
				g.logger.Warn("response protocol conversion failed", "component", "conversion", "event", "response_conversion_failed", "request_id", ids.Request, "model", resolved, "source_protocol", upstreamRoute.Protocol, "target_protocol", external, "error", err)
				wire.WriteError(w, external, http.StatusBadGateway, "unsupported upstream response", "upstream_error", ids.Request)
				return
			}
		}
		// The client must see the name it asked for, whichever upstream served
		// the request. A conversion above may have rebuilt the document from the
		// upstream's model name, so this runs after it.
		if aliased {
			responseBody, _ = wire.RewriteResponseModel(responseBody, model)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(responseBody)
	}
}

// handleSystemOne serves System One decision requests on their own endpoint.
// Only models whose upstream protocol is System One can be served here; a chat
// or responses model belongs on its own endpoint, where the bridge can convert
// it.
func (g *Gateway) handleSystemOne(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		wire.WriteError(w, wire.SystemOne, http.StatusBadRequest, "request body is too large or unreadable", "invalid_request_error", "")
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		wire.WriteError(w, wire.SystemOne, http.StatusBadRequest, "request body must be a JSON object", "invalid_request_error", "")
		return
	}
	model := jsonutil.StringAt(payload, "model")
	if meta := telemetry.MetaFromRequest(r); meta != nil {
		meta.Model = model
	}
	if model == "" {
		wire.WriteError(w, wire.SystemOne, http.StatusBadRequest, "model is required", "invalid_request_error", "model")
		return
	}
	// Aliases apply here too, so an operator's client-facing name works on
	// every inference endpoint rather than only on the message-shaped ones.
	resolved := g.cfg.ResolveModel(model)
	aliased := resolved != model
	if aliased {
		if meta := telemetry.MetaFromRequest(r); meta != nil {
			meta.Model, meta.ModelAlias = resolved, model
		}
	}
	if !g.catalog.Supported(resolved) {
		wire.WriteError(w, wire.SystemOne, http.StatusBadRequest, "the model uses an upstream protocol that opencode2api does not expose", "invalid_request_error", "model")
		return
	}
	route, err := g.catalog.Route(resolved, len(g.cfg.ZenKeys) > 0, len(g.cfg.GoKeys) > 0, g.cfg.Anonymous)
	if err != nil {
		// Same rule as the message endpoints: a rejected alias reports the name
		// the client used, never the real upstream ID embedded in the error.
		wire.WriteError(w, wire.SystemOne, http.StatusBadRequest, substituteModelName(err.Error(), model, resolved), "invalid_request_error", "model")
		return
	}
	if route.Protocol != wire.SystemOne {
		wire.WriteError(w, wire.SystemOne, http.StatusBadRequest, fmt.Sprintf("the model does not use the %s protocol", wire.SystemOne), "invalid_request_error", "model")
		return
	}
	if aliased {
		// The payload is relayed verbatim, so the real ID has to be written back.
		payload["model"] = resolved
		encoded, encodeErr := json.Marshal(payload)
		if encodeErr != nil {
			wire.WriteError(w, wire.SystemOne, http.StatusBadRequest, "request contains unsupported JSON values", "invalid_request_error", "")
			return
		}
		body = encoded
	}
	g.forwardSystemOne(w, r, body, payload, model, route, aliased)
}

// forwardSystemOne relays a System One decision payload verbatim to the
// upstream systemone endpoint and returns the typed answer document unchanged.
// The payload pairs a free-form state with typed questions, which no
// message-shaped upstream protocol accepts, so it is never translated. Answers
// are non-streaming today; a streaming upstream reply is still relayed.
//
// aliased marks a request that addressed the model through a configured alias;
// it makes the relayed answer report the client's name instead of the real one.
func (g *Gateway) forwardSystemOne(w http.ResponseWriter, r *http.Request, body []byte, payload map[string]any, model string, route models.Route, aliased bool) {
	meta := telemetry.MetaFromRequest(r)
	if meta != nil {
		// Telemetry must report the model that actually served the request.
		// route.ID is the real upstream model ID, whereas model is the name
		// the client used, which differs only for a configured alias and is
		// already recorded in meta.ModelAlias by the caller. Assigning model
		// here overwrote the resolved ID, so the SystemOne lane disagreed
		// with every other lane and with the per-session burn and
		// per-server-key usage accounting.
		meta.Model = route.ID
		meta.Tier = string(route.Tier)
		meta.Protocol = route.Protocol
	}
	// One verbatim body serves every tier: a decision payload has no per-tier
	// encoding, so no protocol conversion is attempted.
	bodies := make(map[config.Tier][]byte, len(route.KeyTiers)+1)
	bodies[route.Tier] = body
	for _, tier := range route.KeyTiers {
		bodies[tier] = body
	}
	ids := identity.DeriveRequestIDs(r, payload)
	if meta != nil {
		meta.Request = ids.Request
		meta.Session = ids.Session
	}
	requestCtx, cancel := context.WithTimeout(r.Context(), time.Duration(g.cfg.Retry.TimeoutSeconds)*time.Second)
	defer cancel()
	resp, upstreamRoute, err := g.doUpstream(requestCtx, route, bodies, ids)
	if err != nil {
		finalTier := route.Tier
		if meta != nil && meta.Tier != "" {
			finalTier = config.Tier(meta.Tier)
		}
		keyID, channel, anonymous := requestCredential(requestCtx)
		if !errors.Is(err, context.DeadlineExceeded) && wire.ClientCanceled(r.Context(), err) {
			g.logger.Debug("client canceled before upstream response", "component", "upstream", "event", "request_canceled", "request_id", ids.Request, "tier", finalTier, "key_id", keyID, "channel", channel, "anonymous", anonymous)
			if meta != nil {
				meta.Outcome = "client_canceled"
			}
			wire.WriteError(w, wire.SystemOne, statusClientClosedRequest, "client closed request", "client_canceled", ids.Request)
			return
		}
		g.logger.Warn("all upstream attempts failed", "component", "upstream", "event", "request_failed", "request_id", ids.Request, "tier", finalTier, "key_id", keyID, "channel", channel, "anonymous", anonymous, "error", err)
		if errors.Is(err, context.DeadlineExceeded) {
			wire.WriteError(w, wire.SystemOne, http.StatusGatewayTimeout, "upstream request timed out", "upstream_timeout", ids.Request)
			return
		}
		wire.WriteError(w, wire.SystemOne, http.StatusBadGateway, "all upstream attempts failed", "upstream_error", ids.Request)
		return
	}
	defer resp.Body.Close()
	if meta != nil {
		meta.Tier = string(upstreamRoute.Tier)
		meta.Protocol = upstreamRoute.Protocol
	}
	w.Header().Set("x-request-id", ids.Request)
	if resp.StatusCode/100 != 2 {
		// route.ID is the real upstream model ID; model is the name the client
		// used, which differs only for a configured alias.
		copyErrorResponse(w, wire.SystemOne, resp, ids.Request, model, route.ID)
		return
	}
	if contentType := resp.Header.Get("Content-Type"); strings.HasPrefix(contentType, "text/event-stream") {
		if meta != nil {
			meta.Stream = true
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(resp.StatusCode)
		source := io.Reader(resp.Body)
		if aliased {
			source = wire.NewModelRewriteReader(source, model)
		}
		_, _ = io.Copy(w, source)
		return
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		wire.WriteError(w, wire.SystemOne, http.StatusBadGateway, "failed to read upstream response", "upstream_error", ids.Request)
		return
	}
	if usage, reported := wire.ResponseUsage(upstreamRoute.Protocol, responseBody); meta != nil {
		meta.Usage, meta.UsageReported = usage, reported
	}
	if aliased {
		responseBody, _ = wire.RewriteResponseModel(responseBody, model)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(responseBody)
}

func (g *Gateway) prepareRouteBodies(from wire.Protocol, route models.Route, input map[string]any) (map[config.Tier][]byte, error) {
	tiers := make([]config.Tier, 0, len(route.KeyTiers)+1)
	seen := make(map[config.Tier]bool, len(route.KeyTiers)+1)
	addTier := func(tier config.Tier) {
		if tier != config.TierZen && tier != config.TierGo || seen[tier] {
			return
		}
		seen[tier] = true
		tiers = append(tiers, tier)
	}
	addTier(route.Tier)
	for _, tier := range route.KeyTiers {
		addTier(tier)
	}
	if len(tiers) == 0 {
		return nil, errors.New("no usable upstream tier")
	}
	bodies := make(map[config.Tier][]byte, len(tiers))
	for _, tier := range tiers {
		protocol := route.ProtocolFor(tier)
		baseURL := g.cfg.Upstream.Zen
		if tier == config.TierGo {
			baseURL = g.cfg.Upstream.Go
		}
		upstreamPayload, err := wire.PrepareRequest(from, protocol, input, baseURL)
		if err != nil {
			if tier != route.Tier {
				// A fallback tier may use a stricter wire format than the
				// preferred tier. Do not reject a request before the preferred
				// upstream has even been tried; that tier is attempted only if
				// the request actually falls back.
				g.logger.Debug("skipping fallback tier with incompatible request shape", "component", "conversion", "event", "fallback_tier_skipped", "model", jsonutil.StringAt(input, "model"), "tier", tier, "protocol", protocol, "error", err)
				continue
			}
			return nil, fmt.Errorf("prepare %s upstream request: %w", tier, err)
		}
		if effort := g.resolvedEffort(jsonutil.StringAt(upstreamPayload, "model"), tier, protocol); effort != "" {
			wire.ForcedEffort(protocol, upstreamPayload, effort)
		}
		encoded, err := json.Marshal(upstreamPayload)
		if err != nil {
			return nil, errors.New("request contains unsupported JSON values")
		}
		bodies[tier] = encoded
	}
	return bodies, nil
}

// copyErrorResponse relays an upstream error to the client.
//
// An aliased request must not learn the real upstream model ID from the
// upstream's own error text: a rejection routinely names the model it was asked
// for, and that text is forwarded as the client-visible message. The name the
// client used is therefore substituted for the real ID, exactly as it is in a
// successful reply. A request that named the real ID directly is unaffected.
func copyErrorResponse(w http.ResponseWriter, protocol wire.Protocol, resp *http.Response, requestID, clientModel, realModel string) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	message := http.StatusText(resp.StatusCode)
	var value map[string]any
	if json.Unmarshal(body, &value) == nil {
		message = jsonutil.FirstString(jsonutil.StringAt(value, "error", "message"), jsonutil.StringAt(value, "message"), message)
	}
	message = substituteModelName(message, clientModel, realModel)
	wire.WriteError(w, protocol, resp.StatusCode, message, "upstream_error", requestID)
}

// modelNameByte reports whether a byte can appear inside a model ID. Configured
// names are letters, digits, dot, dash, underscore, colon or slash, so any other
// byte delimits a whole name.
func modelNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	switch b {
	case '.', '_', ':', '/', '-':
		return true
	}
	return false
}

// substituteModelName replaces whole occurrences of realModel with clientModel
// in a client-visible message.
//
// Only whole names are replaced. A longer model ID that merely embeds the real
// one — "muse-spark-1.3-contributor-free-pro" against a real ID of
// "muse-spark-1.3-contributor-free" — must survive intact, otherwise the
// message would name a model that does not exist. An empty or identical pair
// leaves the message byte-identical, which keeps an unaliased request
// unaffected. Every other byte is copied through unchanged.
func substituteModelName(message, clientModel, realModel string) string {
	if message == "" || clientModel == "" || realModel == "" || clientModel == realModel {
		return message
	}
	if !strings.Contains(message, realModel) {
		return message
	}
	var builder strings.Builder
	builder.Grow(len(message))
	for index := 0; index < len(message); {
		offset := strings.Index(message[index:], realModel)
		if offset < 0 {
			builder.WriteString(message[index:])
			break
		}
		start := index + offset
		after := start + len(realModel)
		builder.WriteString(message[index:start])
		// The match is a whole name only when neither neighbour belongs to a
		// model ID. A scan can land mid-name, so both sides are checked.
		if start > 0 && modelNameByte(message[start-1]) || after < len(message) && modelNameByte(message[after]) {
			builder.WriteString(realModel)
		} else {
			builder.WriteString(clientModel)
		}
		index = after
	}
	return builder.String()
}
