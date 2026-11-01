package v1

import "errors"

func (s NodeSettings) Validate() error {
	if s.Revision == 0 || s.ConnectionMode != "wss" || s.CollectionSeconds > 10 || s.ReportSeconds < 30 || s.ReportSeconds > 180 || s.WssReportSeconds < 1 || s.WssReportSeconds > 5 {
		return errors.New("invalid node settings")
	}
	return nil
}

func (r NodeSettingsResult) Validate() error {
	if r.Revision == 0 {
		return errors.New("invalid settings result revision")
	}
	if r.State == "applied" && r.AppliedRevision == r.Revision && r.ErrorCode == "" {
		return nil
	}
	if r.State == "failed" {
		switch r.ErrorCode {
		case "invalid_settings", "stale_revision", "revision_conflict", "persist_failed":
			return nil
		}
	}
	return errors.New("invalid settings result")
}
