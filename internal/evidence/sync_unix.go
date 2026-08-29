//go:build unix

package evidence

import "os"

// syncDir fsyncs a directory so entries created in it survive a crash.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
