package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// boolPtr is the shape ModelsConfig.ProbeFreeModels takes in JSON, where an
// absent field means "keep probing".
func boolPtr(value bool) *bool { return &value }

// TestProbeFreeModelsDefaultsToOn pins the compatibility rule: a configuration
// written before models.probe_free_models existed keeps the background probe,
// and Normalize materializes the effective value so the WebUI and the saved
// file never disagree with the running behavior.
func TestProbeFreeModelsDefaultsToOn(t *testing.T) {
	cfg, err := Normalize("", aliasConfig(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Models.FreeModelProbeEnabled() {
		t.Fatal("an absent models.probe_free_models must keep probing enabled")
	}
	if cfg.Models.ProbeFreeModels == nil {
		t.Fatal("Normalize must materialize models.probe_free_models")
	}
	encoded, err := json.Marshal(cfg.Models)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"probe_free_models":true`) {
		t.Fatalf("models must round-trip the probe switch, got %s", encoded)
	}
}

// TestProbeFreeModelsCanBeDisabled checks the switch itself, both through the
// helper and after a JSON round trip, because the management API sends the
// whole models object back.
func TestProbeFreeModelsCanBeDisabled(t *testing.T) {
	base := aliasConfig(nil)
	base.Models.ProbeFreeModels = boolPtr(false)
	cfg, err := Normalize("", base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.FreeModelProbeEnabled() {
		t.Fatal("models.probe_free_models=false must disable the probe")
	}
	encoded, err := json.Marshal(cfg.Models)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ModelsConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.FreeModelProbeEnabled() {
		t.Fatalf("the switch did not survive a round trip: %s", encoded)
	}
}

// TestCloneDoesNotShareProbeSwitch makes sure a clone owns its own switch, so a
// later edit of the copy cannot flip the probe on the configuration the
// running runtime was built from.
func TestCloneDoesNotShareProbeSwitch(t *testing.T) {
	original := aliasConfig(nil)
	original.Models.ProbeFreeModels = boolPtr(true)
	cloned := Clone(original)
	*cloned.Models.ProbeFreeModels = false
	if !original.Models.FreeModelProbeEnabled() {
		t.Fatal("Clone shared the probe switch with the original")
	}
}
