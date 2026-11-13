package state

import (
	"context"
	"fmt"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"math"
	"math/bits"
	"slices"
	"strconv"
	"time"
)

const maxDailyRecords = 366

func cloneDaily(records []*v1.TelemetryDailyTraffic) []*v1.TelemetryDailyTraffic {
	out := make([]*v1.TelemetryDailyTraffic, len(records))
	for i, d := range records {
		copy := *d
		out[i] = &copy
	}
	return out
}
func dailyInterval(at time.Time, zone string) (time.Time, time.Time) {
	loc, _ := time.LoadLocation(zone)
	local := at.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return start.UTC(), start.AddDate(0, 0, 1).UTC()
}
func trafficDelta(previous *TrafficSnapshot, boot, filter string, interfaces []TrafficInterface) (uint64, uint64, uint64) {
	if previous == nil {
		return 0, 0, v1.DailyTrafficPartial
	}
	if previous.FilterRevision != filter {
		return 0, 0, v1.DailyTrafficPartial | v1.DailyTrafficMixedFilter
	}
	if previous.BootID != boot {
		return 0, 0, v1.DailyTrafficPartial
	}
	old := map[string]TrafficInterface{}
	for _, i := range previous.Interfaces {
		old[i.Name] = i
	}
	var rx, tx, quality uint64
	for _, i := range interfaces {
		last, ok := old[i.Name]
		delete(old, i.Name)
		if !ok || last.Ifindex != i.Ifindex || i.RX < last.RX || i.TX < last.TX {
			quality |= v1.DailyTrafficPartial
			continue
		}
		rx += i.RX - last.RX
		tx += i.TX - last.TX
	}
	if len(old) > 0 {
		quality |= v1.DailyTrafficPartial
	}
	return rx, tx, quality
}
func apportioned(value uint64, part, total time.Duration) uint64 {
	hi, lo := bits.Mul64(value, uint64(part))
	q, _ := bits.Div64(hi, lo, uint64(total))
	return q
}

// observeDaily runs under the ledger sampling lock. Only byte deltas enter the
// daily ledger; monthly calibration and monthly resets never participate.
func (l *TrafficLedger) observeDaily(next *TrafficSnapshot, previous *TrafficSnapshot) error {
	at := next.SampledAt.Truncate(time.Millisecond)
	rx, tx, quality := trafficDelta(previous, next.BootID, next.FilterRevision, next.Interfaces)
	var records []*v1.TelemetryDailyTraffic
	if previous != nil {
		records = cloneDaily(previous.Daily)
	}
	from := at
	if previous != nil {
		from = previous.SampledAt.Truncate(time.Millisecond)
	}
	if len(records) == 0 || at.Sub(from) > 366*24*time.Hour {
		start, end := dailyInterval(at, l.settings.Timezone)
		if len(records) > 0 {
			quality |= v1.DailyTrafficHistoryGap
		}
		records = []*v1.TelemetryDailyTraffic{{DayStart: start, DayEnd: end, RXBytes: "0", TXBytes: "0", SampledAt: at, Revision: 1, Quality: quality | v1.DailyTrafficPartial}}
		next.Daily = records
		return nil
	}
	if !at.After(from) {
		next.Daily = records
		return nil
	}
	crossed := !at.Before(records[len(records)-1].DayEnd)
	if crossed {
		quality |= v1.DailyTrafficBoundaryEstimated
	}
	for {
		d := records[len(records)-1]
		if !from.Before(d.DayEnd) {
			_, end := dailyInterval(from, l.settings.Timezone)
			records = append(records, &v1.TelemetryDailyTraffic{DayStart: from, DayEnd: end, RXBytes: "0", TXBytes: "0", SampledAt: from})
			d = records[len(records)-1]
		}
		until := at
		if until.After(d.DayEnd) {
			until = d.DayEnd
		}
		partRX, partTX := rx, tx
		if until.Before(at) {
			partRX = apportioned(rx, until.Sub(from), at.Sub(from))
			partTX = apportioned(tx, until.Sub(from), at.Sub(from))
		}
		oldRX, _ := strconv.ParseUint(d.RXBytes, 10, 63)
		oldTX, _ := strconv.ParseUint(d.TXBytes, 10, 63)
		if partRX > math.MaxInt64-oldRX || partTX > math.MaxInt64-oldTX || d.Revision == math.MaxInt64 {
			return fmt.Errorf("daily traffic counter overflow")
		}
		d.RXBytes = strconv.FormatUint(oldRX+partRX, 10)
		d.TXBytes = strconv.FormatUint(oldTX+partTX, 10)
		d.SampledAt = until
		d.Revision++
		d.Quality |= quality
		rx -= partRX
		tx -= partTX
		from = until
		if !at.After(until) {
			if at.Equal(d.DayEnd) {
				_, end := dailyInterval(at, l.settings.Timezone)
				records = append(records, &v1.TelemetryDailyTraffic{DayStart: at, DayEnd: end, RXBytes: "0", TXBytes: "0", SampledAt: at, Revision: 1})
			}
			break
		}
	}
	if len(records) > maxDailyRecords {
		records = records[len(records)-maxDailyRecords:]
		records[len(records)-1].Quality |= v1.DailyTrafficHistoryGap
	}
	next.Daily = records
	return nil
}

// PendingDaily exposes only durable snapshots. A concurrent save can defer this
// report, but cannot publish a version whose restart baseline is not on disk.
func (l *TrafficLedger) PendingDaily(ctx context.Context) ([]*v1.TelemetryDailyTraffic, error) {
	if err := l.Save(ctx, time.Now(), true); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	records := l.durableDaily
	if len(records) <= 16 {
		return cloneDaily(records), nil
	}
	out := cloneDaily(records[:15])
	return append(out, cloneDaily(records[len(records)-1:])...), nil
}

// AcknowledgeDaily only removes ended records with the exact committed version.
// Removal is persisted by the next save. A crash before that simply replays it.
func (l *TrafficLedger) AcknowledgeDaily(acks []*v1.TelemetryDailyTrafficAck) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.snapshot == nil || len(l.snapshot.Daily) < 2 {
		return
	}
	next := cloneTraffic(*l.snapshot)
	next.Daily = slices.DeleteFunc(next.Daily, func(d *v1.TelemetryDailyTraffic) bool {
		if d.DayStart.Equal(l.snapshot.Daily[len(l.snapshot.Daily)-1].DayStart) {
			return false
		}
		for _, a := range acks {
			if a.DayStart.Equal(d.DayStart) && a.Revision == d.Revision {
				return true
			}
		}
		return false
	})
	l.snapshot = &next
	l.dirty = true
}
