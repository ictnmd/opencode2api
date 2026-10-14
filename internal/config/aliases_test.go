package config

import (
	"strings"
	"testing"
)

func aliasConfig(aliases map[string]string) Config {
	cfg := Config{
		Listen:     "127.0.0.1:8080",
		ServerKeys: []string{"sk-test"},
		Anonymous:  true,
		Upstream:   UpstreamConfig{Zen: "https://opencode.ai/zen", Go: "https://opencode.ai/zen/go"},
		Retry:      RetryConfig{MaxAttempts: 3, TimeoutSeconds: 300},
		Models:     ModelsConfig{RefreshSeconds: 300, Protocols: map[string]string{}, Aliases: aliases},
		Performance: PerformanceConfig{
			MaxIdleConns: 8, MaxIdleConnsPerHost: 8, IdleConnTimeoutSeconds: 30,
			ConnectTimeoutSeconds: 5, FailureCooldownSeconds: 15,
		},
		Logging: LoggingConfig{Level: "info", RingSize: 100},
		Prefer:  TierGo,
	}
	return cfg
}

func TestNormalizeAcceptsAliasMap(t *testing.T) {
	cfg, err := Normalize("", aliasConfig(map[string]string{"gpt-5.5": "gpt-5.6"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResolveModel("gpt-5.5") != "gpt-5.6" {
		t.Fatalf("alias did not resolve: %q", cfg.ResolveModel("gpt-5.5"))
	}
	// A model without an alias resolves to itself.
	if cfg.ResolveModel("glm-5.3") != "glm-5.3" {
		t.Fatal("unaliased model must resolve to itself")
	}
}

func TestNormalizeRejectsAliasChains(t *testing.T) {
	_, err := Normalize("", aliasConfig(map[string]string{"a": "b", "b": "c"}))
	if err == nil || !strings.Contains(err.Error(), "another alias") {
		t.Fatalf("expected a chain rejection, got %v", err)
	}
}

func TestNormalizeRejectsSelfAlias(t *testing.T) {
	_, err := Normalize("", aliasConfig(map[string]string{"a": "a"}))
	if err == nil || !strings.Contains(err.Error(), "itself") {
		t.Fatalf("expected a self-mapping rejection, got %v", err)
	}
}

func TestNormalizeRejectsRotationGroupTarget(t *testing.T) {
	_, err := Normalize("", aliasConfig(map[string]string{"gpt-5.5": "sota"}))
	if err == nil || !strings.Contains(err.Error(), "rotation group alias") {
		t.Fatalf("expected a rotation collision rejection, got %v", err)
	}
}

func TestNormalizeRejectsInvalidAliasName(t *testing.T) {
	for _, alias := range []string{"has space", "", "-leading-dash"} {
		if _, err := Normalize("", aliasConfig(map[string]string{alias: "real"})); err == nil {
			t.Fatalf("expected %q to be rejected", alias)
		}
	}
}

func TestNormalizeDropsEmptyAliasMap(t *testing.T) {
	cfg, err := Normalize("", aliasConfig(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.Aliases != nil {
		t.Fatal("an empty alias map must be dropped so it is not persisted")
	}
}

func TestCloneCopiesAliases(t *testing.T) {
	cfg, err := Normalize("", aliasConfig(map[string]string{"a": "b"}))
	if err != nil {
		t.Fatal(err)
	}
	clone := Clone(cfg)
	clone.Models.Aliases["a"] = "mutated"
	if cfg.Models.Aliases["a"] != "b" {
		t.Fatal("Clone must deep-copy the alias map")
	}
}
