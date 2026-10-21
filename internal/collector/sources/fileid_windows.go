//go:build windows

package sources

import "os"

// fileID is not available on Windows; rotation detection falls back to os.SameFile
func fileID(info os.FileInfo) string {
	return ""
}
