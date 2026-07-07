// M3: live-sync daemon.
//
// Watches for claude.ai activity (Cookie-DB writes) and incrementally syncs new
// AND updated conversations to Markdown, diffing against a persisted state file so
// each cycle only re-fetches what actually changed.
//
// Why a per-user logon autostart (Startup-folder .vbs, not the Windows Task Scheduler
// API — see startupVbsPath below) and NOT a classic Windows service:
// the DPAPI master key is user-scoped and the CDP harvest needs a *visible* Chrome
// (Cloudflare blocks headless). A session-0 LocalSystem service can do neither — it
// can't decrypt the user's cookies and can't show a browser on the user's desktop.
// So the daemon must live in the interactive user session; `install-task` registers
// it to start at logon under the current user.
//
//   schroedinger-sync-go watch [outDir] [intervalMinutes]   # run the daemon
//   schroedinger-sync-go install-task                       # register logon task
//   schroedinger-sync-go uninstall-task                     # remove it
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultInterval  = 30 * time.Minute
	stateFileName    = ".sync-state.json"
	minFileSizeBytes = 100
)

// syncState persists what we've already harvested: conversation UUID -> its
// server-side updated_at at the time we last wrote it. A mismatch on a later cycle
// means the conversation changed and must be re-fetched.
type syncState struct {
	Conversations map[string]string `json:"conversations"` // uuid -> updated_at
	LastSync      string            `json:"last_sync"`
	LastCookieMod string            `json:"last_cookie_mod"` // Cookie-DB mtime we last acted on
	LastSurfaces  string            `json:"last_surfaces"`   // last time project docs + memory were refreshed
}

const surfacesRefreshEvery = 24 * time.Hour

func statePath(outDir string) string { return filepath.Join(outDir, stateFileName) }

func loadState(outDir string) *syncState {
	s := &syncState{Conversations: map[string]string{}}
	b, err := os.ReadFile(statePath(outDir))
	if err != nil {
		return s // first run
	}
	_ = json.Unmarshal(b, s) // corrupt/partial state file -> fall through to a fresh syncState, same as first-run
	if s.Conversations == nil {
		s.Conversations = map[string]string{}
	}
	return s
}

// saveState writes atomically: marshal to a temp file in the same directory, then rename
// over the target. A plain os.WriteFile can be interrupted mid-write (crash, or two
// instances racing against the same outDir) and leave a truncated/corrupt
// .sync-state.json; os.Rename onto an existing file is atomic on the same volume on both
// Windows and POSIX, so the state file is always either the complete old version or the
// complete new one, never a partial write.
func saveState(outDir string, s *syncState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := statePath(outDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(outDir))
}

// logSink is where logf writes. Default stdout; the daemon redirects it to a MultiWriter
// (stdout + desktop-chats/sync.log) so failures are visible even when launched hidden via
// the WScript logon launcher, whose stdout is discarded.
var logSink io.Writer = os.Stdout

func logf(format string, a ...any) {
	line := fmt.Sprintf("[%s] "+format+"\n", append([]any{time.Now().Format("2006-01-02 15:04:05")}, a...)...)
	fmt.Fprint(logSink, redact(line))
}

// cookieDBPath is the live Cookies SQLite that Claude Desktop writes when active.
func cookieDBPath() string {
	return filepath.Join(claudeDir(), "Network", "Cookies")
}

// cookieMod returns the Cookie-DB modification time as a string, or "" if unreadable.
func cookieMod() string {
	fi, err := os.Stat(cookieDBPath())
	if err != nil {
		return ""
	}
	return fi.ModTime().UTC().Format(time.RFC3339Nano)
}

// isDesktopRunning reports whether Claude Desktop (Claude.exe) currently holds a
// process. Discovered empirically 2026-07-02: DPAPI cookie reads only succeed when
// Desktop is closed — Chromium holds the Cookies SQLite file with an exclusive lock
// (FILE_SHARE_* is refused) the entire time it's running. The cookie-mtime activity
// gate alone can't see this: mtime keeps advancing while Desktop runs, so the old
// gate kept firing "activity" and then failing the harvest every single cycle,
// silently (before the sync.log fix). This check must run BEFORE any cookie read,
// so a locked cycle skips cleanly instead of popping Chrome and failing.
func isDesktopRunning() bool {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq Claude.exe", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false // tasklist itself failed - let the harvest attempt surface the real error
	}
	return strings.Contains(strings.ToLower(string(out)), "claude.exe")
}

// harvestOnce opens a Cloudflare-cleared session, lists all conversations, and
// fetches only the ones that are new or whose updated_at changed since last time.
// On first run (no state) it SEEDS state from conversations that already have a
// Markdown file on disk (the M2 harvest) instead of re-downloading all 241.
func harvestOnce(outDir string, s *syncState) (newN, changedN, seedN, errN int, err error) {
	if mkErr := os.MkdirAll(outDir, 0o750); mkErr != nil { // #nosec G703 -- outDir is a local CLI arg, see cdp.go
		return 0, 0, 0, 0, mkErr
	}
	get, _, teardown, e := openClaudeSession()
	if e != nil {
		return 0, 0, 0, 0, e
	}
	defer teardown()

	org, e := resolveOrg(get)
	if e != nil {
		return 0, 0, 0, 0, e
	}

	var all []convSummary
	const limit = 100
	for offset := 0; ; offset += limit {
		body, lerr := getWithRetry(get,
			fmt.Sprintf("/api/organizations/%s/chat_conversations?limit=%d&offset=%d", org, limit, offset), 6)
		if lerr != nil {
			return newN, changedN, seedN, errN, fmt.Errorf("list: %w", lerr)
		}
		var page []convSummary
		if json.Unmarshal([]byte(body), &page) != nil {
			return newN, changedN, seedN, errN, fmt.Errorf("list parse: %s", trunc(body, 160))
		}
		all = append(all, page...)
		if len(page) < limit {
			break
		}
		time.Sleep(800 * time.Millisecond)
	}
	logf("listed %d conversations", len(all))

	for _, c := range all {
		fname := filepath.Join(outDir, fmt.Sprintf("%s_%s_%s.md",
			trunc(c.CreatedAt, 10), trunc(c.UUID, 8), sanitize(c.Name)))
		fileOK := false
		if fi, e := os.Stat(fname); e == nil && fi.Size() > minFileSizeBytes { // #nosec G703 -- see cdp.go
			fileOK = true
		}
		known, seen := s.Conversations[c.UUID]

		switch {
		case seen && known == c.UpdatedAt && fileOK:
			// up to date — nothing to do
			continue
		case !seen && fileOK:
			// already harvested by M2; trust it, seed state so we don't re-download
			s.Conversations[c.UUID] = c.UpdatedAt
			seedN++
			continue
		}

		// new, changed, or missing-file -> (re)fetch
		body, ferr := fetchConvBody(get, org, c.UUID)
		if ferr != nil {
			errN++
			logf("  ERR %.40s: %v", c.Name, ferr)
			continue
		}
		if werr := os.WriteFile(fname, []byte(convToMarkdown(body)), 0o600); werr != nil { // #nosec G703 -- see cdp.go
			errN++
			continue
		}
		if seen {
			changedN++
		} else {
			newN++
		}
		s.Conversations[c.UUID] = c.UpdatedAt
		logf("  synced %.50s", c.Name)
		time.Sleep(1200 * time.Millisecond)
	}
	return newN, changedN, seedN, errN, nil
}

// parseWatchArgs reads the shared outDir/interval CLI args used by both `watch` and
// `tray` (they're the same daemon, just with/without a visible tray icon).
// defaultOutDir is a stable, well-known per-user data directory — NOT relative to the
// current working directory. An installed app can be launched from a Start Menu icon,
// a Desktop shortcut, or Task Scheduler, each of which may set a different (or no)
// working directory; relying on a relative "desktop-chats" path (as early dev builds
// of this tool did) would silently scatter data across whichever folder happened to be
// current at launch. Explicit outDir args (CLI or install-task) always override this.
func defaultOutDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "SchroedingerSync", "desktop-chats")
}

func parseWatchArgs() (outDir string, interval time.Duration) {
	outDir = defaultOutDir()
	interval = defaultInterval
	if len(os.Args) > 2 && os.Args[2] != "" {
		outDir = os.Args[2]
	}
	if len(os.Args) > 3 {
		if m, e := time.ParseDuration(os.Args[3] + "m"); e == nil && m > 0 {
			interval = m
		}
	}
	return outDir, interval
}

// setupFileLog redirects logf's output to outDir/sync.log in addition to stdout, so a
// hidden (WScript-launched or tray-only) daemon leaves a diagnosable trail even though
// nobody is watching a console.
func setupFileLog(outDir string) {
	if err := os.MkdirAll(outDir, 0o750); err == nil { // #nosec G703 -- see cdp.go
		if f, ferr := os.OpenFile(filepath.Join(outDir, "sync.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); ferr == nil { // #nosec G703 G304 -- outDir is a local CLI arg, see cdp.go
			logSink = io.MultiWriter(os.Stdout, f)
		}
	}
}

// runCycle performs exactly one sync cycle (chat harvest + due surfaces refresh) and
// returns a short, human-readable status line. Shared by watchLoop (headless) and the
// tray daemon (tray.go) — both must behave identically, so there is exactly one place
// that decides what a "cycle" does.
func runCycle(outDir string) string {
	if isDesktopRunning() {
		return "Claude Desktop läuft — Cookie-DB gesperrt (Desktop schließen zum Syncen)"
	}

	s := loadState(outDir)
	mod := cookieMod()
	activity := mod != "" && mod != s.LastCookieMod
	first := s.LastSync == ""

	status := "keine Aktivität seit letztem Sync — übersprungen"
	if activity || first {
		logf("sync trigger (activity=%v first=%v)", activity, first)
		newN, changedN, seedN, errN, err := harvestOnce(outDir, s)
		if err != nil {
			if errors.Is(err, ErrDesktopNotFound) {
				// Clean, actionable sentence — no raw Go/OS error text needed for this
				// specific, common case (see the Apple/defensive-design principle).
				status = ErrDesktopNotFound.Error()
			} else {
				status = fmt.Sprintf("Sync fehlgeschlagen: %v", err)
			}
		} else {
			s.LastSync = time.Now().UTC().Format(time.RFC3339)
			s.LastCookieMod = mod
			if e := saveState(outDir, s); e != nil {
				logf("state save error: %v", e)
			}
			status = fmt.Sprintf("Sync: %d neu, %d geändert, %d bekannt, %d Fehler", newN, changedN, seedN, errN)
		}
	}
	logf("%s", status)

	// Project docs + memory change far less often than chats; refresh daily rather
	// than every cycle, using a fresh Chrome session.
	s = loadState(outDir)
	dueSurfaces := s.LastSurfaces == ""
	if !dueSurfaces {
		if t, e := time.Parse(time.RFC3339, s.LastSurfaces); e == nil {
			dueSurfaces = time.Since(t) >= surfacesRefreshEvery
		} else {
			dueSurfaces = true
		}
	}
	if dueSurfaces {
		if e := refreshSurfaces(outDir); e != nil {
			logf("surfaces refresh FAILED: %v (will retry next cycle)", e)
		} else {
			s.LastSurfaces = time.Now().UTC().Format(time.RFC3339)
			if e := saveState(outDir, s); e != nil {
				logf("state save error: %v", e)
			}
			logf("surfaces refresh done (project docs + memory)")
		}
	}
	return status
}

// watchLoop is the headless daemon: every interval, run a cycle. No visible tray icon —
// use `tray` instead for a normal desktop session; `watch` is for running this without
// any GUI at all (e.g. under a task runner).
func watchLoop() {
	outDir, interval := parseWatchArgs()
	setupFileLog(outDir)
	logf("schroedinger watch: outDir=%s interval=%s", outDir, interval)
	for {
		runCycle(outDir)
		time.Sleep(interval)
	}
}

// refreshSurfaces opens its own Chrome session and re-harvests project docs + the
// memory blob. Kept separate from harvestOnce (chats) so a surfaces failure never
// blocks the chat sync, and vice versa.
func refreshSurfaces(outDir string) error {
	get, _, teardown, e := openClaudeSession()
	if e != nil {
		return e
	}
	defer teardown()
	org, e := resolveOrg(get)
	if e != nil {
		return e
	}
	docN, errN := harvestProjects(get, org, outDir)
	logf("  projects: %d docs, %d errors", docN, errN)
	if e := harvestMemory(get, org, outDir); e != nil {
		return fmt.Errorf("memory: %w", e)
	}
	return nil
}

// startupVbsPath is the per-user Startup-folder launcher. Dropping a file here
// makes Windows run it at logon in the interactive user session — no admin
// required (unlike schtasks /Create, which is access-denied for a normal user).
func startupVbsPath() string {
	return filepath.Join(os.Getenv("APPDATA"),
		"Microsoft", "Windows", "Start Menu", "Programs", "Startup",
		"GSOC-Schroedinger-Sync.vbs")
}

// installTask drops a hidden autostart launcher into the user's Startup folder so
// the daemon starts at logon in the interactive session (DPAPI access + a desktop
// for the visible Chrome). WScript window-style 0 hides only this program's own
// console window — the tray icon still appears, because it's drawn by explorer.exe
// via the Shell_NotifyIconW API call, not by a window this process owns. The periodic
// Chrome popups during an actual sync are still visible, same as before.
//
// Accepts an optional outDir (schroedinger-sync.exe install-task [outDir]) so autostart
// can be pinned to an existing data folder — e.g. migrating from a dev build's
// CWD-relative "desktop-chats" to the installed app without losing continuity with
// whatever already ingests that folder. Omit it to use defaultOutDir().
// vbsLauncherContent builds the .vbs autostart launcher's content. Pulled out as a pure
// function (no side effects) specifically so the quote-escaping logic — VBScript
// escapes a literal " inside a string literal by doubling it ("") — can be unit tested.
// This is exactly the kind of hand-rolled string-escaping that's easy to get subtly
// wrong (an earlier draft of this function used Go's %q, which doubles backslashes
// instead of quotes — wrong escaping convention entirely, caught by TestVbsLauncherContent).
func vbsLauncherContent(exe, outDir string) string {
	argsPart := " tray"
	if outDir != "" {
		// outDir is a caller-supplied CLI arg (install-task's os.Args[2], see installTask
		// below) — it may itself contain a literal " character. Left unescaped, that "
		// would terminate the ""..."" quoted-argument sequence early and splice whatever
		// follows as new, attacker-controlled VBScript into a file Windows executes
		// unattended at every logon. Double it first, per VBScript's own quote-escaping
		// convention, exactly like exe already is below — this mirrors doing the same
		// escaping this function already relies on, just applied to untrusted input too.
		// exe (os.Executable()'s own result) needs no such escaping: a Windows path
		// structurally cannot contain ", it's a reserved filename character.
		escapedOutDir := strings.ReplaceAll(outDir, `"`, `""`)
		argsPart = fmt.Sprintf(` tray ""%s""`, escapedOutDir)
	}
	return "' GSOC Schroedinger live-sync daemon - autostart at logon (hidden console, visible tray icon)\r\n" +
		fmt.Sprintf("CreateObject(\"WScript.Shell\").Run \"\"\"%s\"\"%s\", 0, False\r\n", exe, argsPart)
}

func installTask() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Println("FAIL: cannot resolve own path:", err)
		os.Exit(1)
	}
	outDir := ""
	if len(os.Args) > 2 && os.Args[2] != "" {
		outDir = os.Args[2]
	}
	vbs := startupVbsPath()
	content := vbsLauncherContent(exe, outDir)
	if err := os.WriteFile(vbs, []byte(content), 0o600); err != nil {
		fmt.Println("FAIL @write startup launcher:", err)
		os.Exit(1)
	}
	fmt.Printf("Installed logon autostart (no admin needed):\n  %s\n  -> %s tray (console hidden, tray icon visible)\n", vbs, exe)
	fmt.Printf("Starts at next logon. Start now with:\n  start \"\" \"%s\" tray\n", exe)
}

func uninstallTask() {
	vbs := startupVbsPath()
	if err := os.Remove(vbs); err != nil {
		fmt.Println("nothing to remove (", err, ")")
		return
	}
	fmt.Println("Removed logon autostart:", vbs)
}
