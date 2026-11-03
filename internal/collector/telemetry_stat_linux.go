package collector

import (
	"fmt"
	"syscall"
)

func statTelemetryFilesystem(path string) (filesystemCapacity, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err == syscall.ENOTDIR {
		return filesystemCapacity{}, errNotFilesystemDirectory
	}
	if err != nil {
		return filesystemCapacity{}, err
	}
	defer syscall.Close(fd)
	var stat syscall.Statfs_t
	if err = syscall.Fstatfs(fd, &stat); err != nil {
		return filesystemCapacity{}, err
	}
	if stat.Bsize <= 0 {
		return filesystemCapacity{}, fmt.Errorf("invalid filesystem block size")
	}
	return capacityFromBlocks(fmt.Sprintf("%d:%v", stat.Type, stat.Fsid), stat.Blocks, stat.Bfree, stat.Bavail, uint64(stat.Bsize))
}
