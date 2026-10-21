//go:build !windows

package sources

import (
	"fmt"
	"os"
	"syscall"
)

// fileID returns a stable identity (device:inode) for a file
func fileID(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	}
	return ""
}
