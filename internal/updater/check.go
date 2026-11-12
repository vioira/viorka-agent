package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/vioira/viorka-agent/internal/network"
)

const releasesURL = "https://api.github.com/repos/vioira/viorka-agent/releases"
const maxResponse = 4 << 20

type Result struct {
	Current   string `json:"current"`
	Candidate string `json:"candidate,omitempty"`
	Platform  string `json:"platform"`
	Channel   string `json:"channel"`
	Status    string `json:"status"`
	Upgrade   string `json:"upgrade,omitempty"`
	Installer string `json:"installer,omitempty"`
}
type release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Check is read-only and shares one deadline across all pages. It never opens identity state.
func Check(ctx context.Context, current, goos, arch string, preview bool, dns []string) (Result, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = network.NewDNS(dns).DialContext
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return fmt.Errorf("release API redirects are not allowed")
	}}
	return check(ctx, client, current, goos, arch, preview)
}
func check(ctx context.Context, client *http.Client, current, goos, arch string, preview bool) (Result, error) {
	r := Result{Current: current, Platform: goos + "/" + arch, Channel: "stable", Status: "no-release"}
	if preview {
		r.Channel = "prerelease"
	}
	cv, err := parseVersion(current)
	if err != nil {
		return r, err
	}
	if goos != "linux" || (arch != "amd64" && arch != "arm64") {
		r.Status = "unsupported-platform"
		return r, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var best release
	var bv version
	for page := 1; ; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", releasesURL, page), nil)
		if err != nil {
			return r, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "viorka-agent-update-check")
		resp, err := client.Do(req)
		if err != nil {
			return r, fmt.Errorf("fetch releases: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return r, fmt.Errorf("release API HTTP %d", resp.StatusCode)
		}
		if readErr != nil {
			return r, fmt.Errorf("read releases: %w", readErr)
		}
		if len(data) > maxResponse {
			return r, fmt.Errorf("release response exceeds size limit")
		}
		var releases []release
		if err := json.Unmarshal(data, &releases); err != nil {
			return r, fmt.Errorf("decode releases: %w", err)
		}
		for _, rel := range releases {
			if rel.Draft || len(rel.Tag) == 0 || rel.Tag[0] != 'v' {
				continue
			}
			v, err := parseVersion(rel.Tag)
			if err != nil {
				continue
			}
			if !preview && (rel.Prerelease || len(v.pre) > 0) {
				continue
			}
			if best.Tag == "" || v.compare(bv) > 0 {
				best = rel
				bv = v
			}
		}
		if len(releases) < 100 {
			break
		}
		if page == 10 {
			return r, fmt.Errorf("release page limit exceeded")
		}
	}
	if best.Tag == "" {
		return r, nil
	}
	r.Candidate = best.Tag
	r.Status = "up-to-date"
	if cv.compare(bv) > 0 {
		r.Status = "ahead"
	}
	if cv.compare(bv) >= 0 {
		return r, nil
	}
	asset := "viorka-agent-" + goos + "-" + arch
	base := "https://github.com/vioira/viorka-agent/releases/download/" + best.Tag + "/"
	found := map[string]bool{}
	for _, a := range best.Assets {
		if a.URL == base+a.Name {
			found[a.Name] = true
		}
	}
	if !found[asset] {
		r.Status = "asset-missing"
		return r, nil
	}
	if !found[asset+".sha256"] {
		r.Status = "checksum-missing"
		return r, nil
	}
	r.Status = "update-available"
	r.Installer = "https://raw.githubusercontent.com/vioira/viorka-agent/main/install.sh"
	r.Upgrade = "sh install.sh install --version=" + best.Tag
	return r, nil
}
