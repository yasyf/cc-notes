package gitobj

import (
	"os"
	"syscall"
)

// StatIdentity returns the change time in nanoseconds and the inode number
// behind info.
func StatIdentity(info os.FileInfo) (ctime int64, inode uint64) {
	st := info.Sys().(*syscall.Stat_t)
	return st.Ctimespec.Nano(), st.Ino
}
