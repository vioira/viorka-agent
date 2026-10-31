package v1

import "testing"

func TestNodeSettingsBounds(t *testing.T) {
	good := NodeSettings{Revision: 1, CollectionSeconds: 10, ReportSeconds: 60, ConnectionMode: "wss", WssReportSeconds: 300}
	if good.Validate() != nil {
		t.Fatal("valid settings rejected")
	}
	for _, mutate := range []func(*NodeSettings){func(s *NodeSettings) { s.Revision = 0 }, func(s *NodeSettings) { s.CollectionSeconds = 9 }, func(s *NodeSettings) { s.CollectionSeconds = 301 }, func(s *NodeSettings) { s.WssReportSeconds = 9 }, func(s *NodeSettings) { s.WssReportSeconds = 301 }, func(s *NodeSettings) { s.ReportSeconds = 59 }, func(s *NodeSettings) { s.ReportSeconds = 3601 }, func(s *NodeSettings) { s.ConnectionMode = "http_post" }, func(s *NodeSettings) { s.CollectionSeconds = 30; s.WssReportSeconds = 20 }} {
		s := good
		mutate(&s)
		if s.Validate() == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}
