package main

import _ "embed"

// The AudioMuse-AI menu bar icon: black on transparent, so macOS colors it for light and dark menu bars.
//
//go:embed assets/tray-macos.png
var trayIcon []byte
