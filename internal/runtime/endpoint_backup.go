package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// StoreEndpointBackup writes a sibling of state.json so an engine operation
// cannot overwrite a concurrently delivered endpoint update (or vice versa).
func (m *Managed) StoreEndpointBackup(ctx context.Context, b v1.EngineEndpointBackup) error {
	switch b.Name {
	case "sing-box", "xray", "realm":
	default:
		return errors.New("invalid endpoint backup engine")
	}
	state, err := m.state(b.Name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !state.Installed || state.EngineID != b.EngineID || v1.EndpointCommandDigest(state.Command) != b.CommandSHA256 {
		return nil
	}
	if !b.Matches(state.EngineID, state.Command) {
		return errors.New("invalid published endpoints")
	}
	dir := filepath.Join(m.root, b.Name)
	if err := privateDirectory(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, "published-endpoints.json")
	var saved v1.EngineEndpointBackup
	err = readPrivate(path, &saved)
	if err == nil && reflect.DeepEqual(b, saved) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := atomicEngineFile(path, raw, 0600); err != nil {
		return err
	}
	m.logger.Info("Published endpoints saved", "engine", b.Name, "endpoints", len(b.Endpoints))
	return nil
}

func (m *Managed) loadEndpointBackup(snapshot *v1.EngineRecovery) error {
	var backup v1.EngineEndpointBackup
	err := readPrivate(filepath.Join(m.root, snapshot.Command.Name, "published-endpoints.json"), &backup)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if backup.EngineID == snapshot.EngineID && backup.CommandSHA256 == v1.EndpointCommandDigest(*snapshot.Command) {
		if !backup.Matches(snapshot.EngineID, *snapshot.Command) {
			return errors.New("invalid saved endpoint backup")
		}
		snapshot.EndpointBackup = &backup
	}
	return nil
}
