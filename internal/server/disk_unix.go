//go:build unix

package server

import "syscall"

// freeDiskMB reports free space (MB) at path. ok=false if it can't be determined.
func freeDiskMB(path string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	return (int64(st.Bavail) * int64(st.Bsize)) / (1024 * 1024), true
}

// diskUsageMB reports the total and available space (MB) of the filesystem at
// path. ok=false if it can't be determined.
func diskUsageMB(path string) (totalMB, freeMB int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bs := int64(st.Bsize)
	return int64(st.Blocks) * bs / (1024 * 1024), int64(st.Bavail) * bs / (1024 * 1024), true
}
