package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// NetworkFilter applies defaults, explicit inclusion, then explicit exclusion.
// Patterns are local configuration, never executable expressions or regexes.
type NetworkFilter struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

var defaultNetworkExclusions = []string{"lo", "docker*", "veth*", "br*", "tun*", "wg*", "tailscale*", "zerotier*", "tap*"}

func (f NetworkFilter) Validate() error {
	for _, patterns := range [][]string{f.Include, f.Exclude} {
		if len(patterns) > 64 {
			return fmt.Errorf("network filter permits at most 64 patterns per list")
		}
		seen := map[string]bool{}
		for _, pattern := range patterns {
			if len(pattern) == 0 || len(pattern) > 64 || seen[pattern] {
				return fmt.Errorf("invalid or duplicate network filter pattern")
			}
			seen[pattern] = true
			for i, c := range []byte(pattern) {
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("._:-", rune(c)) || (c == '*' && i == len(pattern)-1) {
					continue
				}
				return fmt.Errorf("network filter supports literal names and a trailing wildcard only")
			}
		}
	}
	return nil
}
func matchesInterface(patterns []string, name string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(p, "*") {
			if strings.HasPrefix(name, strings.TrimSuffix(p, "*")) {
				return true
			}
		} else if p == name {
			return true
		}
	}
	return false
}
func (f NetworkFilter) Includes(name string) bool {
	return (!matchesInterface(defaultNetworkExclusions, name) || matchesInterface(f.Include, name)) && !matchesInterface(f.Exclude, name)
}

// Revision includes the default rule version and canonical lists. Reordering
// equivalent configuration does not spuriously change the accounting scope.
func (f NetworkFilter) Revision() (string, error) {
	if err := f.Validate(); err != nil {
		return "", err
	}
	include := append([]string{}, f.Include...)
	exclude := append([]string{}, f.Exclude...)
	slices.Sort(include)
	slices.Sort(exclude)
	raw, err := json.Marshal(struct {
		Version                    int
		Defaults, Include, Exclude []string
	}{1, defaultNetworkExclusions, include, exclude})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
