package connection

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type savedTelemetrySettings struct {
	PanelURL string               `json:"panel_url"`
	NodeID   string               `json:"node_id"`
	Settings v1.TelemetrySettings `json:"settings"`
}

// telemetrySettings is owned by the scheduler. Persistence is independent of
// legacy settings and live lease state; a restart never restores a live lease.
type telemetrySettings struct {
	panel, node string
	current     *v1.TelemetrySettings
	write       func(string, any) error
}

func loadTelemetrySettings(store *state.Store, id state.Identity) (*telemetrySettings, error) {
	settings := &telemetrySettings{panel: id.PanelURL, node: id.NodeID, write: store.Write}
	var saved savedTelemetrySettings
	err := store.ReadTelemetrySettings(&saved)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load telemetry settings: %w", err)
	}
	if saved.PanelURL != id.PanelURL || saved.NodeID != id.NodeID || saved.Settings.Validate() != nil {
		return nil, fmt.Errorf("telemetry settings identity or values invalid")
	}
	settings.current = &saved.Settings
	return settings, nil
}
func (s *telemetrySettings) apply(next v1.TelemetrySettings) v1.TelemetrySettingsResult {
	result := v1.TelemetrySettingsResult{Revision: next.Revision, State: "failed"}
	if s.current != nil {
		result.AppliedRevision = s.current.Revision
	}
	switch {
	case next.Validate() != nil:
		result.ErrorCode = "invalid_settings"
	case s.current != nil && next.Revision < s.current.Revision:
		result.ErrorCode = "stale_revision"
	case s.current != nil && next.Revision == s.current.Revision && next != *s.current:
		result.ErrorCode = "revision_conflict"
	default:
		if err := s.write("node-telemetry-settings.json", savedTelemetrySettings{s.panel, s.node, next}); err != nil {
			result.ErrorCode = "persist_failed"
			return result
		}
		s.current = &next
		result.State = "applied"
		result.AppliedRevision = next.Revision
	}
	return result
}

// telemetryCadence contains deadlines only, never data baselines. Its owner
// samples/sends once when due and advances from now, without catch-up bursts.
type telemetryCadence struct {
	settings               v1.TelemetrySettings
	realtime               bool
	fast, full, post, live time.Time
}

func newTelemetryCadence(settings v1.TelemetrySettings, now time.Time) (*telemetryCadence, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	return &telemetryCadence{settings: settings, fast: now, full: now, post: now.Add(time.Duration(settings.HttpReportSeconds) * time.Second)}, nil
}
func (c *telemetryCadence) change(next v1.TelemetrySettings, now time.Time) error {
	if err := next.Validate(); err != nil {
		return err
	}
	old := c.settings
	if next.Revision < old.Revision || (next.Revision == old.Revision && next != old) {
		return fmt.Errorf("stale or conflicting cadence revision")
	}
	if next.HttpReportSeconds != old.HttpReportSeconds {
		c.full = now.Add(time.Duration(next.HttpReportSeconds) * time.Second)
		if !c.realtime {
			c.post = c.full
		}
	}
	if !c.realtime && next.CollectionSeconds != old.CollectionSeconds {
		c.fast = now.Add(time.Duration(next.CollectionSeconds) * time.Second)
	}
	if c.realtime && next.WssReportSeconds != old.WssReportSeconds {
		c.fast = now.Add(time.Duration(next.WssReportSeconds) * time.Second)
		c.live = c.fast
	}
	c.settings = next
	return nil
}

// realtimeMode is called only after the transport transition/in-flight POST
// finishes. Full's independent R deadline is preserved across mode changes.
func (c *telemetryCadence) realtimeMode(enabled bool, now time.Time) {
	if c.realtime == enabled {
		return
	}
	c.realtime = enabled
	if enabled {
		c.fast = now
		c.live = now
		c.post = time.Time{}
	} else {
		c.live = time.Time{}
		c.fast = now.Add(time.Duration(c.settings.CollectionSeconds) * time.Second)
		c.post = now.Add(time.Duration(c.settings.HttpReportSeconds) * time.Second)
	}
}

type telemetryDue struct{ Fast, Full, POST, WSS bool }

func (c *telemetryCadence) due(now time.Time) telemetryDue {
	result := telemetryDue{}
	take := func(deadline *time.Time, seconds uint64) bool {
		if deadline.IsZero() || now.Before(*deadline) {
			return false
		}
		*deadline = now.Add(time.Duration(seconds) * time.Second)
		return true
	}
	seconds := c.settings.CollectionSeconds
	if c.realtime {
		seconds = c.settings.WssReportSeconds
	}
	result.Fast = take(&c.fast, seconds)
	result.Full = take(&c.full, c.settings.HttpReportSeconds)
	if c.realtime {
		result.WSS = take(&c.live, c.settings.WssReportSeconds)
	} else {
		result.POST = take(&c.post, c.settings.HttpReportSeconds)
	}
	return result
}
