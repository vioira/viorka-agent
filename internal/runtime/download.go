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
	"net/http"
	"path"
	"strings"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

const maxArchive = 128 << 20
const maxBinary = 256 << 20

func Download(ctx context.Context, r v1.EngineRelease) ([]byte, error) {
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
		return nil, errors.New("engine_download_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("engine_download_failed")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArchive+1))
	if err != nil || len(data) > maxArchive {
		return nil, errors.New("engine_download_failed")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != r.SHA256 {
		return nil, errors.New("engine_checksum")
	}
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
