package runtime

import (
	"context"
	"debug/elf"
	"errors"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The staged executable is already synced and private, on the same filesystem.
func publishEngineBinary(target, staged string) error {
	if i, err := os.Lstat(target); err == nil && !i.Mode().IsRegular() {
		return errors.New("unsafe managed binary")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		return err
	}
	return syncEngineDir(filepath.Dir(target))
}

func copyEngineBinary(ctx context.Context, target, source string) error {
	i, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !i.Mode().IsRegular() || i.Size() < 4 || i.Size() > maxBinary {
		return errors.New("unsafe managed binary")
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	return atomicEngineWrite(target, 0700, func(out *os.File) error {
		n, err := copyEngineStream(ctx, out, io.LimitReader(f, maxBinary+1))
		if err != nil {
			return err
		}
		if n != i.Size() {
			return errors.New("managed binary size changed")
		}
		return nil
	})
}

// Verify execution before publishing a download or changing live configuration.
// Mutating lifecycle operations repair approved dependencies; inspection stays read-only.
func (m *Managed) verifyBinary(ctx context.Context, binary string, c v1.EngineCommand, repair bool) string {
	if err := checkpoint(ctx, "verify_binary"); err != nil {
		return "engine_io_failed"
	}
	arg := "version"
	if c.Name == "realm" {
		arg = "--version"
	}
	err := m.run(ctx, binary, arg)
	if err == nil {
		return ""
	}
	reportFailure(ctx, err)
	if !errors.Is(err, os.ErrNotExist) {
		return "engine_binary_unusable"
	}
	loader, inspectErr := missingInterpreter(binary, m.hostRoot)
	if inspectErr != nil || loader == "" {
		return "engine_binary_unusable"
	}
	message := "引擎缺少动态加载器"
	known := loader == "/lib/ld-linux-aarch64.so.1" || loader == "/lib64/ld-linux-x86-64.so.2"
	if known {
		message += " " + loader
	}
	alpine, _ := os.ReadFile(filepath.Join(m.hostRoot, "etc/alpine-release"))
	supported := known && strings.HasPrefix(strings.TrimSpace(string(alpine)), "3.21.")
	if supported {
		message += "；Alpine/musl 需要 gcompat，可通过安装、应用配置或启动引擎自动修复"
	}
	reportReason(ctx, message, nil)
	if !repair || !supported {
		return "engine_dependency_missing"
	}
	if err := checkpoint(ctx, "install_dependencies"); err != nil {
		return "engine_io_failed"
	}
	// Use only the approved official repository and normal APK signature checks.
	// Never use the node's third-party repository list or task-supplied packages.
	repos, err := os.CreateTemp(filepath.Dir(binary), ".repositories-")
	if err != nil {
		reportFailure(ctx, err)
		return "engine_dependency_install_failed"
	}
	defer os.Remove(repos.Name())
	_, err = repos.WriteString("https://dl-cdn.alpinelinux.org/alpine/v3.21/main\n")
	closeErr := repos.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = m.run(ctx, "/sbin/apk", "--no-cache", "--repositories-file", repos.Name(), "add", "gcompat=1.1.0-r4")
	}
	if err != nil {
		reportReason(ctx, "安装 Alpine 官方 gcompat 失败："+safeFailure(err), nil)
		return "engine_dependency_install_failed"
	}
	if err := checkpoint(ctx, "verify_binary"); err != nil {
		return "engine_io_failed"
	}
	if err := m.run(ctx, binary, arg); err != nil {
		reportReason(ctx, "已安装 gcompat，但引擎仍无法执行："+safeFailure(err), nil)
		return "engine_binary_unusable"
	}
	if d, _ := ctx.Value(diagnosticKey{}).(*Diagnostics); d != nil {
		d.failure = nil
	}
	return ""
}

func missingInterpreter(binary, hostRoot string) (string, error) {
	f, err := elf.Open(binary)
	if err != nil {
		return "", err
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		if p.Filesz < 2 || p.Filesz > 4096 {
			return "", errors.New("invalid ELF interpreter")
		}
		b, err := io.ReadAll(io.LimitReader(p.Open(), 4097))
		if err != nil {
			return "", err
		}
		if len(b) != int(p.Filesz) || b[len(b)-1] != 0 {
			return "", errors.New("invalid ELF interpreter")
		}
		loader := string(b[:len(b)-1])
		if !filepath.IsAbs(loader) || filepath.Clean(loader) != loader || strings.ContainsRune(loader, 0) {
			return "", errors.New("invalid ELF interpreter")
		}
		_, err = os.Stat(filepath.Join(hostRoot, loader))
		if os.IsNotExist(err) {
			return loader, nil
		}
		return "", err
	}
	return "", nil
}
