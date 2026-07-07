// Tray daemon: hosts a system-tray icon (github.com/gogpu/systray — pure Go, zero CGO,
// calls Shell_NotifyIconW via golang.org/x/sys/windows, same mechanism main.go already
// uses for DPAPI — see go.mod for why staying CGO-free matters here) around the same
// sync engine watchLoop uses. This is the recommended way to run Schroedinger day to
// day: a visible, dismissible status icon instead of a silent headless process.
//
// Icons are generated in-process (no asset file, no go:embed complexity) — swap
// trayIcon()/trayIconDark() for a real embedded PNG later if a proper icon is designed.
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/gogpu/systray"
)

// statusHolder is a tiny mutex-guarded string. The status is written by the background
// sync goroutine (after every runCycle) and read from the "Status anzeigen" menu callback,
// which systray dispatches on its own goroutine when the user clicks — two goroutines
// touching a plain string with no synchronization is a data race (a torn read could
// surface a garbled status in the toast notification).
type statusHolder struct {
	mu sync.RWMutex
	s  string
}

func (h *statusHolder) set(v string) { h.mu.Lock(); h.s = v; h.mu.Unlock() }
func (h *statusHolder) get() string  { h.mu.RLock(); defer h.mu.RUnlock(); return h.s }

func trayMain() {
	outDir, interval := parseWatchArgs()
	setupFileLog(outDir)
	logf("schroedinger tray: outDir=%s interval=%s", outDir, interval)

	tray := systray.New()
	menu := systray.NewMenu()

	status := &statusHolder{s: "wird geprüft…"}
	syncNow := make(chan struct{}, 1)

	menu.Add("Jetzt synchronisieren", func() {
		select {
		case syncNow <- struct{}{}:
		default: // a cycle is already pending/running — don't queue a second one
		}
	})
	menu.Add("Status anzeigen", func() {
		_ = tray.ShowNotification("Schroedinger Sync", status.get())
	})
	menu.Add("Logs öffnen", func() {
		if err := openInExplorer(filepath.Join(outDir, "sync.log")); err != nil {
			logf("open logs error: %v", err)
		}
	})
	menu.AddSeparator()
	menu.Add("Beenden", func() {
		tray.Remove()
		os.Exit(0)
	})

	tray.SetIcon(trayIcon(color.RGBA{R: 0, G: 150, B: 220, A: 255})).
		SetDarkModeIcon(trayIcon(color.RGBA{R: 60, G: 200, B: 255, A: 255})).
		SetTooltip("Schroedinger Sync — wird gestartet…").
		SetMenu(menu)
	// SystemTray.Show() returns *SystemTray (fluent chaining), not error — matching the
	// library's own official example, which also calls it as a bare statement. An
	// earlier version of this code mistakenly treated the returned pointer as an error
	// (a nil *SystemTray-shaped "err" is never nil as an interface, so the check fired
	// on every single launch, logging a bogus "tray show error: &{0x...}" even on
	// success — the pointer's default %v formatting, not a real failure).
	tray.Show()

	go func() {
		for {
			_ = tray.SetTooltip("Schroedinger Sync — synchronisiere…")
			status.set(runCycle(outDir))
			_ = tray.SetTooltip("Schroedinger Sync — " + status.get())
			select {
			case <-time.After(interval):
			case <-syncNow:
			}
		}
	}()

	if err := tray.Run(); err != nil {
		logf("tray run error: %v", err)
	}
}

// openInExplorer opens a file's containing folder (or the file itself) with the OS
// default handler — the same "open the folder for me" action any normal Windows app
// offers, not a way to run arbitrary commands: the argument is always a path this
// program itself constructed from outDir + a fixed filename, never free-form input.
func openInExplorer(path string) error {
	return exec.Command("explorer", "/select,", path).Run() // #nosec G204 -- path is always outDir+fixed filename, not user/network input
}

// trayIcon renders a small filled circle with a white ring — a neutral placeholder
// visible against both light and dark taskbars. Swap for a real designed icon later.
func trayIcon(c color.RGBA) []byte {
	const size = 22
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	cx, cy, r := float64(size)/2, float64(size)/2, float64(size)/2-1
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			d := dx*dx + dy*dy
			switch {
			case d <= (r-2)*(r-2):
				img.SetRGBA(x, y, c)
			case d <= r*r:
				img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		fmt.Println("icon encode error:", err) // unreachable in practice (fixed in-memory image), no reason to fail startup over it
	}
	return buf.Bytes()
}
