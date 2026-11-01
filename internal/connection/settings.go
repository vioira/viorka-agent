package connection

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Only validated Panel settings are stored here, never bootstrap or credentials.
type savedSettings struct {
	PanelURL string          `json:"panel_url"`
	NodeID   string          `json:"node_id"`
	Settings v1.NodeSettings `json:"settings"`
}

func (c *Client) loadSettings(id state.Identity) error {
	var saved savedSettings
	err := c.store.Read("node-settings.json", &saved)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load node settings: %w", err)
	}
	if saved.PanelURL != id.PanelURL || saved.NodeID != id.NodeID || saved.Settings.Validate() != nil {
		return fmt.Errorf("saved node settings invalid or identity mismatch")
	}
	c.settings = &saved.Settings
	return nil
}
func settingsInterval(s v1.NodeSettings) time.Duration {
	seconds := s.CollectionSeconds
	if seconds == 0 {
		seconds = s.WssReportSeconds
	}
	return time.Duration(seconds) * time.Second
}
func (c *Client) applySettings(id state.Identity, next v1.NodeSettings) v1.NodeSettingsResult {
	result := v1.NodeSettingsResult{Revision: next.Revision, State: "failed"}
	if c.settings != nil {
		result.AppliedRevision = c.settings.Revision
	}
	switch {
	case next.Validate() != nil:
		result.ErrorCode = "invalid_settings"
	case c.settings != nil && next.Revision < c.settings.Revision:
		result.ErrorCode = "stale_revision"
	case c.settings != nil && next.Revision == c.settings.Revision && next != *c.settings:
		result.ErrorCode = "revision_conflict"
	default:
		// Re-write even a repeated revision: a prior directory sync may have failed.
		if err := c.store.Write("node-settings.json", savedSettings{id.PanelURL, id.NodeID, next}); err != nil {
			result.ErrorCode = "persist_failed"
			return result
		}
		c.settings = &next
		if c.samples != nil {
			c.samples.setInterval(settingsInterval(next))
		}
		result.State = "applied"
		result.AppliedRevision = next.Revision
	}
	return result
}
