package runtime

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Read the exact active, owned files and compare them with the last successful
// command before returning any content. No draft rendering is exposed as a read.
func (m *Managed) readEngineConfiguration(dir string, command v1.EngineCommand) (*v1.EngineConfiguration, error) {
	var expected map[string][]byte
	base := dir
	if command.Name == "sing-box" && activeConfig(dir, command.Name) == filepath.Join(dir, "conf.d") {
		generation, err := singboxGeneration(dir)
		if err != nil {
			return nil, err
		}
		base = filepath.Join(dir, generation)
		expected, err = renderSingboxFiles(command)
		if err != nil {
			return nil, err
		}
	} else {
		data, err := Render(command)
		if err != nil {
			return nil, err
		}
		expected = map[string][]byte{configName(command.Name): data}
	}
	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	sort.Strings(names)
	result := &v1.EngineConfiguration{Revision: command.Revision, Files: []*v1.EngineConfigFile{}}
	total := 0
	for _, name := range names {
		path := filepath.Join(base, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > v1.MaxEngineConfigurationBytes {
			return nil, os.ErrPermission
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, v1.MaxEngineConfigurationBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		total += len(data)
		if total > v1.MaxEngineConfigurationBytes || !bytes.Equal(data, expected[name]) {
			return nil, os.ErrInvalid
		}
		result.Files = append(result.Files, &v1.EngineConfigFile{Name: name, Content: string(data)})
	}
	if err := v1.ValidateEngineConfiguration(command.Name, result); err != nil {
		return nil, err
	}
	return result, nil
}
