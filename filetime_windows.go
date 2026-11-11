package main

import (
	"os"
	"syscall"
)

// fileCreated returns the file creation time in ms, falling back to the modification time.
func fileCreated(fi os.FileInfo) int64 {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok && d.CreationTime.Nanoseconds() > 0 {
		return d.CreationTime.Nanoseconds() / 1e6
	}
	return fi.ModTime().UnixMilli()
}
