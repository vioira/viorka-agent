package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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
