package config

import (
	"path/filepath"
	"runtime"
	"testing"
)

// The example config is the seed every fresh container copies before the
// O2A_* overlay touches it, so it must always stay parseable by the strict
// loader. Load uses DisallowUnknownFields, which turns a stray key — for
// example an "errors" block nested under "reasoning" — into a startup
// failure, so an example that drifted from the Config shape has to fail this
// test instead of shipping.
func TestLoadExampleConfig(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "config.example.json")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("config.example.json fails Load: %v", err)
	}
	if len(cfg.ServerKeys) == 0 {
		t.Error("config.example.json must contain at least one server key")
	}
	// The "errors" block lives at the top level; assert the reasoning block
	// did not swallow it by checking both decode independently.
	if cfg.Errors.SimpleBody {
		t.Error("config.example.json must not enable errors.simple_body by default")
	}
	if cfg.Reasoning.Effort != "high" {
		t.Errorf("config.example.json reasoning.effort = %q, want %q", cfg.Reasoning.Effort, "high")
	}
}
