package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadNativeConfigurationFiles(t *testing.T) {
	for _, name := range []string{"sing-box", "xray", "realm"} {
		t.Run(name, func(t *testing.T) {
			dir, pathErr := filepath.EvalSymlinks(t.TempDir())
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			c := renderCommand(t, name)
			m := NewManaged()
			base := dir
			content, err := Render(c)
			if err != nil {
				t.Fatal(err)
			}
			files := map[string][]byte{configName(name): content}
			if name == "sing-box" {
				base = filepath.Join(dir, ".conf-test")
				if err = os.Mkdir(base, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(".conf-test", filepath.Join(dir, "conf.d")); err != nil {
					t.Fatal(err)
				}
				files, err = renderSingboxFiles(c)
				if err != nil {
					t.Fatal(err)
				}
			}
			for filename, data := range files {
				if err = os.WriteFile(filepath.Join(base, filename), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := m.readEngineConfiguration(dir, c)
			if err != nil || result == nil || len(result.Files) != len(files) {
				t.Fatalf("read: %+v %v", result, err)
			}
			for _, file := range result.Files {
				if file.Content != string(files[file.Name]) {
					t.Fatal("native configuration changed")
				}
			}
			for filename := range files {
				if err = os.WriteFile(filepath.Join(base, filename), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
				break
			}
			if _, err = m.readEngineConfiguration(dir, c); err == nil {
				t.Fatal("changed active file accepted")
			}
		})
	}
}
