package config

import (
	"os"
	"path/filepath"
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

// The exact operator configuration from the requirement:
//
//	O2A_MODEL_ALIASES={"muse-spark-1.3":"muse-spark-1.3-contributor-free"}
//
// The alias is the client-facing name and the target is the real upstream ID;
// resolution is one hop and the real ID stays addressable in its own right.
func TestNormalizeAcceptsMuseSparkContributorAlias(t *testing.T) {
	const (
		requested = "muse-spark-1.3"
		real      = "muse-spark-1.3-contributor-free"
	)
	cfg, err := Normalize("", aliasConfig(map[string]string{requested: real}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolveModel(requested); got != real {
		t.Fatalf("ResolveModel(%q) = %q, want %q", requested, got, real)
	}
	if got := cfg.ResolveModel(real); got != real {
		t.Fatalf("the real model ID must stay addressable, got %q", got)
	}
	if got := cfg.ResolveModel("muse-spark-1.2"); got != "muse-spark-1.2" {
		t.Fatalf("an unrelated model must resolve to itself, got %q", got)
	}
}

// A rotation group alias must not be shadowed by an operator alias, and an
// operator alias must not point into a group: either direction would make a
// group request resolve to something the operator never named. Both group
// aliases are checked, in both directions.
func TestNormalizeRejectsRotationGroupCollisions(t *testing.T) {
	cases := map[string]map[string]string{
		"target sota":  {"gpt-5.5": "sota"},
		"target sweet": {"gpt-5.5": "sweet"},
		"name sota":    {"sota": "gpt-5.6"},
		"name sweet":   {"sweet": "gpt-5.6"},
	}
	for name, aliases := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Normalize("", aliasConfig(aliases))
			if err == nil || !strings.Contains(err.Error(), "rotation group alias") {
				t.Fatalf("expected a rotation collision rejection, got %v", err)
			}
		})
	}
}

// The target side of the map is validated with the same name rule as the alias
// side: a target that is empty, whitespace-bearing, badly prefixed or longer
// than 128 characters must be rejected rather than silently accepted.
func TestNormalizeRejectsInvalidAliasTarget(t *testing.T) {
	for _, target := range []string{"", "has space", "-leading-dash", ".dot-first", "tab\tname", strings.Repeat("a", 129)} {
		if _, err := Normalize("", aliasConfig(map[string]string{"alias": target})); err == nil {
			t.Fatalf("expected target %q to be rejected", target)
		}
	}
}

// An alias may not be used as another alias's target, and the check must hold
// for the muse-spark pair as well: mapping it at a second alias would make
// resolution order depend on map iteration.
func TestNormalizeRejectsMuseSparkAliasChain(t *testing.T) {
	const (
		requested = "muse-spark-1.3"
		real      = "muse-spark-1.3-contributor-free"
	)
	_, err := Normalize("", aliasConfig(map[string]string{requested: real, real: "muse-spark-1.3-other"}))
	if err == nil || !strings.Contains(err.Error(), "another alias") {
		t.Fatalf("expected a chain rejection, got %v", err)
	}
}

// The operator supplies the map as one JSON object through the environment:
//
//	O2A_MODEL_ALIASES={"muse-spark-1.3":"muse-spark-1.3-contributor-free"}
//
// The entrypoint writes that object into models.aliases, so this proves the
// exact JSON text the operator types survives config load and normalization.
func TestLoadMuseSparkAliasFromJSON(t *testing.T) {
	const aliasJSON = `{"muse-spark-1.3":"muse-spark-1.3-contributor-free"}`
	path := filepath.Join(t.TempDir(), "config.json")
	contents := `{"server_keys":["sk-test"],"anonymous":true,"models":{"aliases":` + aliasJSON + `}}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolveModel("muse-spark-1.3"); got != "muse-spark-1.3-contributor-free" {
		t.Fatalf("ResolveModel(muse-spark-1.3) = %q", got)
	}
	// The loaded map must be exactly the operator's one entry.
	if len(cfg.Models.Aliases) != 1 {
		t.Fatalf("expected exactly one alias, got %v", cfg.Models.Aliases)
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
