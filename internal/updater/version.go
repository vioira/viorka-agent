package updater

import (
	"fmt"
	"regexp"
	"strings"
)

var semver = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

type version struct {
	core []string
	pre  []string
}

func parseVersion(s string) (version, error) {
	m := semver.FindStringSubmatch(strings.TrimPrefix(s, "v"))
	if m == nil {
		return version{}, fmt.Errorf("invalid SemVer: %q", s)
	}
	v := version{core: m[1:4]}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
	}
	for _, p := range v.pre {
		if numeric(p) && len(p) > 1 && p[0] == '0' {
			return version{}, fmt.Errorf("invalid numeric prerelease: %q", s)
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
func (v version) compare(w version) int {
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
