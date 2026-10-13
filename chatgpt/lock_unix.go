//go:build !windows

package chatgpt

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockFile(f *os.File) error         { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func secureDirectory(path string) error { return os.Chmod(path, 0700) }
func checkPrivatePermissions(info os.FileInfo) error {
	if info.Mode().Perm()&0077 != 0 {
		return os.ErrPermission
	}
	return nil
}
