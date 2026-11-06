package v1

import "testing"

func TestPlainVLESSOutbound(t *testing.T) {
	base := EngineOutbound{ID: "plain", Name: "Plain", Type: "vless", Server: "example.com", ServerPort: 443, Uuid: "12345678-1234-4234-8234-123456789012"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*EngineOutbound){
		func(o *EngineOutbound) { o.Flow = "xtls-rprx-vision" },
		func(o *EngineOutbound) { o.Fingerprint = "chrome" },
		func(o *EngineOutbound) { o.ServerName = "example.com" },
		func(o *EngineOutbound) { o.PublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" },
		func(o *EngineOutbound) { o.ShortID = "aabb" },
		func(o *EngineOutbound) { o.Tls = true },
		func(o *EngineOutbound) { o.Uuid = "invalid" },
		func(o *EngineOutbound) { o.Password = "secret" },
	} {
		o := base
		change(&o)
		if o.Validate() == nil {
			t.Fatal("accepted incompatible plain TCP fields")
		}
	}
}
