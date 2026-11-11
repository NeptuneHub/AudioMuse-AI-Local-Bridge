//go:build !darwin && !windows

package main

import "os"

// fileCreated returns the modification time in ms: this OS does not report a birth time.
func fileCreated(fi os.FileInfo) int64 { return fi.ModTime().UnixMilli() }
