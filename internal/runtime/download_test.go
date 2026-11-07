package runtime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
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
