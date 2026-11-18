package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const EngineEndpointsCapability = "engine.endpoints.v1"

// EndpointCommandDigest binds addresses to the deployed services and credentials.
// Lifecycle operations preserve their mapping across revisions and upgrades.
func EndpointCommandDigest(c EngineCommand) string {
	c.Revision, c.Version, c.RunIntent, c.AllowUnreachable = 0, "", "", false
	raw, _ := json.Marshal(c)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func (b EngineEndpointBackup) Matches(id string, c EngineCommand) bool {
	if id != b.EngineID || c.Name != b.Name || b.CommandSHA256 != EndpointCommandDigest(c) || b.Endpoints == nil || len(b.Endpoints) > len(c.Services) {
		return false
	}
	services := make(map[string]bool, len(c.Services))
	for _, s := range c.Services {
		services[s.ID] = true
	}
	for _, ep := range b.Endpoints {
		if ep == nil || !services[ep.ServiceID] || !engineHost(ep.Host) || ep.Port < 1 || ep.Port > 65535 {
			return false
		}
		delete(services, ep.ServiceID)
	}
	return true
}
