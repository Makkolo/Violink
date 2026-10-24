// tray.go - the system tray icon and its menu.
//
// Violink has no main window. On Windows it lives in the notification area,
// on Fedora KDE in the system tray. The menu opens the web UI, the program
// folder and the log, or quits.

package main

import (
	_ "embed"
	"os/exec"
	"path/filepath"
	"runtime"

	"fyne.io/systray"
)

//go:embed icons/violink.ico
var iconICO []byte

//go:embed icons/violink-64.png
var iconPNG []byte

// runTray shows the tray icon and blocks until Quit is chosen (or quitTray is called).
func runTray(onQuit func()) {
	systray.Run(func() {
		if runtime.GOOS == "windows" {
			systray.SetIcon(iconICO)
		} else {
			systray.SetIcon(iconPNG)
		}
		systray.SetTitle("")
		systray.SetTooltip("Violink " + version + " · scriptable I/O bridge")

		title := systray.AddMenuItem("Violink "+version, "")
		title.Disable()
		systray.AddSeparator()

		var openWeb *systray.MenuItem
		if config.WebUI.Enabled {
			openWeb = systray.AddMenuItem("Open web UI", webURL(config.WebUI))
		}
		openFolder := systray.AddMenuItem("Open program folder", baseDir)
		openLog := systray.AddMenuItem("Open log file", "")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit", "Stop Violink")

		go func() {
			var webClicks chan struct{}
			if openWeb != nil {
				webClicks = openWeb.ClickedCh
			}
			for {
				select {
				case <-webClicks:
					openInDesktop(webURL(config.WebUI))
				case <-openFolder.ClickedCh:
					openInDesktop(baseDir)
				case <-openLog.ClickedCh:
					openInDesktop(filepath.Join(resolvePath(config.Log.Folder), "violink.log"))
				case <-quit.ClickedCh:
					logInfo("quit chosen in the tray menu")
					systray.Quit()
					return
				}
			}
		}()
	}, onQuit)
}

func quitTray() {
	systray.Quit()
}

// openInDesktop opens a URL, folder or file with the default program.
func openInDesktop(target string) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// rundll32 instead of "cmd /c start", so no console window flashes up.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	} else {
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		logWarn("cannot open %s: %v", target, err)
		return
	}
	go cmd.Wait()
}
