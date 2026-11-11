//go:build darwin && !cgo

package main

import "os"

// The macOS menu bar icon needs cgo. A build without it (for example cross-compiled from
// another system) runs in the terminal only.
const trayAvailable = false

func runTray(*bridge, string, string, <-chan os.Signal) {}
