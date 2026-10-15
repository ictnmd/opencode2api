package gateway

import (
	"net/http"
	"slices"
	"sort"
	"time"

	"opencode2api/internal/buildinfo"
	"opencode2api/internal/config"
	"opencode2api/internal/httpx"
	modelcatalog "opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
)

type healthResponse struct {
	Status  string        `json:"status"`
	Ready   bool          `json:"ready"`
	Version string        `json:"version"`
	Models  healthModels  `json:"models"`
	Keys    healthKeys    `json:"keys"`
	Proxies healthProxies `json:"proxies"`
	Issues  []string      `json:"issues,omitempty"`
}

type healthModels struct {
	Status            string     `json:"status"`
	Total             int        `json:"total"`
	Exposed           int        `json:"exposed"`
	Zen               int        `json:"zen"`
	Go                int        `json:"go"`
	LastRefresh       *time.Time `json:"last_refresh,omitempty"`
	StaleAfterSeconds int        `json:"stale_after_seconds"`
	CacheSource       string     `json:"cache_source,omitempty"`
	Stale             bool       `json:"stale"`
}

type healthKeys struct {
	Zen       int  `json:"zen"`
	Go        int  `json:"go"`
	Total     int  `json:"total"`
	Anonymous bool `json:"anonymous"`
}

type healthProxies struct {
	Total     int `json:"total"`
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
}

func (g *Gateway) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_, models := g.availableModels()
	proxyTotal, proxyHealthy := g.transports.healthCounts()
	zenKeys, goKeys := g.zenNodes.Len(), g.goNodes.Len()
	staleAfter := max(2*time.Duration(g.cfg.Models.RefreshSeconds)*time.Second, time.Minute)

	modelStatus := "ready"
	var lastRefresh *time.Time
	issues := make([]string, 0, 3)
	if models.UpdatedAt.IsZero() {
		modelStatus = "pending"
		issues = append(issues, "model_catalog_pending")
	} else {
		updatedAt := models.UpdatedAt.UTC()
		lastRefresh = &updatedAt
		if models.Exposed == 0 {
			modelStatus = "empty"
			issues = append(issues, "model_catalog_empty")
		} else if models.Stale || time.Since(models.UpdatedAt) > staleAfter {
			modelStatus = "stale"
			issues = append(issues, "model_catalog_stale")
		}
	}
	if zenKeys+goKeys == 0 && !g.cfg.Anonymous {
		issues = append(issues, "no_upstream_keys")
	}
	if proxyHealthy == 0 {
		issues = append(issues, "no_healthy_proxies")
	}

	status := "ok"
	if len(issues) > 0 {
		status = "degraded"
	}
	blocking := modelStatus == "pending" || modelStatus == "empty" || proxyHealthy == 0
	httpStatus := http.StatusOK
	ready := !blocking
	if blocking {
		httpStatus = http.StatusServiceUnavailable
		if modelStatus == "pending" {
			status = "starting"
		}
	}
	httpx.WriteJSON(w, httpStatus, healthResponse{
		Status:  status,
		Ready:   ready,
		Version: buildinfo.Version,
		Models: healthModels{
			Status:            modelStatus,
			Total:             models.Total,
			Exposed:           models.Exposed,
			Zen:               models.Zen,
			Go:                models.Go,
			LastRefresh:       lastRefresh,
			StaleAfterSeconds: int(staleAfter / time.Second),
			CacheSource:       models.CacheSource,
			Stale:             models.Stale,
		},
		Keys: healthKeys{Zen: zenKeys, Go: goKeys, Total: zenKeys + goKeys, Anonymous: g.cfg.Anonymous},
		Proxies: healthProxies{
			Total:     proxyTotal,
			Healthy:   proxyHealthy,
			Unhealthy: proxyTotal - proxyHealthy,
		},
		Issues: issues,
	})
}

func (g *Gateway) availableModels() ([]modelcatalog.Route, modelcatalog.CatalogSnapshot) {
	return g.catalog.AvailableModels(g.zenNodes.Len() > 0, g.goNodes.Len() > 0, g.cfg.Anonymous)
}

// reasoningLevels are the effort values the gateway accepts and forwards, from
// lowest to highest. "auto" and "none" are gateway controls, not levels.
var reasoningLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// reasoningEfforts lists the levels a reasoning model can be asked for. When an
// "auto" probe has found the highest level the upstream accepts, the list stops
// there so clients do not offer levels that would be rejected.
func (g *Gateway) reasoningEfforts(model string, route modelcatalog.Route) []string {
	if g.cfg.ForcedEffort(model) == "auto" {
		if ceiling := slices.Index(reasoningLevels, g.resolvedEffort(model, route.Tier, route.Protocol)); ceiling >= 0 {
			return slices.Clone(reasoningLevels[:ceiling+1])
		}
	}
	return slices.Clone(reasoningLevels)
}

func (g *Gateway) handleModels(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().Unix()
	routes, _ := g.availableModels()
	data := make([]map[string]any, 0, len(routes))
	for _, route := range routes {
		data = append(data, g.modelEntry(route.ID, route.ID, route, now, ""))
	}
	for _, group := range g.rotation.Snapshot().Groups {
		if g.goNodes.Len() == 0 {
			continue
		}
		available := false
		for _, model := range group.Order {
			if route, err := g.catalog.RouteForTier(model, config.TierGo, false, true); err == nil && route.Protocol != wire.SystemOne {
				available = true
				break
			}
		}
		if !available {
			continue
		}
		data = append(data, map[string]any{"id": group.Alias, "object": "model", "created": now, "owned_by": "opencode-go-rotation"})
	}
	data = append(data, g.aliasModelEntries(now, data)...)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// modelEntry renders one /v1/models entry. id is the name the client sees and
// model is the real upstream model whose metadata and routing describe it; the
// two differ only for a configured alias. upstreamModel, when non-empty, is
// exposed as upstream_model so the indirection stays visible.
//
// Both the real-model and the alias path go through here on purpose: an alias
// must advertise exactly what the model it points at advertises, or a
// discovery-driven client sees a second-class model.
func (g *Gateway) modelEntry(id, model string, route modelcatalog.Route, now int64, upstreamModel string) map[string]any {
	// Tier-scoped metadata: the advertised context window must match
	// the tier that will serve the request (anonymous ⇒ Zen).
	md := g.catalog.MetadataForTier(model, route.Tier)
	entry := map[string]any{
		"id": id, "object": "model", "created": now, "owned_by": "opencode",
		"metadata":        md,
		"native_protocol": route.Protocol,
	}
	if upstreamModel != "" {
		entry["upstream_model"] = upstreamModel
	}
	if route.Protocol == wire.SystemOne {
		entry["supported_endpoints"] = []string{"/v1/systemone"}
	} else {
		entry["supported_endpoints"] = []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"}
	}
	// Top-level [OI]-standard fields: discovery clients (jcode, Pi, …)
	// read context/reasoning at the top level of each model entry.
	if md.ContextWindow > 0 {
		entry["context_window"] = md.ContextWindow
		entry["context_length"] = md.ContextWindow
	}
	if md.MaxInput > 0 {
		entry["max_input"] = md.MaxInput
	}
	if md.MaxOutput > 0 {
		entry["max_output"] = md.MaxOutput
	}
	if md.Reasoning {
		entry["reasoning"] = true
		entry["supports_reasoning"] = true
		efforts := g.reasoningEfforts(model, route)
		entry["reasoning_efforts"] = efforts
		// OpenClaw and similar harnesses read effort levels from compat
		// rather than inferring them, and otherwise offer only off/max.
		entry["compat"] = map[string]any{"supportsReasoningEffort": true, "supportedReasoningEfforts": efforts}
		if effort := g.resolvedEffort(model, route.Tier, route.Protocol); effort != "" && effort != "none" {
			entry["default_reasoning_effort"] = effort
		}
	}
	if md.Cost != nil {
		cost := map[string]any{}
		// Camel-case aliases match OpenClaw's model cost shape.
		for _, field := range []struct {
			names []string
			value *float64
		}{
			{[]string{"input"}, md.Cost.Input},
			{[]string{"output"}, md.Cost.Output},
			{[]string{"cache_read", "cacheRead"}, md.Cost.CacheRead},
			{[]string{"cache_write", "cacheWrite"}, md.Cost.CacheWrite},
		} {
			if field.value == nil {
				continue
			}
			for _, name := range field.names {
				cost[name] = *field.value
			}
		}
		entry["cost"] = cost
	}
	if md.ToolCall {
		entry["tool_call"] = true
	}
	if md.StructuredOutput {
		entry["structured_output"] = true
	}
	return entry
}

// aliasModelEntries advertises configured model aliases so a discovery-driven
// client can pick one up and address the model by the name the operator chose.
// An alias whose real model is not currently routable is omitted, exactly like
// the real model itself would be; the entry carries the real ID in
// upstream_model so the indirection stays visible to operators and clients.
//
// An alias that shadows a real model ID (a deliberate in-place rename) is not
// listed twice: the real entry already advertises that name.
//
// Routing the alias target through catalog.Route also keeps discovery
// consistent with models.free_only: an alias pointing at a paid model is
// hidden here for the same reason the model itself is.
func (g *Gateway) aliasModelEntries(now int64, existing []map[string]any) []map[string]any {
	aliases := g.cfg.Models.Aliases
	if len(aliases) == 0 {
		return nil
	}
	listed := make(map[string]bool, len(existing)+len(aliases))
	for _, entry := range existing {
		if id, ok := entry["id"].(string); ok {
			listed[id] = true
		}
	}
	names := make([]string, 0, len(aliases))
	for alias := range aliases {
		if !listed[alias] {
			names = append(names, alias)
		}
	}
	sort.Strings(names)
	entries := make([]map[string]any, 0, len(names))
	for _, alias := range names {
		model := aliases[alias]
		route, err := g.catalog.Route(model, g.zenNodes.Len() > 0, g.goNodes.Len() > 0, g.cfg.Anonymous)
		if err != nil {
			continue
		}
		entries = append(entries, g.modelEntry(alias, model, route, now, model))
	}
	return entries
}
