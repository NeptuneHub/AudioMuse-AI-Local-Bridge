package main

import (
	"os"
	"syscall"
)

// fileCreated returns the file birth time in ms, falling back to the modification time.
func fileCreated(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Birthtimespec.Sec > 0 {
		return st.Birthtimespec.Sec*1000 + st.Birthtimespec.Nsec/1e6
	}
	return fi.ModTime().UnixMilli()
}
