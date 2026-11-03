package v1

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const TrafficCapability = "node.traffic.v1"

var interfaceName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

func DefaultTrafficSettings() TelemetryTrafficSettings {
	return TelemetryTrafficSettings{ResetDay: 1, Timezone: "UTC", BillingMode: "both", QuotaBytes: "0"}
}
func (s TelemetrySettings) TrafficSettings() TelemetryTrafficSettings {
	if s.Traffic == nil {
		return DefaultTrafficSettings()
	}
	return *s.Traffic
}
func TrafficBytes(s string) (uint64, error) {
	v, e := strconv.ParseUint(s, 10, 63)
	if e != nil || strconv.FormatUint(v, 10) != s {
		return 0, errors.New("invalid traffic bytes")
	}
	return v, nil
}
func (s TelemetryTrafficSettings) Validate() error {
	if s.ResetDay < 1 || s.ResetDay > 31 || s.ResetHour > 23 || s.ResetMinute > 59 || len(s.Timezone) > 64 {
		return errors.New("invalid traffic cycle")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil || s.Timezone == "" || s.Timezone == "Local" {
		return errors.New("invalid traffic timezone")
	}
	if s.BillingMode != "both" && s.BillingMode != "outbound" && s.BillingMode != "inbound" {
		return errors.New("invalid billing mode")
	}
	if _, err := TrafficBytes(s.QuotaBytes); err != nil {
		return err
	}
	if s.Interfaces != "" {
		names := strings.Split(s.Interfaces, ",")
		if len(names) > 64 {
			return errors.New("too many interfaces")
		}
		seen := map[string]bool{}
		for _, name := range names {
			if !interfaceName.MatchString(name) || seen[name] {
				return errors.New("invalid or duplicate interface")
			}
			seen[name] = true
		}
	}
	return nil
}

// Period computes calendar boundaries in the configured zone, clamping short months.
func (s TelemetryTrafficSettings) Period(at time.Time) (time.Time, time.Time) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := at.In(loc)
	boundary := func(y int, m time.Month) time.Time {
		day := min(int(s.ResetDay), time.Date(y, m+1, 0, 0, 0, 0, 0, loc).Day())
		return time.Date(y, m, day, int(s.ResetHour), int(s.ResetMinute), 0, 0, loc).UTC()
	}
	y, m, _ := local.Date()
	start := boundary(y, m)
	if at.Before(start) {
		return boundary(y, m-1), start
	}
	return start, boundary(y, m+1)
}
func (c TrafficCalibration) Validate() error {
	if c.PeriodStart.IsZero() || c.PeriodStart.Unix() < 0 {
		return errors.New("invalid traffic period")
	}
	_, e := TrafficBytes(c.RXBytes)
	if e != nil {
		return e
	}
	_, e = TrafficBytes(c.TXBytes)
	if e != nil {
		return e
	}
	return nil
}
