package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Each service ID is validated by the protocol before becoming a filename.
func renderSingboxFiles(c v1.EngineCommand) (map[string][]byte, error) {
	data, err := Render(c)
	if err != nil {
		return nil, err
	}
	var full struct {
		Inbounds []json.RawMessage `json:"inbounds"`
	}
	if err = json.Unmarshal(data, &full); err != nil {
		return nil, err
	}
	docs := map[string]any{
		"00-base.json": map[string]any{"log": map[string]any{"level": "warn", "timestamp": true}, "outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}}},
	}
	tags := []string{}
	index := 0
	for _, service := range c.Services {
		if !service.Enabled {
			continue
		}
		docs["01-"+service.ID+".json"] = map[string]any{"inbounds": []json.RawMessage{full.Inbounds[index]}}
		tags = append(tags, service.ID)
		index++
	}
	rules := []any{}
	if len(tags) > 0 {
		rules = append(rules, map[string]any{"inbound": tags, "action": "sniff", "timeout": "300ms"})
	}
	docs["00-egress.json"] = map[string]any{
		"outbounds": []any{map[string]any{"type": "direct", "tag": "sb-egress-direct", "domain_resolver": "sb-egress-dns"}},
		"dns":       map[string]any{"servers": []any{map[string]any{"type": "local", "tag": "sb-egress-dns"}}},
		"route":     map[string]any{"rules": rules, "final": "sb-egress-direct"},
	}
	files := make(map[string][]byte, len(docs))
	for name, doc := range docs {
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, err
		}
		files[name] = append(b, '\n')
	}
	return files, nil
}

func activeConfig(dir, name string) string {
	if name == "sing-box" {
		if _, err := os.Lstat(filepath.Join(dir, "conf.d")); !os.IsNotExist(err) {
			return filepath.Join(dir, "conf.d")
		}
	}
	return filepath.Join(dir, configName(name))
}

// Only links to our own private, sibling generations are allowed. Never follow
// an arbitrary symlink or overwrite a pre-existing operator-owned directory.
func singboxGeneration(dir string) (string, error) {
	target, err := os.Readlink(filepath.Join(dir, "conf.d"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if filepath.Base(target) != target || !strings.HasPrefix(target, ".conf-") {
		return "", errors.New("unsafe configuration link")
	}
	if _, err := os.Lstat(filepath.Join(dir, target)); err != nil {
		return "", err
	}
	if err := privateDirectory(filepath.Join(dir, target)); err != nil {
		return "", err
	}
	return target, nil
}

func syncEngineDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func switchSingbox(dir, generation string) error {
	if generation == "" {
		if err := os.Remove(filepath.Join(dir, "conf.d")); err != nil && !os.IsNotExist(err) {
			return err
		}
		return syncEngineDir(dir)
	}
	// A unique private temporary directory avoids reusing a predictable link path.
	tmp, err := os.MkdirTemp(dir, ".link-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	link := filepath.Join(tmp, "conf.d")
	if err = os.Symlink(generation, link); err != nil {
		return err
	}
	if err = os.Rename(link, filepath.Join(dir, "conf.d")); err != nil {
		return err
	}
	return syncEngineDir(dir)
}

func inspectSingbox(dir string, c v1.EngineCommand) error {
	generation, err := singboxGeneration(dir)
	if err != nil {
		return err
	}
	if generation == "" {
		return errors.New("missing configuration")
	}
	expected, err := renderSingboxFiles(c)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, generation)
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != len(expected) {
		return errors.New("configuration file set changed")
	}
	for _, entry := range entries {
		want, ok := expected[entry.Name()]
		if !ok {
			return errors.New("unexpected configuration file")
		}
		p := filepath.Join(path, entry.Name())
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != int64(len(want)) {
			return errors.New("unsafe configuration file")
		}
		actual, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.Equal(want, actual) {
			return errors.New("configuration changed")
		}
	}
	return nil
}

func (m *Managed) prepareConfig(ctx context.Context, dir string, c v1.EngineCommand) (func(context.Context) error, func(), string) {
	if c.Name != "sing-box" {
		return m.prepareSingle(ctx, dir, c)
	}
	files, err := renderSingboxFiles(c)
	if err != nil {
		return nil, nil, "engine_invalid_config"
	}
	previous, err := singboxGeneration(dir)
	if err != nil {
		return nil, nil, "engine_io_failed"
	}
	unitPath := filepath.Join(m.units, "viorka-engine-sing-box.service")
	oldUnit, err := os.ReadFile(unitPath)
	if err != nil || !bytes.HasPrefix(oldUnit, []byte(unitMarker)) {
		return nil, nil, "engine_io_failed"
	}
	stage, err := os.MkdirTemp(dir, ".conf-")
	if err != nil {
		return nil, nil, "engine_io_failed"
	}
	next := filepath.Base(stage)
	// Delete only an inactive generation after success or a completed rollback.
	// A failed rollback retains both generations for investigation/recovery.
	cleanup := func() {
		active, e := singboxGeneration(dir)
		if e != nil {
			return
		}
		if active == next && previous != "" {
			_ = os.RemoveAll(filepath.Join(dir, previous))
		}
		if active == previous {
			_ = os.RemoveAll(stage)
		}
	}
	for name, data := range files {
		if err = atomicEngineFile(filepath.Join(stage, name), data, 0600); err != nil {
			return nil, cleanup, "engine_io_failed"
		}
	}
	if err = syncEngineDir(dir); err != nil {
		return nil, cleanup, "engine_io_failed"
	}
	if err = m.validate(ctx, dir, c, stage); err != nil {
		return nil, cleanup, "engine_invalid_config"
	}
	rollback := func(recovery context.Context) error {
		if err := switchSingbox(dir, previous); err != nil {
			return err
		}
		if err := atomicEngineFile(unitPath, oldUnit, 0644); err != nil {
			return err
		}
		return m.ctl(recovery, c.Name, "daemon-reload")
	}
	err = switchSingbox(dir, next)
	if err == nil {
		err = atomicEngineFile(unitPath, m.unit(c.Name, dir), 0644)
	}
	if err == nil {
		err = m.ctl(ctx, c.Name, "daemon-reload")
	}
	if err != nil {
		recovery, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if rollback(recovery) != nil {
			return nil, nil, "engine_rollback_failed"
		}
		return nil, cleanup, "engine_io_failed"
	}
	return rollback, cleanup, ""
}
