package metrics

import "syscall"

// statfsUsage reports used/total bytes for the filesystem containing path
// via statfs, which is a single cheap syscall -- safe to run on every scrape,
// unlike a directory walk.
func statfsUsage(path string) (usedBytes, totalBytes uint64, ok bool) {
	if path == "" {
		path = "."
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, false
	}
	// Bsize is int64 on linux; guard the conversion so a hostile or exotic
	// filesystem cannot wrap the arithmetic into a tiny number.
	if stat.Bsize <= 0 {
		return 0, 0, false
	}
	block := uint64(stat.Bsize)
	totalBytes = block * stat.Blocks
	freeBytes := block * stat.Bavail
	if freeBytes > totalBytes {
		return 0, 0, false
	}
	return totalBytes - freeBytes, totalBytes, true
}
