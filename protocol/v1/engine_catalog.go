package v1

import (
	_ "embed"
	"encoding/json"
)

//go:embed engine-releases.json
var engineReleasesJSON string

// EngineRelease is a fixed upstream asset, never a task-supplied URL.
// Source: api/agent/v1/engine-releases.json; Linux/client acceptance is separate.
type EngineRelease struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

func EngineReleaseFor(name, version, arch string) (EngineRelease, bool) {
	var releases []EngineRelease
	if json.Unmarshal([]byte(engineReleasesJSON), &releases) != nil {
		return EngineRelease{}, false
	}
	for _, r := range releases {
		if r.Name == name && r.Version == version && r.Arch == arch {
			return r, true
		}
	}
	return EngineRelease{}, false
}
func EngineVersion(name string) string {
	var releases []EngineRelease
	if json.Unmarshal([]byte(engineReleasesJSON), &releases) != nil {
		return ""
	}
	for _, r := range releases {
		if r.Name == name {
			return r.Version
		}
	}
	return ""
}
