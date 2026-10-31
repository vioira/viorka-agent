package v1

import "errors"

func (s NodeSettings) Validate() error {
	if s.Revision == 0 || s.ConnectionMode != "wss" || s.CollectionSeconds < 10 || s.CollectionSeconds > 300 || s.ReportSeconds < 60 || s.ReportSeconds > 3600 || s.WssReportSeconds < s.CollectionSeconds || s.WssReportSeconds > 300 {
		return errors.New("invalid node settings")
	}
	return nil
}
