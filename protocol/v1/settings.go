package v1

import "errors"

func (s NodeSettings) Validate() error {
	if s.Revision == 0 || s.ConnectionMode != "wss" || s.CollectionSeconds > 10 || s.ReportSeconds < 30 || s.ReportSeconds > 180 || s.WssReportSeconds < 1 || s.WssReportSeconds > 5 {
		return errors.New("invalid node settings")
	}
	return nil
}
