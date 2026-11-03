package collector

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

var errNotFilesystemDirectory = errors.New("mount is not a directory")

type filesystemMount struct{ point, kind, device, source string }

// Only local disk filesystems are probed. In particular, never stat NFS, CIFS,
// FUSE or autofs mounts, which can wait on an unavailable remote server.
func localFilesystem(kind, point string) bool {
	switch kind {
	case "ext2", "ext3", "ext4", "xfs", "btrfs", "f2fs", "jfs", "reiserfs", "nilfs2", "bcachefs", "vfat", "exfat", "ntfs3":
		return true
	case "overlay":
		return point == "/" // Container root, never count nested overlay views.
	case "zfs":
		// Include quota-limited container roots. Other datasets can share pool
		// capacity, so summing every ZFS mount would overstate available space.
		return point == "/"
	}
	return false
}

func mountPath(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		if i+3 >= len(s) {
			return "", fmt.Errorf("invalid mount escape")
		}
		switch s[i : i+4] {
		case `\040`:
			b.WriteByte(' ')
		case `\134`:
			b.WriteByte('\\')
		default:
			return "", fmt.Errorf("unsupported mount path escape")
		}
		i += 3
	}
	p := b.String()
	if len(p) == 0 || len(p) > 512 || p[0] != '/' || path.Clean(p) != p || !utf8.ValidString(p) || strings.ContainsAny(p, "\x00\n\r\t") {
		return "", fmt.Errorf("invalid mount path")
	}
	return p, nil
}

func parseFilesystemMounts(data string) ([]filesystemMount, error) {
	if len(data) > 262144 {
		return nil, fmt.Errorf("mount table too large")
	}
	mounts := []filesystemMount{}
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) > 4096 {
		return nil, fmt.Errorf("too many mounts")
	}
	for _, line := range lines {
		f := strings.Fields(line)
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || len(f) < sep+4 {
			return nil, fmt.Errorf("invalid mountinfo")
		}
		// Filter types before decoding paths or probing anything.
		if !localFilesystem(f[sep+1], f[4]) {
			continue
		}
		point, err := mountPath(f[4])
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, filesystemMount{point, f[sep+1], f[2], f[sep+2]})
		if len(mounts) > 128 {
			return nil, fmt.Errorf("too many local mount candidates")
		}
	}
	// Root first, then the shortest mount path: stable choice for bind aliases.
	sort.Slice(mounts, func(i, j int) bool {
		if len(mounts[i].point) != len(mounts[j].point) {
			return len(mounts[i].point) < len(mounts[j].point)
		}
		return mounts[i].point < mounts[j].point
	})
	return mounts, nil
}
