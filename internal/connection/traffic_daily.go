package connection

import v1 "github.com/vioira/viorka-agent/protocol/v1"

func validDailyAcks(acks []*v1.TelemetryDailyTrafficAck, records []*v1.TelemetryDailyTraffic) bool {
	if len(acks) != len(records) {
		return false
	}
	seen := map[int64]bool{}
	for _, a := range acks {
		if a == nil || seen[a.DayStart.UnixMilli()] {
			return false
		}
		seen[a.DayStart.UnixMilli()] = true
		found := false
		for _, d := range records {
			if d.DayStart.Equal(a.DayStart) && d.Revision == a.Revision {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
