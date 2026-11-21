//go:build windows

package platformutil

import "golang.org/x/sys/windows"

// DiskAvailable returns available bytes on the volume containing path, or 0
// when the platform query fails.
func DiskAvailable(path string) uint64 {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &free, &total, &totalFree); err != nil {
		return 0
	}
	return free
}
