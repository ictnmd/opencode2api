package gateway

import (
	"context"

	"errors"

	"time"

	"opencode2api/internal/config"
	"opencode2api/internal/models"
)

// AvailabilitySnapshot returns the availability state of every free model in
// the current runtime's catalog.
func (m *RuntimeController) AvailabilitySnapshot() []models.Availability {
	items := []models.Availability{}
	r := m.current.Load()
	if r == nil {
		return items
	}
	for _, model := range r.gateway.catalog.List() {
		if r.gateway.catalog.IsFreeModel(model) {
			items = append(items, r.availability.Get(model))
		}
	}
	return items
}

// RestoreModel re-enables a free model by putting it under manual control
// (see AvailabilityRegistry.Restore), which also discards the results of
// probes already in flight. It fails for a model that is not a free model of
// the current catalog.
func (m *RuntimeController) RestoreModel(model string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	r := m.current.Load()
	if r == nil {
		return errors.New("gateway runtime is unavailable")
	}
	for _, item := range m.AvailabilitySnapshot() {
		if item.Model == model {
			return r.availability.Restore(model, time.Now().UTC())
		}
	}
	return errors.New("unknown free model")
}

// SetManualModel switches a free model between manual and automatic control.
// Manual models stay enabled and are excluded from automatic probing.
func (m *RuntimeController) SetManualModel(model string, manual bool) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	r := m.current.Load()
	if r == nil {
		return errors.New("gateway runtime is unavailable")
	}
	for _, item := range m.AvailabilitySnapshot() {
		if item.Model == model {
			return r.availability.SetManual(model, manual, time.Now().UTC())
		}
	}
	return errors.New("unknown free model")
}

// startAvailabilityChecks starts the free-model probe loop unless it is
// already running. The loop runs on the controller's root context, so it
// survives runtime swaps.
func (m *RuntimeController) startAvailabilityChecks() {
	m.availabilityMu.Lock()
	defer m.availabilityMu.Unlock()
	m.startAvailabilityChecksLocked()
}

// startAvailabilityChecksLocked starts the loop when no cancel func is stored;
// the caller holds availabilityMu.
func (m *RuntimeController) startAvailabilityChecksLocked() {
	if m.availabilityCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(m.root)
	m.availabilityCancel = cancel
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			m.checkFreeModels(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// stopAvailabilityChecks cancels the probe loop when it is running. A probe in
// flight sees its context end and records nothing.
func (m *RuntimeController) stopAvailabilityChecks() {
	m.availabilityMu.Lock()
	defer m.availabilityMu.Unlock()
	if m.availabilityCancel == nil {
		return
	}
	m.availabilityCancel()
	m.availabilityCancel = nil
}

// syncAvailabilityChecks starts or stops the free-model probe loop so it
// matches cfg. Turning models.probe_free_models off therefore takes effect on
// the next configuration apply, without a restart, and turning it back on
// probes every due model at once.
func (m *RuntimeController) syncAvailabilityChecks(cfg config.Config) {
	if cfg.Models.FreeModelProbeEnabled() {
		m.startAvailabilityChecks()
		return
	}
	m.stopAvailabilityChecks()
}

// ProbeEnabled reports whether the background free-model probe is enabled by
// the current configuration, so the availability page can explain a state that
// will not change on its own.
func (m *RuntimeController) ProbeEnabled() bool {
	return m.Config().Models.FreeModelProbeEnabled()
}

// checkFreeModels probes, one at a time, every free model under automatic
// control whose next check is due. A result is recorded only while the
// runtime that ran the probe is still current.
func (m *RuntimeController) checkFreeModels(ctx context.Context) {
	for _, item := range m.AvailabilitySnapshot() {
		if ctx.Err() != nil {
			return
		}
		r := m.current.Load()
		if r == nil {
			return
		}
		item = r.availability.Get(item.Model)
		if item.Manual {
			continue
		}
		if time.Now().Before(item.NextCheck) {
			continue
		}
		result := r.gateway.probeFreeModel(ctx, item.Model)
		if !result.attempted || ctx.Err() != nil {
			// No request left the process, so nothing was observed. Logging the
			// reason at debug is what tells an operator why a due model never
			// shows a new check time (no lane, paused breaker, unsupported
			// protocol).
			m.logger.Debug("free model availability probe skipped", "component", "models", "event", "availability_skipped", "model", item.Model, "reason", result.reason)
			continue
		}
		// Config changes may replace credentials or endpoints during a probe.
		m.updateMu.Lock()
		if m.current.Load() == r {
			if err := r.availability.Record(item.Model, item.Generation, result.success, result.reason, result.channel, time.Now().UTC(), result.effort); err != nil {
				m.logger.Warn("model availability state save failed", "error", err)
			}
			m.logger.Info("free model availability checked", "component", "models", "event", "availability_checked", "model", item.Model, "available", result.success, "reason", result.reason, "channel", result.channel)
		}
		m.updateMu.Unlock()
	}
}
