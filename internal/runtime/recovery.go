package runtime

import (
	"context"
	"fmt"
	"os"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// RecoverySnapshots reads only Viorka-managed installations. The existing
// inspector verifies unit ownership, binary and effective config without
// restarting or rewriting the engine. Enrollment keeps the original EngineID.
func (m *Managed) RecoverySnapshots(ctx context.Context) ([]*v1.EngineRecovery, error) {
	items := []*v1.EngineRecovery{}
	for _, name := range []string{"sing-box", "xray", "realm"} {
		state, err := m.state(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s recovery state: %w", name, err)
		}
		if !state.Installed {
			continue
		}
		if state.Command.Name != name {
			return nil, fmt.Errorf("invalid %s recovery identity", name)
		}
		unit, err := os.ReadFile(m.unitPath(name))
		if err != nil || !m.ownsUnit(unit) {
			return nil, fmt.Errorf("cannot recover %s: engine_external", name)
		}
		snapshot := &v1.EngineRecovery{EngineID: state.EngineID, Command: &state.Command, Status: "stopped"}
		if err = v1.ValidateEngineRecovery([]*v1.EngineRecovery{snapshot}); err != nil {
			return nil, fmt.Errorf("invalid %s recovery state", name)
		}
		inspect := v1.EngineCommand{Name: name, Version: state.Command.Version, Revision: state.Command.Revision + 1, RunIntent: state.Command.RunIntent, Services: []*v1.EngineService{}}
		result, err := m.Execute(ctx, state.EngineID, "engine.inspect", inspect)
		if err != nil {
			return nil, err
		}
		if result.ErrorCode != "" {
			return nil, fmt.Errorf("cannot recover %s: %s", name, result.ErrorCode)
		}
		snapshot.Status = result.Status
		items = append(items, snapshot)
	}
	return items, nil
}
