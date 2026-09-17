package notes

import (
	"os"
	"syscall"
)

func statIdentity(info os.FileInfo) (ctime int64, inode uint64) {
	st := info.Sys().(*syscall.Stat_t)
	return st.Ctim.Nano(), st.Ino
}
