package v1

import "testing"

func TestNodeSettingsBounds(t *testing.T) {
	for _, c := range []uint64{0, 1, 10} {
		for _, w := range []uint64{1, 2, 5} {
			for _, r := range []uint64{30, 60, 180} {
				s := NodeSettings{Revision: 1, CollectionSeconds: c, WssReportSeconds: w, ReportSeconds: r, ConnectionMode: "wss"}
				if s.Validate() != nil {
					t.Fatalf("rejected %+v", s)
				}
			}
		}
	}
	for _, mutate := range []func(*NodeSettings){func(s *NodeSettings) { s.Revision = 0 }, func(s *NodeSettings) { s.CollectionSeconds = 11 }, func(s *NodeSettings) { s.ReportSeconds = 29 }, func(s *NodeSettings) { s.ReportSeconds = 181 }, func(s *NodeSettings) { s.WssReportSeconds = 0 }, func(s *NodeSettings) { s.WssReportSeconds = 6 }, func(s *NodeSettings) { s.ConnectionMode = "http_post" }} {
		s := NodeSettings{Revision: 1, ReportSeconds: 60, WssReportSeconds: 2, ConnectionMode: "wss"}
		mutate(&s)
		if s.Validate() == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}
