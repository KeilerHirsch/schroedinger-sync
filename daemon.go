// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

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
//	schroedinger-sync-go watch [outDir] [intervalMinutes]   # run the daemon
//	schroedinger-sync-go install-task                       # register logon task
//	schroedinger-sync-go uninstall-task                     # remove it
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
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
	LastCookieMod string            `json:"last_cookie_mod"`        // Cookie-DB mtime we last acted on
	LastSurfaces  string            `json:"last_surfaces"`          // last time project docs + memory were refreshed
	RetryCycles   int               `json:"retry_cycles,omitempty"` // consecutive no-progress error cycles; caps the retry storm (see cookieWatermark)
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
	// Unique temp name (not a fixed ".tmp"): if two daemon instances ever share an outDir
	// (Startup-folder tray + a manual `watch`), a fixed temp name means both write and rename
	// THE SAME file, racing each other — one rename can then fail because the other already
	// moved it. A per-write temp keeps the atomic-rename invariant intact regardless.
	f, err := os.CreateTemp(outDir, ".sync-state-*.tmp") // #nosec G304 G703 -- outDir is a local CLI arg, see cdp.go
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, werr := f.Write(b); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return werr
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	if err := os.Rename(tmp, statePath(outDir)); err != nil {
		_ = os.Remove(tmp) // don't leave the temp behind if the rename fails (e.g. the state file is locked by another process / AV)
		return err
	}
	return nil
}

// logSink is where logf writes, behind an atomic pointer. Default stdout; setupFileLog
// swaps it to a MultiWriter (stdout + desktop-chats/sync.log) so failures are visible even
// when launched hidden via the WScript logon launcher, whose stdout is discarded. Atomic
// rather than a plain package var: today setupFileLog always runs before trayMain/
// watchLoop start their background sync goroutine, so a plain var happens to be safe — but
// that ordering is an invariant a future call site could silently break (the same bug
// class the os.Stdout redactor was hardened against). An atomic load/store removes the
// invariant instead of just documenting it.
var logSink atomic.Pointer[io.Writer]

func init() {
	var w io.Writer = os.Stdout
	logSink.Store(&w)
}

func logf(format string, a ...any) {
	line := fmt.Sprintf("[%s] "+format+"\n", append([]any{time.Now().Format("2006-01-02 15:04:05")}, a...)...)
	fmt.Fprint(*logSink.Load(), redact(line))
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

// claudeDesktopPathHint is a stable substring of Claude Desktop's installed executable
// path (MSIX/Store package: ...\WindowsApps\Claude_<version>_x64...\app\claude.exe).
const claudeDesktopPathHint = `WindowsApps\Claude_`

// processPathContains reports whether any currently running process named imageName has
// an executable path containing pathSubstring (case-insensitive). imageName is always a
// fixed literal from a call site in this file, never external input, so building the
// PowerShell filter via Sprintf carries no injection risk. PowerShell's Get-CimInstance is
// used (not tasklist, which has no path column, and not wmic, deprecated and prone to
// encoding issues when its output is captured through a non-native console) specifically
// because this needs the executable PATH, not just the image name — see isDesktopRunning.
func processPathContains(imageName, pathSubstring string) bool {
	script := fmt.Sprintf(
		`(Get-CimInstance Win32_Process -Filter "Name='%s'" -ErrorAction SilentlyContinue).ExecutablePath`,
		imageName)
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output() // #nosec G204 -- imageName is always a fixed literal ("claude.exe"/"Code.exe") from a call site in this file, never user/network input
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), strings.ToLower(pathSubstring))
}

// isDesktopRunning reports whether Claude Desktop specifically — not any process that
// merely happens to be named claude.exe — currently holds a process. Discovered
// empirically 2026-07-02: DPAPI cookie reads only succeed when Desktop is closed —
// Chromium holds the Cookies SQLite file with an exclusive lock (FILE_SHARE_* is refused)
// the entire time it's running. This check must run BEFORE any cookie read, so a locked
// cycle skips cleanly instead of popping Chrome and failing.
//
// Discovered 2026-07-18: a bare `tasklist /FI "IMAGENAME eq Claude.exe"` (the original
// form of this check) cannot tell Claude Desktop apart from Claude Code's own CLI binary,
// which also runs as a process literally named claude.exe (bundled at
// .vscode\extensions\anthropic.claude-code-*\resources\native-binary\claude.exe) — so the
// old check reported "Desktop is running" (and skipped every sync cycle) whenever the CLI
// alone was active, even with the actual Desktop app fully closed. Path-matching against
// claudeDesktopPathHint distinguishes them.
func isDesktopRunning() bool {
	return processPathContains("claude.exe", claudeDesktopPathHint)
}

// isVSCodeRunning reports whether VS Code (Code.exe) currently holds a process — the
// "actively working" half of supervise's autostart gate (Michael, 18.07.: "Autostart soll
// sein wenn Desktopclaude oder VSCode online sind"). Unlike Claude Desktop, Code.exe has no
// realistic name collision to disambiguate, so a plain tasklist image-name check suffices —
// no need for the slower PowerShell path lookup isDesktopRunning uses.
func isVSCodeRunning() bool {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq Code.exe", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "code.exe")
}

// --- sync-state decision (pure, unit-tested) ---

// reUpdatedHeader matches the "- Updated: <ts>" line convToMarkdown writes into every
// exported conversation file.
var reUpdatedHeader = regexp.MustCompile(`(?m)^- Updated: (.+)$`)

// fileConvUpdatedAt returns the server updated_at recorded in an already-exported file's
// header (the value convToMarkdown wrote, truncated to 19 chars), or "" if the file is
// unreadable or has no such header. Lets the daemon tell a genuinely-current M2 file from
// one that went stale before the daemon first ran. Reads only a bounded prefix — the header
// is always in the first few lines, so there's no need to load a multi-MB transcript.
func fileConvUpdatedAt(path string) string {
	f, err := os.Open(path) // #nosec G304 G703 -- path is outDir + sanitised filename, see cdp.go
	if err != nil {
		return ""
	}
	// Close error on a read-only file we're done with carries nothing actionable.
	defer func() { _ = f.Close() }()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf) // ReadFull fills buf unless the file is shorter (ErrUnexpectedEOF); either way buf[:n] holds the full bounded prefix, so a single short Read can't drop the header
	m := reUpdatedHeader.FindSubmatch(buf[:n])
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

type convActionKind int

const (
	actionFetch convActionKind = iota // (re)download the conversation
	actionSkip                        // already current in state + on disk
	actionSeed                        // trust an existing M2 file, record state without downloading
)

// convAction is the daemon's per-conversation decision, pulled out as a pure function so the
// sync state machine — the tool's most important logic — is unit-tested without a live
// browser. `fileUpdated` is fileConvUpdatedAt's result for the on-disk file (only meaningful
// when fileOK).
//
// The actionSeed guard fixes a real data-loss bug: the old code seeded state from ANY
// existing file on the first daemon cycle, recording the CURRENT server updated_at even when
// the on-disk file was older. If a conversation changed between the one-shot M2 harvest and
// the first daemon cycle, that delta was then never re-fetched (state and server agreed on
// the new timestamp, but the file still held the old content). Seeding now requires the file
// to actually match the server version; otherwise we fetch.
func convAction(c convSummary, known string, seen, fileOK bool, fileUpdated string) convActionKind {
	switch {
	case seen && known == c.UpdatedAt && fileOK && fileUpdated == trunc(c.UpdatedAt, 19):
		// Skip only when the ON-DISK header also matches the server (not just "seen + size").
		// Requiring the header heals files poisoned by a pre-fix build that wrote a raw
		// HTML/error page AND recorded state: such a file's header won't match, so it
		// re-fetches instead of being skipped forever.
		return actionSkip
	case !seen && fileOK && fileUpdated == trunc(c.UpdatedAt, 19):
		return actionSeed
	default:
		return actionFetch
	}
}

// maxRetryCycles caps how many consecutive no-progress error cycles hold the Cookie-DB
// watermark before it advances anyway — see cookieWatermark.
const maxRetryCycles = 3

// cookieWatermark decides, after a cycle, BOTH the Cookie-DB watermark to persist and the
// updated consecutive-stall counter. A clean cycle (errN==0) advances the watermark and
// resets the counter. A partial cycle that still made progress holds the watermark (so the
// failed items retry next interval) and resets the counter — we're not stuck. A partial cycle
// with NO progress holds and counts the stall, but only up to maxRetryCycles: after that it
// advances anyway, so one permanently-failing item (e.g. a huge conversation that always
// rate-limits) can't wedge the daemon into re-listing and popping Chrome every interval
// forever with zero Desktop activity.
func cookieWatermark(cur, prev string, madeProgress bool, errN, retries int) (mod string, nextRetries int) {
	if errN == 0 {
		return cur, 0
	}
	if madeProgress {
		return prev, 0
	}
	if retries+1 >= maxRetryCycles {
		return cur, 0
	}
	return prev, retries + 1
}

// harvestOnce opens a Cloudflare-cleared session, lists all conversations, and
// fetches only the ones that are new or whose updated_at changed since last time.
// On first run (no state) it SEEDS state from conversations that already have a
// Markdown file on disk (the M2 harvest) instead of re-downloading all 241.
func harvestOnce(outDir string, s *syncState) (newN, changedN, seedN, errN int, err error) {
	if mkErr := os.MkdirAll(outDir, 0o750); mkErr != nil { // #nosec G304 G703 -- outDir is a local CLI arg, see cdp.go
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

	all, lerr := listAllConversations(get, org)
	if lerr != nil {
		return newN, changedN, seedN, errN, lerr
	}
	logf("listed %d conversations", len(all))

	return syncConversations(get, org, outDir, s, all, itemDelay)
}

// itemDelay is the pacing sleep between successful conversation writes (rate-limit
// friendliness — see fetchConvBodyDelay's doc comment for why claude.ai needs this).
// A package var, not a syncConversations literal, so tests can override it to 0 without
// threading a parameter through every call site that doesn't care about pacing.
var itemDelay = 1200 * time.Millisecond

// syncConversations is harvestOnce's per-conversation decision loop, pulled out to take an
// already-open session (get, org) and an already-listed conversation set as parameters
// instead of opening its own Chrome session internally. This is the daemon's most important
// logic — the actionSkip/Seed/Fetch dispatch, the new/changed progress counting, the
// "don't count a healing rewrite as progress" rule (F3) — and until now it lived entirely
// inside harvestOnce, which only a live browser could exercise. Pulling it out one call
// frame earlier makes it unit-testable with an in-memory `get` mock and a fixture
// []convSummary, matching the pure-function discipline already applied to
// convAction/cookieWatermark — see TestSyncConversations in daemon_test.go.
func syncConversations(get func(string) (string, error), org, outDir string, s *syncState, all []convSummary, delay time.Duration) (newN, changedN, seedN, errN int, err error) {
	for _, c := range all {
		fname := convFilename(outDir, c)
		fileOK := false
		if fi, e := os.Stat(fname); e == nil && fi.Size() > minFileSizeBytes { // #nosec G304 G703 -- see cdp.go
			fileOK = true
		}
		known, seen := s.Conversations[c.UUID]
		fileUpdated := ""
		if fileOK {
			fileUpdated = fileConvUpdatedAt(fname)
		}

		switch convAction(c, known, seen, fileOK, fileUpdated) {
		case actionSkip:
			continue
		case actionSeed:
			// M2 already harvested this AND the on-disk file reflects the current server
			// version — trust it, seed state so we don't re-download.
			s.Conversations[c.UUID] = c.UpdatedAt
			seedN++
			continue
		}

		// actionFetch: new, changed, missing-file, OR an M2 file that went stale before this
		// first daemon cycle -> (re)fetch.
		body, ferr := fetchConvBody(get, org, c.UUID)
		if ferr != nil {
			if errors.Is(ferr, context.DeadlineExceeded) {
				// The whole session timed out mid-harvest: every remaining fetch will fail too.
				// Return a hard error so runCycle reports failure and (via cookieWatermark) does
				// NOT advance the watermark — otherwise the un-fetched tail would be silently
				// reported as a successful cycle and wait for the next activity trigger.
				return newN, changedN, seedN, errN, fmt.Errorf("session deadline exceeded mid-harvest: %w", ferr)
			}
			errN++
			logf("  ERR %.40s: %v", c.Name, ferr)
			continue
		}
		md := convToMarkdown(body)
		if md == "" { // non-conversation response (e.g. HTML) — never persist
			errN++
			logf("  SKIP %.40s: unexpected non-conversation response, not written", c.Name)
			continue
		}
		if werr := os.WriteFile(fname, []byte(md), 0o600); werr != nil { // #nosec G304 G703 -- see cdp.go
			errN++
			logf("  write ERR %.40s: %v", c.Name, werr)
			continue
		}
		// Count as progress only when the state actually moved — a new conversation or a
		// genuine server-side change. Re-writing an UNCHANGED conversation merely to heal a
		// small/headerless on-disk file is NOT progress; counting it would let a perpetually-
		// rewritten file reset the retry-stall counter and defeat cookieWatermark's cap (F3).
		switch {
		case !seen:
			newN++
		case known != c.UpdatedAt:
			changedN++
		}
		s.Conversations[c.UUID] = c.UpdatedAt
		logf("  synced %.50s", c.Name)
		time.Sleep(delay)
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
		} else {
			logf("ignoring invalid interval arg %q — using default %s", os.Args[3], interval)
		}
	}
	return outDir, interval
}

// setupFileLog redirects logf's output to outDir/sync.log in addition to stdout, so a
// hidden (WScript-launched or tray-only) daemon leaves a diagnosable trail even though
// nobody is watching a console.
func setupFileLog(outDir string) {
	if err := os.MkdirAll(outDir, 0o750); err == nil { // #nosec G304 G703 -- see cdp.go
		if f, ferr := os.OpenFile(filepath.Join(outDir, "sync.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); ferr == nil { // #nosec G304 G703 -- outDir is a local CLI arg, see cdp.go
			w := io.MultiWriter(os.Stdout, f)
			logSink.Store(&w)
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
			s.LastCookieMod, s.RetryCycles = cookieWatermark(mod, s.LastCookieMod, newN+changedN > 0, errN, s.RetryCycles)
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
	cleanupTemp() // once per daemon startup, not per cycle -- sweeps crash residue from PIDs that will never run again
	logf("schroedinger watch: outDir=%s interval=%s", outDir, interval)
	for {
		runCycle(outDir)
		time.Sleep(interval)
	}
}

// supervisePollInterval is how often superviseLoop checks whether Desktop/VS Code are
// running. Cheap (one PowerShell + one tasklist call), so a short interval costs nothing
// noticeable while idle.
const supervisePollInterval = 20 * time.Second

// superviseGraceOffPolls is how many consecutive polls must find neither Desktop nor
// VS Code running before supervise actually goes idle. Absorbs a brief gap (VS Code
// restarting, a Desktop auto-update relaunch) without prematurely pausing right before the
// next legitimate sync window would have fired.
const superviseGraceOffPolls = 3

// nextSuperviseState is supervise's pure start/stop decision, pulled out so the hysteresis
// is unit-tested without spawning real processes or sleeping real poll intervals — same
// discipline as convAction/cookieWatermark.
func nextSuperviseState(desktopUp, vscodeUp, wasActive bool, missCount int) (active bool, nextMissCount int) {
	if desktopUp || vscodeUp {
		return true, 0
	}
	if !wasActive {
		return false, 0
	}
	if missCount+1 >= superviseGraceOffPolls {
		return false, 0
	}
	return true, missCount + 1
}

// superviseLoop is the autostart entry point (install-task registers this, not watch/tray
// directly): it stays resident at logon but only runs sync cycles while Claude Desktop or
// VS Code is actually open, going idle (no Chrome, no network — just a process-list poll
// every supervisePollInterval) once neither has been seen for superviseGraceOffPolls
// consecutive polls. Fixes a real gap: `install-task` previously wired straight to `tray`,
// which ran a sync cycle on a fixed interval forever regardless of whether anyone was even
// at the machine (Michael, 18.07.: "Autostart soll sein wenn Desktopclaude oder VSCode
// online sind, aber wieder automatisch beenden wenn beide offline sind — muss nicht
// dauerhaft laufen"). No tray icon here by design (keeps this change to the autostart
// lifecycle only) — `tray`/`watch` remain available for a manually-launched, always-on run.
func superviseLoop() {
	outDir, interval := parseWatchArgs()
	setupFileLog(outDir)
	cleanupTemp() // once per daemon startup, not per cycle -- sweeps crash residue from PIDs that will never run again
	logf("schroedinger supervise: outDir=%s cycleInterval=%s pollInterval=%s", outDir, interval, supervisePollInterval)

	active := false
	missCount := 0
	var lastCycle time.Time

	for {
		desktopUp, vscodeUp := isDesktopRunning(), isVSCodeRunning()
		wasActive := active
		active, missCount = nextSuperviseState(desktopUp, vscodeUp, active, missCount)
		switch {
		case active && !wasActive:
			logf("activity detected (desktop=%v vscode=%v) — resuming sync", desktopUp, vscodeUp)
		case !active && wasActive:
			logf("no Desktop/VS Code activity for %d polls — pausing sync (idle, no Chrome)", superviseGraceOffPolls)
		}

		if active && (lastCycle.IsZero() || time.Since(lastCycle) >= interval) {
			runCycle(outDir)
			lastCycle = time.Now()
		}
		time.Sleep(supervisePollInterval)
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
	_, memErr := harvestMemory(get, org, outDir)
	if errN > 0 || memErr != nil {
		// Don't let a PARTIAL surfaces harvest be recorded as a completed 24h refresh: return
		// an error so runCycle leaves LastSurfaces unset and retries next cycle instead of
		// swallowing the project-doc failures and reporting "done".
		return fmt.Errorf("surfaces incomplete: %d project-doc error(s), memory err=%v", errN, memErr)
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
// outDirHasLineBreak reports whether outDir contains a raw CR or LF. installTask rejects
// such a value before it ever reaches vbsLauncherContent: a line break — unlike a literal
// `"`, which vbsLauncherContent's quote-doubling already neutralizes — would terminate the
// generated VBScript STATEMENT itself, not just the quoted argument, and splice whatever
// follows as new code into a file Windows executes unattended at every logon. Pulled out
// as its own function so the guard is unit-tested without going through installTask's own
// file I/O (os.Executable, writing the Startup-folder launcher).
func outDirHasLineBreak(outDir string) bool {
	return strings.ContainsAny(outDir, "\r\n")
}

func vbsLauncherContent(exe, subcommand, outDir string) string {
	argsPart := " " + subcommand
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
		argsPart = fmt.Sprintf(` %s ""%s""`, subcommand, escapedOutDir)
	}
	return "' GSOC Schroedinger live-sync daemon - autostart at logon (hidden console)\r\n" +
		fmt.Sprintf("CreateObject(\"WScript.Shell\").Run \"\"\"%s\"\"%s\", 0, False\r\n", exe, argsPart)
}

func installTask() {
	exe, err := os.Executable()
	if err != nil {
		fatal("FAIL: cannot resolve own path:", err)
	}
	outDir := ""
	if len(os.Args) > 2 && os.Args[2] != "" {
		outDir = os.Args[2]
	}
	// vbsLauncherContent's quote-doubling correctly stops a `"` from terminating the
	// quoted-argument sequence early, but a raw CR or LF would terminate the VBScript
	// *statement* itself and splice whatever follows as new, unattended-at-every-logon
	// code — defense in depth, since outDir is a local CLI arg here (self-inflicted at
	// worst on Windows, where a command line can't easily carry a literal newline), not a
	// remote-input vector.
	if outDirHasLineBreak(outDir) {
		fatal("FAIL: outDir must not contain a line break:", outDir)
	}
	vbs := startupVbsPath()
	content := vbsLauncherContent(exe, "supervise", outDir)
	if err := os.WriteFile(vbs, []byte(content), 0o600); err != nil {
		fatal("FAIL @write startup launcher:", err)
	}
	fmt.Printf("Installed logon autostart (no admin needed):\n  %s\n  -> %s supervise (console hidden; only syncs while Desktop or VS Code is open)\n", vbs, exe)
	fmt.Printf("Starts at next logon. Start now with:\n  start \"\" \"%s\" supervise\n", exe)
}

func uninstallTask() {
	vbs := startupVbsPath()
	if err := os.Remove(vbs); err != nil {
		fmt.Println("nothing to remove (", err, ")")
		return
	}
	fmt.Println("Removed logon autostart:", vbs)
}

// residueMinAge is how old a %TEMP%\schroedinger_* copyCookieDB leftover must be before
// cleanupTemp will remove it. copyCookieDB's temp dir is randomly named per call (unlike
// chrome-profile, it carries no PID to check liveness against), but it only ever exists for
// the brief duration of one function call — reading a few small files and returning. A
// concurrently-running instance's OWN in-flight copyCookieDB call (supervise autostart
// alongside a manually-launched tray/watch/one-shot command is an explicitly supported
// combination, see README) would have a directory younger than this; anything older is
// unambiguously a crash leftover from a run that never reached its own deferred cleanup.
const residueMinAge = 5 * time.Minute

// cleanupTemp removes on-disk residue this program itself creates outside its main
// data directory (defaultOutDir — desktop-chats/, deliberately NEVER touched here: that's
// the user's own exported conversation history, not installer/runtime state, see README
// and installer/schroedinger-sync.iss's own [UninstallRun] comment):
//
//  1. Stale subdirectories of the chrome-profile tree (chromeProfileDir's parent, cdp.go)
//     whose PID no longer belongs to a running process. Normally only the current
//     process's own PID subdirectory is swept (sweepProfileDir, per-process, so a
//     concurrently-running instance's live profile is never touched) — this widens that to
//     every OTHER subdirectory too, but only once its owning process is confirmed gone.
//     A blind RemoveAll of the whole parent tree would risk deleting files out from under a
//     still-running instance's live Chrome profile: Chromium refuses FILE_SHARE_* on some
//     of its own profile files while running (see isDesktopRunning's doc comment above), so
//     that wouldn't cleanly fail closed — RemoveAll's directory walk could delete other,
//     not-yet-locked files in that same live profile before ever reaching the one that
//     blocks it. Checked via processAlive, not just "does the directory look old", because
//     PID reuse means an old PID number can belong to a brand new, unrelated process.
//  2. Any %TEMP%\schroedinger_* leftover from copyCookieDB (main.go) older than
//     residueMinAge (see its doc comment for why age, not PID, is the right check here) —
//     created fresh with a random suffix every session and cleaned up by its own deferred
//     call on a normal exit; a hard-kill (crash/SIGKILL/panic — no recover() in this
//     codebase) orphans it permanently, since each one gets a new random name with nothing
//     to ever find it again otherwise.
//
// Exposed as its own subcommand (not folded into uninstall-task) so it can also be run
// standalone for manual hygiene without uninstalling, and wired into the installer's
// [UninstallRun] so a full uninstall leaves nothing behind except the user's own data.
//
// Uses logf, not fmt.Println: called both as a standalone CLI command (logSink still
// defaults to os.Stdout at that point, so this prints the same either way) AND from
// watchLoop/superviseLoop/trayMain AFTER setupFileLog has already redirected logSink to
// also write sync.log — the daemon call sites have no console anyone is watching (a hidden
// WScript-launched autostart process), so a cleanup error routed only to fmt.Println would
// be silently lost exactly like the pre-fix probe/cdpSmoke failures earlier in this round.
//
// KNOWN RESIDUAL GAP (deliberately deferred, same class as the single-instance guard this
// codebase already documents as an open design question — go-reviewer/security-reviewer,
// 2026-07-18): processAlive only checks whether the DIRECTORY NAME's PID (the
// schroedinger-sync.exe process that created it) is still alive — not whether an orphaned
// Chrome CHILD process outlived a hard-killed parent and is still using that same
// directory under a different PID (os/exec does not tie child lifetime to parent via a Job
// Object here, so this is possible on a crash/SIGKILL — the exact scenario chromeProfileDir
// exists to clean up after in the first place). Closing this fully would mean scanning for
// chrome.exe processes whose --user-data-dir argument matches the directory in question
// (similar in shape to processPathContains, but enumerating all matching processes rather
// than checking one fixed image name) — real design work, not a quick fix, deferred rather
// than rushed.
func cleanupTemp() {
	removed, errN := 0, 0
	sweep := func(dir string) {
		existed, err := removeResidueTree(dir)
		if err != nil {
			errN++
			logf("cleanup-temp: %s: %v", dir, err)
			return
		}
		if existed {
			removed++
			logf("cleanup-temp: removed %s", dir)
		}
	}

	chromeProfileParent := filepath.Join(os.Getenv("LOCALAPPDATA"), "SchroedingerSync", "chrome-profile")
	entries, err := os.ReadDir(chromeProfileParent) // #nosec G304 G703 -- fixed %LOCALAPPDATA% path, not variable input
	if err != nil && !os.IsNotExist(err) {
		errN++
		logf("cleanup-temp: %s: %v", chromeProfileParent, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, perr := strconv.Atoi(e.Name())
		// Directory names under chrome-profile are always exactly strconv.Itoa(os.Getpid())
		// (chromeProfileDir, cdp.go), i.e. always in-range for uint32 -- but e.Name() is
		// whatever's actually on disk, not something this function controls, so a
		// negative or too-large parse (a directory this tool never created) is rejected
		// explicitly rather than silently wrapping through a bare uint32(pid) conversion.
		if perr != nil || pid < 0 || pid > math.MaxUint32 {
			continue // not a valid PID-named directory this tool would have created -- leave alone, don't guess
		}
		if processAlive(uint32(pid)) {
			continue // some instance's live Chrome may still have this profile open
		}
		sweep(filepath.Join(chromeProfileParent, e.Name()))
	}

	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "schroedinger_*")) // #nosec G304 -- fixed glob pattern under os.TempDir(), not variable input
	for _, m := range matches {
		fi, statErr := os.Stat(m)
		if statErr != nil || time.Since(fi.ModTime()) < residueMinAge {
			continue // too young to be sure it isn't a concurrently-running instance's own in-flight copyCookieDB call
		}
		sweep(m)
	}

	logf("cleanup-temp: %d location(s) removed, %d error(s)", removed, errN)
}

// processAlive reports whether pid currently belongs to a running process. Used to decide
// whether a chrome-profile subdirectory is safe to remove (see cleanupTemp) — PID reuse
// means a directory named "12345" could belong to today's brand new process even if
// schroedinger-sync last used PID 12345 weeks ago, so this must check liveness at the
// moment of cleanup, not just treat every non-self PID as stale.
//
// Conflates "process genuinely gone" with "OpenProcess/GetExitCodeProcess failed for some
// other reason" into a single false — reviewed and accepted (2026-07-18): the failure
// direction is the safe one for this caller (skip a directory rather than delete a live
// one), and PROCESS_QUERY_LIMITED_INFORMATION is cheap enough that a transient failure
// would require genuine system-level resource exhaustion to trigger.
func processAlive(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE (windows.h) -- x/sys/windows has no named constant for this
}

// removeResidueTree removes dir if it exists, refusing to touch a reparse point at dir
// itself for the same reason sweepProfileDir does (cdp.go, isReparsePoint) — every caller
// here passes a fully predictable %LOCALAPPDATA%/%TEMP% path, so refusing a reparse point
// means a future change can't silently turn "predictable path" into "something else got
// deleted/followed instead." Returns whether dir existed (regardless of removal outcome).
//
// The existence check uses os.Lstat, not os.Stat: os.Stat follows a reparse point to its
// target, so for a DANGLING junction (target already deleted) it fails not-exist-shaped —
// which would make this function silently report "nothing here" and return before ever
// reaching the isReparsePoint refusal below, exactly the case that refusal exists for.
// os.Lstat matches isReparsePoint's own non-following GetFileAttributes semantics, so a
// dangling junction is still correctly detected and refused, not skipped.
func removeResidueTree(dir string) (existed bool, err error) {
	if _, statErr := os.Lstat(dir); os.IsNotExist(statErr) { // #nosec G304 G703 -- dir is always a fixed %LOCALAPPDATA%/%TEMP% path in production; only tests pass another value, from t.TempDir()
		return false, nil
	}
	if isReparsePoint(dir) {
		return true, fmt.Errorf("refusing to remove %s — it's a reparse point/symlink, not a plain directory", dir)
	}
	return true, os.RemoveAll(dir) // #nosec G304 G703 -- dir is always a fixed %LOCALAPPDATA%/%TEMP% path with this tool's own literal prefix, not variable input
}
