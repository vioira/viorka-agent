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
	"path"
	"strings"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

const maxArchive = 128 << 20
const maxBinary = 256 << 20

func Download(ctx context.Context, r v1.EngineRelease) ([]byte, error) {
	return downloadLogged(ctx, r, slog.New(slog.DiscardHandler))
}

func downloadLogged(ctx context.Context, r v1.EngineRelease, logger *slog.Logger) (binary []byte, resultErr error) {
	started := time.Now()
	stage := "request"
	logger.Debug("Engine download started")
	defer func() {
		if resultErr != nil {
			logger.Error("Engine download failed", "stage", stage, "error", resultErr, "duration", time.Since(started))
		} else {
			logger.Debug("Engine download completed", "binary_bytes", len(binary), "duration", time.Since(started))
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
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		// Strip URL-bearing wrappers: signed redirect queries must not enter logs.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		logger.Error("Engine HTTP request failed", "error", err)
		return nil, errors.New("engine_download_failed")
	}
	defer resp.Body.Close()
	logger.Debug("Engine download response", "http_status", resp.StatusCode, "content_length", resp.ContentLength)
	if resp.StatusCode != 200 {
		logger.Error("Engine download HTTP status rejected", "http_status", resp.StatusCode)
		return nil, errors.New("engine_download_failed")
	}
	stage = "body"
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArchive+1))
	if err != nil || len(data) > maxArchive {
		logger.Error("Engine archive read failed", "error", err, "bytes", len(data), "limit", maxArchive)
		return nil, errors.New("engine_download_failed")
	}
	stage = "checksum"
	logger.Debug("Engine archive downloaded; verifying checksum", "bytes", len(data))
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != r.SHA256 {
		return nil, errors.New("engine_checksum")
	}
	stage = "extract"
	logger.Debug("Engine archive checksum verified; extracting")
	return extractBinary(data, r.Name, strings.HasSuffix(r.URL, ".zip"))
}
func extractBinary(data []byte, name string, zipped bool) ([]byte, error) {
	var found []byte
	read := func(r io.Reader, size int64) error {
		if found != nil || size < 1 || size > maxBinary {
			return errors.New("invalid archive member")
		}
		var err error
		found, err = io.ReadAll(io.LimitReader(r, maxBinary+1))
		if err != nil || len(found) > maxBinary {
			return errors.New("invalid binary")
		}
		return nil
	}
	if zipped {
		z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range z.File {
			if path.Base(f.Name) == name {
				if !f.Mode().IsRegular() {
					return nil, errors.New("unsafe archive entry")
				}
				r, err := f.Open()
				if err != nil {
					return nil, err
				}
				err = read(r, int64(f.UncompressedSize64))
				r.Close()
				if err != nil {
					return nil, err
				}
			}
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		tr := tar.NewReader(io.LimitReader(gz, 2*maxBinary))
		for count := 0; count < 1024; count++ {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if path.Base(h.Name) == name {
				if h.Typeflag != tar.TypeReg {
					return nil, errors.New("unsafe archive entry")
				}
				if err = read(tr, h.Size); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(found) < 4 || !bytes.Equal(found[:4], []byte{0x7f, 'E', 'L', 'F'}) {
		return nil, errors.New("invalid Linux binary")
	}
	return found, nil
}
