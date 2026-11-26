package v1

import (
	"fmt"
	"regexp"
	"strings"
)

var semver = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// ReleaseVersion is a parsed SemVer with unbounded numeric components.
type ReleaseVersion struct {
	core []string
	pre  []string
}

func (v ReleaseVersion) IsPrerelease() bool { return len(v.pre) > 0 }

func ParseReleaseVersion(s string) (ReleaseVersion, error) {
	m := semver.FindStringSubmatch(strings.TrimPrefix(s, "v"))
	if m == nil {
		return ReleaseVersion{}, fmt.Errorf("invalid SemVer: %q", s)
	}
	v := ReleaseVersion{core: m[1:4]}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
	}
	for _, p := range v.pre {
		if numeric(p) && len(p) > 1 && p[0] == '0' {
			return ReleaseVersion{}, fmt.Errorf("invalid numeric prerelease: %q", s)
		}
	}
	return v, nil
}
func numeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func numberCompare(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
func (v ReleaseVersion) Compare(w ReleaseVersion) int {
	for i, a := range v.core {
		if c := numberCompare(a, w.core[i]); c != 0 {
			return c
		}
	}
	if len(v.pre) == 0 && len(w.pre) > 0 {
		return 1
	}
	if len(w.pre) == 0 && len(v.pre) > 0 {
		return -1
	}
	for i, a := range v.pre {
		if i >= len(w.pre) {
			return 1
		}
		b := w.pre[i]
		c := strings.Compare(a, b)
		if numeric(a) && numeric(b) {
			c = numberCompare(a, b)
		} else if numeric(a) {
			c = -1
		} else if numeric(b) {
			c = 1
		}
		if c != 0 {
			return c
		}
	}
	if len(v.pre) < len(w.pre) {
		return -1
	}
	return 0
}

const AgentUpdateCapability = "agent.update.v1"
const AgentUpdateOperation = "agent.update"

var updateDigest = regexp.MustCompile("^[0-9a-f]{64}$")

func (u AgentUpdate) Valid() bool {
	v, err := ParseReleaseVersion(u.Version)
	return err == nil && len(u.Version) <= 64 && u.Version == "v"+strings.Join(v.core, ".") && updateDigest.MatchString(u.SHA256)
}
func (u AgentUpdate) NewerThan(current string) bool {
	target, err := ParseReleaseVersion(u.Version)
	old, oldErr := ParseReleaseVersion(current)
	return u.Valid() && err == nil && oldErr == nil && target.Compare(old) > 0
}
