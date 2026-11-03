package state

import (
	"context"
	"errors"
	"fmt"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"strconv"
	"time"
)

var ErrTrafficPeriod = errors.New("traffic period changed or unavailable")

type trafficPeriod struct {
	start, end time.Time
	cycle      v1.TelemetryTrafficSettings
}

func validTrafficPeriod(s TrafficSnapshot) bool {
	if s.Cycle == nil {
		return s.MonthStart.Equal(trafficMonth(s.SampledAt))
	}
	if s.Cycle.Validate() != nil {
		return false
	}
	end, err := time.Parse(time.RFC3339, s.PeriodEnd)
	return err == nil && !s.MonthStart.After(s.SampledAt) && end.After(s.SampledAt) && end.After(s.MonthStart) && end.Sub(s.MonthStart) <= 63*24*time.Hour
}
func (l *TrafficLedger) period(at time.Time) trafficPeriod {
	cycle := l.settings
	if cycle.Timezone == "" {
		cycle = v1.DefaultTrafficSettings()
	}
	start, end := cycle.Period(at)
	if old := l.snapshot; old != nil {
		oldCycle := v1.DefaultTrafficSettings()
		if old.Cycle != nil {
			oldCycle = *old.Cycle
		}
		_, oldEnd := oldCycle.Period(old.SampledAt)
		if old.PeriodEnd != "" {
			if v, e := time.Parse(time.RFC3339, old.PeriodEnd); e == nil {
				oldEnd = v
			}
		}
		if at.Before(oldEnd) {
			return trafficPeriod{old.MonthStart, oldEnd, oldCycle}
		}
		// A schedule change never resets the active period. The first new period
		// starts at the prior boundary, even when the next configured day is later.
		if start.Before(oldEnd) {
			start = oldEnd
		}
	}
	return trafficPeriod{start, end, cycle}
}
func (l *TrafficLedger) Configure(settings v1.TelemetryTrafficSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.settings = settings
	return nil
}

// Calibrate persists the target and idempotency receipt together under the same
// locks as sampling and saving. A retry after rename/fsync uncertainty only
// retries durability; it never applies the target a second time.
func (l *TrafficLedger) Calibrate(ctx context.Context, key string, c v1.TrafficCalibration, at time.Time) error {
	_, err := l.CalibrateWithReceipt(ctx, key, c, at)
	return err
}
func (l *TrafficLedger) CalibrateWithReceipt(ctx context.Context, key string, c v1.TrafficCalibration, at time.Time) (*v1.TrafficCalibration, error) {
	if c.Validate() != nil || key == "" || len(key) > 128 {
		return nil, fmt.Errorf("invalid calibration")
	}
	l.saveMu.Lock()
	defer l.saveMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.blocked != nil {
		return nil, l.blocked
	}
	if l.snapshot == nil || !l.snapshot.MonthStart.Equal(c.PeriodStart) || !l.period(at).start.Equal(c.PeriodStart) {
		return nil, ErrTrafficPeriod
	}
	if old, ok := l.snapshot.Calibrations[key]; ok {
		if old != c {
			return nil, fmt.Errorf("calibration idempotency conflict")
		}
		before := l.snapshot.CalibrationBefore[key]
		return &before, l.saveSnapshot(ctx, at, true)
	}
	if len(l.snapshot.Calibrations) >= 64 {
		return nil, fmt.Errorf("calibration limit reached for period")
	}
	next := cloneTraffic(*l.snapshot)
	if next.Calibrations == nil {
		next.Calibrations = map[string]v1.TrafficCalibration{}
	}
	before := v1.TrafficCalibration{PeriodStart: c.PeriodStart, RXBytes: strconv.FormatUint(next.RX, 10), TXBytes: strconv.FormatUint(next.TX, 10)}
	if next.CalibrationBefore == nil {
		next.CalibrationBefore = map[string]v1.TrafficCalibration{}
	}
	next.CalibrationBefore[key] = before
	next.Calibrations[key] = c
	next.RX, _ = v1.TrafficBytes(c.RXBytes)
	next.TX, _ = v1.TrafficBytes(c.TXBytes)
	l.snapshot = &next
	l.dirty = true
	return &before, l.saveSnapshot(ctx, at, true)
}
