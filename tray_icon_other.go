//go:build !darwin && !windows

package main

import _ "embed"

// The AudioMuse-AI logo for the system tray.
//
//go:embed assets/tray-linux.png
var trayIcon []byte
