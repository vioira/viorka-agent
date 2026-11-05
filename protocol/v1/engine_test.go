package v1

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestEngineShortIDsValidation(t *testing.T) {
	for _, name := range []string{"sing-box", "xray"} {
		for _, tc := range []struct {
			name  string
			ids   []string
			valid bool
		}{
			{"two", []string{"0123456789abcdef", "ab12"}, true},
			{"one", []string{"aa"}, true},
			{"eight", []string{"00", "01", "02", "03", "04", "05", "06", "07"}, true},
			{"missing", nil, false}, {"empty", []string{}, false},
			{"blank", []string{""}, false}, {"odd", []string{"abc"}, false},
			{"nonhex", []string{"zz"}, false}, {"long", []string{"0123456789abcdef00"}, false},
			{"duplicate", []string{"ab", "AB"}, false},
			{"too many", []string{"00", "01", "02", "03", "04", "05", "06", "07", "08"}, false},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				c := EngineCommand{Name: name, Version: "test", Revision: 1, RunIntent: "stopped", Services: []*EngineService{{ID: "service", Name: "service", ListenHost: "127.0.0.1", ListenPort: 8443, TargetHost: "example.com", TargetPort: 443, ServerName: "example.com", Uuid: "12345678-1234-4234-8234-123456789012", PrivateKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ShortIds: tc.ids}}}
				if (c.Validate("engine.apply") == nil) != tc.valid {
					t.Fatal("unexpected short ID validation")
				}
			})
		}
	}
	realm := EngineCommand{Name: "realm", Version: "test", Revision: 1, RunIntent: "stopped", Services: []*EngineService{{ID: "service", Name: "service", ListenHost: "127.0.0.1", ListenPort: 8443, TargetHost: "example.com", TargetPort: 443, ShortIds: []string{}}}}
	if err := realm.Validate("engine.apply"); err != nil {
		t.Fatal(err)
	}
	realm.Services[0].ShortIds = []string{"ab"}
	if realm.Validate("engine.apply") == nil {
		t.Fatal("Realm accepted REALITY short IDs")
	}
	var legacy EngineService
	if err := Decode(json.RawMessage(`{"short_id":"0123456789abcdef"}`), &legacy); err == nil {
		t.Fatal("legacy scalar field accepted")
	}
}
