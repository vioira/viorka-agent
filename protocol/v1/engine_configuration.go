package v1

import (
	"encoding/json"
	"errors"
	"strings"
)

const MaxEngineConfigurationBytes = 32 << 10

// RedactEngineConfiguration validates the node response and removes credential
// values before it can be persisted or returned through an API.
func RedactEngineConfiguration(name string, config *EngineConfiguration) error {
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
		var value any
		if err := json.Unmarshal([]byte(file.Content), &value); err != nil {
			return err
		}
		redactConfigValue(value)
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		file.Content = string(data)
	}
	total = 0
	for _, file := range config.Files {
		total += len(file.Content)
	}
	if total > MaxEngineConfigurationBytes {
		return errors.New("configuration too large")
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

func redactConfigValue(value any) {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			switch strings.ToLower(strings.ReplaceAll(key, "_", "")) {
			case "uuid", "id", "password", "username", "privatekey", "publickey", "shortid", "shortids", "token", "secret":
				value[key] = "[REDACTED]"
			default:
				redactConfigValue(item)
			}
		}
	case []any:
		for _, item := range value {
			redactConfigValue(item)
		}
	}
}
