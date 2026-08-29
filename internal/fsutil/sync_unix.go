//go:build unix

package fsutil

import "os"

// SyncFile fsyncs a file so its contents survive a crash.
func SyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// SyncDir fsyncs a directory so entries created in it survive a crash.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
