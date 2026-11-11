//go:build !darwin || cgo

package main

import (
	"os"

	"fyne.io/systray"
)

const trayAvailable = true

// runTray shows the AudioMuse-AI logo in the menu bar (macOS) or the system tray (Windows,
// Linux) with the same menu as the AudioMuse-AI desktop app. It returns after Quit.
func runTray(b *bridge, pageURL, logPath string, sig <-chan os.Signal) {
	systray.Run(func() {
		systray.SetTemplateIcon(trayIcon, trayIcon)
		systray.SetTooltip("AudioMuse-AI Local Bridge")
		status := systray.AddMenuItem("", "")
		status.Disable()
		systray.AddSeparator()
		open := systray.AddMenuItem("Open in Browser", "Open the settings page")
		toggle := systray.AddMenuItem("", "")
		logItem := systray.AddMenuItem("Open Log", "Show the log, with one line for each API call")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit", "Stop the bridge and close it")

		refresh := func() {
			status.SetTitle("Status: " + b.status())
			if b.running() {
				toggle.SetTitle("Stop Server")
				open.Enable()
			} else {
				toggle.SetTitle("Start Server")
				open.Disable()
			}
			if b.app == nil {
				toggle.Disable()
			}
		}
		refresh()
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					openBrowser(pageURL)
				case <-toggle.ClickedCh:
					if b.running() {
						b.stop()
					} else {
						_ = b.start()
					}
					refresh()
				case <-logItem.ClickedCh:
					openLog(logPath)
				case <-quit.ClickedCh:
					systray.Quit()
					return
				case <-sig:
					systray.Quit()
					return
				}
			}
		}()
	}, b.stop)
	b.stop() // on macOS the exit callback above does not run after Quit
}
