package main

import _ "embed"

// The AudioMuse-AI logo for the system tray. Windows needs the .ico format.
//
//go:embed assets/icon.ico
var trayIcon []byte
