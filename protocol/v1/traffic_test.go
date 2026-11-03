package v1

import (
	"testing"
	"time"
)

func TestTrafficPeriodsAndValidation(t *testing.T) {
	s := DefaultTrafficSettings()
	s.ResetDay = 31
	s.ResetHour = 10
	s.Timezone = "Asia/Shanghai"
	at := time.Date(2028, 2, 29, 3, 0, 0, 0, time.UTC)
	start, end := s.Period(at)
	if !start.Equal(time.Date(2028, 2, 29, 2, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2028, 3, 31, 2, 0, 0, 0, time.UTC)) {
		t.Fatal(start, end)
	}
	start, _ = s.Period(at.Add(-2 * time.Hour))
	if start.Day() != 31 || start.Month() != 1 {
		t.Fatal(start)
	}
	for _, bad := range []string{"eth0,eth0", "eth0;rm", "eth0,*", ",eth0"} {
		v := s
		v.Interfaces = bad
		if v.Validate() == nil {
			t.Fatal(bad)
		}
	}
	for _, mode := range []string{"both", "outbound", "inbound"} {
		v := s
		v.BillingMode = mode
		if v.Validate() != nil {
			t.Fatal(mode)
		}
	}
	s.QuotaBytes = "9223372036854775808"
	if s.Validate() == nil {
		t.Fatal("overflow accepted")
	}
}
