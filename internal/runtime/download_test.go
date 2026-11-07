package runtime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func stageBinaryFixture(dir string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, ".binary-")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err = f.Chmod(0700); err == nil {
		_, err = f.Write(data)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), f.Close()
}

func archiveFixture(t testing.TB, size int64, zipped bool) (*os.File, int64) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "archive-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	var member io.Writer
	var closeArchive func() error
	if zipped {
		z := zip.NewWriter(f)
		member, err = z.Create("release/sing-box")
		closeArchive = z.Close
	} else {
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		err = tw.WriteHeader(&tar.Header{Name: "release/sing-box", Mode: 0700, Size: size, Typeflag: tar.TypeReg})
		member = tw
		closeArchive = func() error { return errors.Join(tw.Close(), gz.Close()) }
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = member.Write([]byte{0x7f, 'E', 'L', 'F'}); err != nil {
		t.Fatal(err)
	}
	block := make([]byte, 32<<10)
	for left := size - 4; left > 0; {
		n := min(left, int64(len(block)))
		if _, err := member.Write(block[:n]); err != nil {
			t.Fatal(err)
		}
		left -= n
	}
	if err := closeArchive(); err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return f, info.Size()
}

func TestStreamingDownload(t *testing.T) {
	for _, zipped := range []bool{false, true} {
		t.Run(fmt.Sprint(zipped), func(t *testing.T) {
			archive, size := archiveFixture(t, 4<<20, zipped)
			hash := sha256.New()
			if _, err := io.Copy(hash, io.NewSectionReader(archive, 0, size)); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(w, io.NewSectionReader(archive, 0, size))
			}))
			defer server.Close()
			suffix := ".tar.gz"
			if zipped {
				suffix = ".zip"
			}
			release := v1.EngineRelease{Name: "sing-box", URL: server.URL + "/release" + suffix, SHA256: hex.EncodeToString(hash.Sum(nil))}
			dir := t.TempDir()
			staged, err := Download(context.Background(), release, dir)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(staged)
			if err != nil || info.Size() != 4<<20 || info.Mode().Perm() != 0700 {
				t.Fatalf("binary: %v %v", info, err)
			}
			target := filepath.Join(dir, "sing-box")
			if err := publishEngineBinary(target, staged); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 || entries[0].Name() != "sing-box" {
				t.Fatal("staging leaked", entries)
			}
			release.SHA256 = strings.Repeat("0", 64)
			if _, err := Download(context.Background(), release, dir); err == nil || err.Error() != "engine_checksum" {
				t.Fatal(err)
			}
			entries, _ = os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatal("failed download leaked staging", entries)
			}
			info, err = os.Stat(target)
			if err != nil || info.Size() != 4<<20 {
				t.Fatal("existing binary changed", err)
			}
		})
	}
}

func TestStreamingDownloadRejectsTruncatedArchive(t *testing.T) {
	archive, size := archiveFixture(t, 1<<20, false)
	size -= 8 // Remove gzip checksum/trailer; checksum matches the received file.
	hash := sha256.New()
	io.Copy(hash, io.NewSectionReader(archive, 0, size))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(w, io.NewSectionReader(archive, 0, size))
	}))
	defer server.Close()
	dir := t.TempDir()
	_, err := Download(context.Background(), v1.EngineRelease{Name: "sing-box", URL: server.URL, SHA256: hex.EncodeToString(hash.Sum(nil))}, dir)
	if err == nil {
		t.Fatal("truncated gzip accepted")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("staging leaked", entries)
	}
}

func TestStreamingDownloadInterruptedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.Write([]byte("incomplete"))
	}))
	defer server.Close()
	dir := t.TempDir()
	if _, err := Download(context.Background(), v1.EngineRelease{Name: "sing-box", URL: server.URL}, dir); err == nil {
		t.Fatal("incomplete download accepted")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("staging leaked", entries)
	}
}

func TestStreamingBinaryBackupAndRollback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	backup := filepath.Join(dir, "previous-binary")
	old := bytes.Repeat([]byte("old"), 20000)
	if err := os.WriteFile(target, old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyEngineBinary(context.Background(), backup, target); err != nil {
		t.Fatal(err)
	}
	next, err := stageBinaryFixture(dir, []byte("new binary"))
	if err != nil {
		t.Fatal(err)
	}
	if err := publishEngineBinary(target, next); err != nil {
		t.Fatal(err)
	}
	if err := copyEngineBinary(context.Background(), target, backup); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(actual, old) {
		t.Fatal("rollback failed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyEngineBinary(ctx, target, backup); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	actual, err = os.ReadFile(target)
	if err != nil || !bytes.Equal(actual, old) {
		t.Fatal("cancel overwrote binary", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatal("backup staging leaked", entries)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := copyEngineBinary(context.Background(), backup, link); err == nil {
		t.Fatal("symlink backup accepted")
	}
}

func TestExtractStreamingCancellationAndWriteFailure(t *testing.T) {
	archive, size := archiveFixture(t, 1<<20, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := extractBinary(ctx, archive, size, "sing-box", false, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	failure := errors.New("disk full")
	if _, err := extractBinary(context.Background(), archive, size, "sing-box", false, failingBinaryWriter{failure}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

type failingBinaryWriter struct{ err error }

func (w failingBinaryWriter) Write([]byte) (int, error) { return 0, w.err }

// Run with -bench BenchmarkExtractBinaryStream -benchmem to verify allocation
// stays bounded as the executable grows, without building it in test memory.
func BenchmarkExtractBinaryStream(b *testing.B) {
	for _, size := range []int64{4 << 20, 96 << 20} {
		b.Run(fmt.Sprintf("%dMiB", size>>20), func(b *testing.B) {
			archive, archiveSize := archiveFixture(b, size, false)
			out, err := os.CreateTemp(b.TempDir(), "binary-")
			if err != nil {
				b.Fatal(err)
			}
			defer out.Close()
			b.ReportAllocs()
			b.SetBytes(size)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := out.Truncate(0); err != nil {
					b.Fatal(err)
				}
				if _, err := out.Seek(0, io.SeekStart); err != nil {
					b.Fatal(err)
				}
				if _, err := extractBinary(context.Background(), archive, archiveSize, "sing-box", false, out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// A real ELF program header exercises interpreter parsing on any development OS.
func interpreterFixture(t *testing.T, dir, loader string) string {
	t.Helper()
	b := make([]byte, 120+len(loader)+1)
	copy(b, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	binary.LittleEndian.PutUint16(b[18:], 183)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint64(b[32:], 64)
	binary.LittleEndian.PutUint16(b[52:], 64)
	binary.LittleEndian.PutUint16(b[54:], 56)
	binary.LittleEndian.PutUint16(b[56:], 1)
	binary.LittleEndian.PutUint32(b[64:], 3)
	binary.LittleEndian.PutUint64(b[72:], 120)
	binary.LittleEndian.PutUint64(b[96:], uint64(len(loader)+1))
	copy(b[120:], loader)
	p, err := stageBinaryFixture(dir, b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInterpreterDependencyRepair(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		allow, installFails, stillBroken, canceled bool
		want                                       string
	}{
		{name: "read only", want: "engine_dependency_missing"},
		{name: "automatic repair", allow: true},
		{name: "install failed", allow: true, installFails: true, want: "engine_dependency_install_failed"},
		{name: "still broken", allow: true, stillBroken: true, want: "engine_binary_unusable"},
		{name: "canceled", allow: true, canceled: true, want: "engine_binary_unusable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "etc"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "etc/alpine-release"), []byte("3.21.9\n"), 0600); err != nil {
				t.Fatal(err)
			}
			loader := "/lib/ld-linux-aarch64.so.1"
			path := interpreterFixture(t, root, loader)
			m := NewManaged()
			m.hostRoot = root
			installed := false
			m.run = func(ctx context.Context, executable string, args ...string) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if executable == "/sbin/apk" {
					if !tc.allow {
						t.Fatal("read-only check installed packages")
					}
					data, err := os.ReadFile(args[2])
					if err != nil {
						t.Fatal(err)
					}
					if string(data) != "https://dl-cdn.alpinelinux.org/alpine/v3.21/main\n" || args[4] != "gcompat=1.1.0-r4" {
						t.Fatalf("untrusted package source: %v %s", args, data)
					}
					if tc.installFails {
						return os.ErrPermission
					}
					installed = true
					return nil
				}
				if installed && !tc.stillBroken {
					return nil
				}
				return &os.PathError{Op: "fork/exec", Path: executable, Err: os.ErrNotExist}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			ctx, diag := TrackDiagnostics(ctx, func(string) error { return nil })
			code := m.verifyBinary(ctx, path, v1.EngineCommand{Name: "sing-box"}, tc.allow)
			if code != tc.want {
				t.Fatalf("code=%q diagnostic=%+v", code, diag.failure)
			}
			if code == "" && (!installed || diag.failure != nil) {
				t.Fatalf("repair failed: %+v", diag.failure)
			}
			if !tc.allow && !strings.Contains(diag.Result(code).Message, loader) {
				t.Fatal("missing loader not reported")
			}
			files, _ := filepath.Glob(filepath.Join(root, ".repositories-*"))
			if len(files) != 0 {
				t.Fatal("repository file leaked")
			}
			if err := os.MkdirAll(filepath.Join(root, "lib"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, loader), []byte("loader"), 0700); err != nil {
				t.Fatal(err)
			}
			if missing, err := missingInterpreter(path, root); err != nil || missing != "" {
				t.Fatalf("existing loader: %s %v", missing, err)
			}
		})
	}
}

func TestInstallUnusableBinaryDoesNotPublish(t *testing.T) {
	m := NewManaged()
	var rootErr error
	m.root, rootErr = filepath.EvalSymlinks(t.TempDir())
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	m.root = filepath.Join(m.root, "engines")
	m.units = t.TempDir()
	m.supported = func() bool { return true }
	m.detectExternal = func(string) bool { return false }
	m.download = func(_ context.Context, _ v1.EngineRelease, dir string) (string, error) {
		return stageBinaryFixture(dir, []byte("unusable"))
	}
	m.run = func(context.Context, string, ...string) error { return os.ErrPermission }
	c := v1.EngineCommand{Name: "sing-box", Version: v1.EngineVersion("sing-box"), Revision: 1, RunIntent: "stopped"}
	r, err := m.Execute(context.Background(), "engine", "engine.install", c)
	if err != nil || r.ErrorCode != "engine_binary_unusable" {
		t.Fatalf("%+v %v", r, err)
	}
	for _, name := range []string{"sing-box", "state.json"} {
		if _, err := os.Stat(filepath.Join(m.root, c.Name, name)); !os.IsNotExist(err) {
			t.Fatalf("published %s: %v", name, err)
		}
	}
}
