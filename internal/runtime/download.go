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
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

const maxArchive = 128 << 20
const maxBinary = 256 << 20

// Download stages a verified executable in the private engine directory.
// The caller owns the returned path and must publish or remove it.
func Download(ctx context.Context, r v1.EngineRelease, dir string) (string, error) {
	return downloadLogged(ctx, r, dir, slog.New(slog.DiscardHandler))
}

func downloadLogged(ctx context.Context, r v1.EngineRelease, dir string, logger *slog.Logger) (binary string, resultErr error) {
	if err := checkpoint(ctx, "download"); err != nil {
		return "", err
	}
	started := time.Now()
	stage := "request"
	var binaryBytes int64
	logger.Debug("Engine download started", "storage", "disk")
	defer func() {
		if resultErr != nil {
			if d, _ := ctx.Value(diagnosticKey{}).(*Diagnostics); d != nil && d.failure == nil {
				reportFailure(ctx, resultErr)
			}
			logger.Error("Engine download failed", "stage", stage, "error", resultErr, "duration", time.Since(started))
		} else {
			logger.Debug("Engine download completed", "binary_bytes", binaryBytes, "duration", time.Since(started))
		}
	}()

	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" || (req.URL.Hostname() != "github.com" && req.URL.Hostname() != "release-assets.githubusercontent.com" && req.URL.Hostname() != "objects.githubusercontent.com") {
			return errors.New("untrusted redirect")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		// Signed redirect queries must not enter logs.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		reportFailure(ctx, err)
		logger.Error("Engine HTTP request failed", "error", err)
		return "", errors.New("engine_download_failed")
	}
	defer resp.Body.Close()
	logger.Debug("Engine download response", "http_status", resp.StatusCode, "content_length", resp.ContentLength)
	if resp.StatusCode != 200 || resp.ContentLength > maxArchive {
		status := uint64(resp.StatusCode)
		if resp.StatusCode != 200 {
			reportReason(ctx, "下载服务器返回错误状态", &status)
		} else {
			reportReason(ctx, "安装包超过允许的大小", nil)
		}
		logger.Error("Engine download response rejected", "http_status", resp.StatusCode, "content_length", resp.ContentLength)
		return "", errors.New("engine_download_failed")
	}
	// Keep large temporary files on the engine filesystem, not /tmp (often tmpfs).
	stage = "body"
	archive, err := os.CreateTemp(dir, ".archive-")
	if err != nil {
		return "", err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	hash := sha256.New()
	n, err := copyEngineStream(ctx, io.MultiWriter(archive, hash), io.LimitReader(resp.Body, maxArchive+1))
	if err != nil || n > maxArchive {
		if err != nil {
			reportFailure(ctx, err)
		} else {
			reportReason(ctx, "安装包超过允许的大小", nil)
		}
		logger.Error("Engine archive read failed", "error", err, "bytes", n, "limit", maxArchive)
		return "", errors.New("engine_download_failed")
	}
	stage = "checksum"
	if err := checkpoint(ctx, "checksum"); err != nil {
		return "", err
	}
	logger.Debug("Engine archive downloaded; verifying checksum", "bytes", n)
	if hex.EncodeToString(hash.Sum(nil)) != r.SHA256 {
		reportReason(ctx, "下载文件的 SHA-256 与发行清单不一致", nil)
		return "", errors.New("engine_checksum")
	}
	stage = "extract"
	if err := checkpoint(ctx, "extract"); err != nil {
		return "", err
	}
	logger.Debug("Engine archive checksum verified; extracting")
	staged, err := os.CreateTemp(dir, ".binary-")
	if err != nil {
		return "", err
	}
	defer staged.Close()
	defer func() {
		if resultErr != nil {
			os.Remove(staged.Name())
		}
	}()
	binaryBytes, err = extractBinary(ctx, archive, n, r.Name, strings.HasSuffix(r.URL, ".zip"), staged)
	if err != nil {
		message := safeFailure(err)
		if message == "操作失败，请查看节点运行日志" {
			message = "安装包解压失败，归档内容或二进制格式无效"
		}
		reportReason(ctx, message, nil)
		return "", err
	}
	if err = staged.Chmod(0700); err != nil {
		return "", err
	}
	if err = staged.Sync(); err != nil {
		return "", err
	}
	if err = staged.Close(); err != nil {
		return "", err
	}
	return staged.Name(), nil
}

// Hide optional ReaderFrom/WriterTo methods so every copy uses the bounded
// buffer and checks cancellation between reads, including disk-only work.
type engineReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r engineReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func copyEngineStream(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	return io.CopyBuffer(struct{ io.Writer }{dst}, engineReader{ctx, src}, make([]byte, 32<<10))
}

func extractBinary(ctx context.Context, archive io.ReaderAt, archiveSize int64, name string, zipped bool, dst io.Writer) (int64, error) {
	found := false
	var binaryBytes int64
	read := func(r io.Reader, size int64) error {
		if found || size < 4 || size > maxBinary {
			return errors.New("invalid archive member")
		}
		found = true
		var magic [4]byte
		if _, err := io.ReadFull(engineReader{ctx, r}, magic[:]); err != nil {
			return err
		}
		if !bytes.Equal(magic[:], []byte{0x7f, 'E', 'L', 'F'}) {
			return errors.New("invalid Linux binary")
		}
		if _, err := dst.Write(magic[:]); err != nil {
			return err
		}
		n, err := copyEngineStream(ctx, dst, io.LimitReader(r, size-4+1))
		if err != nil {
			return err
		}
		if n != size-4 {
			return errors.New("invalid binary size")
		}
		binaryBytes = n + 4
		return nil
	}
	if zipped {
		z, err := zip.NewReader(archive, archiveSize)
		if err != nil {
			return 0, err
		}
		if len(z.File) > 1024 {
			return 0, errors.New("too many archive members")
		}
		for _, f := range z.File {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if path.Base(f.Name) != name {
				continue
			}
			if !f.Mode().IsRegular() {
				return 0, errors.New("unsafe archive entry")
			}
			r, err := f.Open()
			if err != nil {
				return 0, err
			}
			err = read(r, int64(f.UncompressedSize64))
			r.Close()
			if err != nil {
				return 0, err
			}
		}
	} else {
		gz, err := gzip.NewReader(engineReader{ctx, io.NewSectionReader(archive, 0, archiveSize)})
		if err != nil {
			return 0, err
		}
		defer gz.Close()
		expanded := &io.LimitedReader{R: engineReader{ctx, gz}, N: 2*maxBinary + 1}
		tr := tar.NewReader(expanded)
		for count := 0; ; count++ {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return 0, err
			}
			if count >= 1024 {
				return 0, errors.New("too many archive members")
			}
			if path.Base(h.Name) == name {
				if h.Typeflag != tar.TypeReg {
					return 0, errors.New("unsafe archive entry")
				}
				if err = read(tr, h.Size); err != nil {
					return 0, err
				}
			}
		}
		// Consume the gzip trailer too, so truncation and checksum failures reject
		// the staging file even when the selected executable was already read.
		if _, err := copyEngineStream(ctx, io.Discard, expanded); err != nil {
			return 0, err
		}
		if expanded.N <= 0 {
			return 0, errors.New("archive expansion limit exceeded")
		}
	}
	if !found {
		return 0, errors.New("missing Linux binary")
	}
	return binaryBytes, nil
}
