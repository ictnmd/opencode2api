package gateway

import (
	"context"
	"testing"

	"opencode2api/internal/config"
)

// probeSwitchConfig is the smallest configuration that carries the free-model
// probe switch; syncAvailabilityChecks only reads that field.
func probeSwitchConfig(enabled bool) config.Config {
	value := enabled
	return config.Config{Models: config.ModelsConfig{ProbeFreeModels: &value}}
}

// TestSyncAvailabilityChecksFollowsTheSwitch pins the wiring: the probe loop
// runs while models.probe_free_models is on, stops when an applied
// configuration turns it off, and starts again when it is turned back on. The
// loop touches no upstream here because the controller has no current runtime,
// so checkFreeModels returns immediately.
func TestSyncAvailabilityChecksFollowsTheSwitch(t *testing.T) {
	controller := &RuntimeController{root: context.Background()}
	t.Cleanup(controller.stopAvailabilityChecks)

	controller.syncAvailabilityChecks(probeSwitchConfig(true))
	if controller.availabilityCancel == nil {
		t.Fatal("the probe loop did not start for an enabled switch")
	}
	controller.syncAvailabilityChecks(probeSwitchConfig(true))
	if controller.availabilityCancel == nil {
		t.Fatal("a second enabled apply must leave the running loop alone")
	}

	controller.syncAvailabilityChecks(probeSwitchConfig(false))
	if controller.availabilityCancel != nil {
		t.Fatal("the probe loop kept running after the switch was turned off")
	}
	controller.stopAvailabilityChecks()
	if controller.availabilityCancel != nil {
		t.Fatal("stopping an already stopped loop must be a no-op")
	}

	controller.syncAvailabilityChecks(probeSwitchConfig(true))
	if controller.availabilityCancel == nil {
		t.Fatal("the probe loop did not restart after the switch was turned back on")
	}
}

// TestProbeEnabledReportsTheAppliedSwitch checks the value the availability
// page reads, including the absent-field default.
func TestProbeEnabledReportsTheAppliedSwitch(t *testing.T) {
	controller := &RuntimeController{root: context.Background()}
	if !controller.ProbeEnabled() {
		t.Fatal("a controller without a runtime must report the default (enabled)")
	}
	disabled := probeSwitchConfig(false)
	controller.current.Store(&gatewayRuntime{config: disabled})
	if controller.ProbeEnabled() {
		t.Fatal("ProbeEnabled ignored models.probe_free_models=false")
	}
}
