package v1

import (
	"math"
	"strconv"
	"time"
)

const DailyTrafficCapability = "node.traffic.daily.v1"
const (
	DailyTrafficPartial uint64 = 1 << iota
	DailyTrafficBoundaryEstimated
	DailyTrafficMixedFilter
	DailyTrafficHistoryGap
)

func (d TelemetryDailyTraffic) Validate() error {
	if d.DayStart.IsZero() || d.DayStart.Unix() < 0 || !d.DayEnd.After(d.DayStart) || d.DayEnd.Sub(d.DayStart) > 26*time.Hour || d.SampledAt.Before(d.DayStart) || d.SampledAt.After(d.DayEnd) || d.Revision == 0 || d.Revision > math.MaxInt64 || d.Quality > 15 {
		return ErrTelemetryPayload
	}
	for _, value := range []string{d.RXBytes, d.TXBytes} {
		n, err := strconv.ParseUint(value, 10, 63)
		if err != nil || strconv.FormatUint(n, 10) != value {
			return ErrTelemetryPayload
		}
	}
	return nil
}

func ValidateDailyTraffic(records []*TelemetryDailyTraffic, now time.Time) error {
	if len(records) > 16 {
		return ErrTelemetryPayload
	}
	seen := map[int64]bool{}
	for _, d := range records {
		if d == nil || d.Validate() != nil || d.SampledAt.After(now.Add(5*time.Minute)) || d.DayEnd.Before(now.AddDate(0, 0, -366)) || seen[d.DayStart.UnixMilli()] {
			return ErrTelemetryPayload
		}
		seen[d.DayStart.UnixMilli()] = true
	}
	return nil
}
