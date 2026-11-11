package v1

import (
	"encoding/json"
	"errors"
	"strings"
)

const MaxEngineConfigurationBytes = 32 << 10

// ValidateEngineConfiguration validates native files without modifying their content.
func ValidateEngineConfiguration(name string, config *EngineConfiguration) error {
	if config == nil || config.Revision == 0 || config.Revision > 1<<53-1 || len(config.Files) == 0 || len(config.Files) > 34 {
		return errors.New("invalid engine configuration")
	}
	total := 0
	seen := map[string]bool{}
	for _, file := range config.Files {
		if file == nil || len(file.Name) == 0 || len(file.Name) > 96 || seen[file.Name] {
			return errors.New("invalid configuration file")
		}
		seen[file.Name] = true
		for _, ch := range file.Name {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.') {
				return errors.New("invalid configuration filename")
			}
		}
		total += len(file.Content)
		if len(file.Content) == 0 || total > MaxEngineConfigurationBytes {
			return errors.New("configuration too large")
		}
		if name == "realm" {
			if len(config.Files) != 1 || file.Name != "config.toml" {
				return errors.New("invalid realm configuration")
			}
			// Realm's managed format has only network endpoints and fixed log/network
			// options. Reject anything outside that format at the trust boundary.
			for _, line := range strings.Split(file.Content, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || line == "[log]" || line == "[network]" || line == "[[endpoints]]" || line == `level = "off"` || line == "no_tcp = false" || line == "use_udp = false" {
					continue
				}
				if !strings.HasPrefix(line, `listen = "`) && !strings.HasPrefix(line, `remote = "`) {
					return errors.New("invalid realm configuration field")
				}
			}
			continue
		}
		if name != "sing-box" && name != "xray" || !strings.HasSuffix(file.Name, ".json") {
			return errors.New("invalid JSON configuration")
		}
		if !json.Valid([]byte(file.Content)) {
			return errors.New("invalid JSON configuration content")
		}
	}

	encoded, err := json.Marshal(config)
	if err != nil {
		return err
	}
	if len(encoded) > 48<<10 {
		return errors.New("encoded configuration too large")
	}
	return nil
}
