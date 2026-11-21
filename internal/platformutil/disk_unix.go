//go:build !windows

package platformutil

import "syscall"

// DiskAvailable returns available bytes on the filesystem containing path, or
// 0 when the platform query fails.
func DiskAvailable(path string) uint64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0
	}
	return stat.Bavail * uint64(stat.Bsize) //nolint:gosec // G115: filesystem block size is small and non-negative
}
