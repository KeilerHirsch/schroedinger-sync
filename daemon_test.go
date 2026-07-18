// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestVbsLauncherContent proves the hand-rolled VBScript quote-escaping in
// vbsLauncherContent produces a launcher that WScript.Shell.Run would actually execute
// as the intended command line — not just "looks plausible on inspection", which is
// exactly how an earlier draft of this function shipped a bug (it used Go's %q instead
// of VBScript's own "" quote-doubling convention, which mangles backslashes in Windows
// paths instead of escaping quotes).
func TestVbsLauncherContent(t *testing.T) {
	cases := []struct {
		name                string
		exe, subcmd, outDir string
		wantCmd             string // the command line WScript.Shell.Run should actually receive
	}{
		{
			name:    "no outDir",
			exe:     `C:\Program Files\schroedinger-sync.exe`,
			subcmd:  "supervise",
			wantCmd: `"C:\Program Files\schroedinger-sync.exe" supervise`,
		},
		{
			name:    "with outDir containing a space",
			exe:     `C:\Program Files\schroedinger-sync.exe`,
			subcmd:  "supervise",
			outDir:  `C:\Users\Test\My Data\desktop-chats`,
			wantCmd: `"C:\Program Files\schroedinger-sync.exe" supervise "C:\Users\Test\My Data\desktop-chats"`,
		},
		{
			// Proves the fix for the VBScript-injection bug: an outDir containing a
			// literal " must round-trip as data, not terminate the quoted argument early
			// and splice extra VBScript into the generated (unattended, logon-run) file.
			name:    "with outDir containing an embedded quote",
			exe:     `C:\Program Files\schroedinger-sync.exe`,
			subcmd:  "supervise",
			outDir:  `C:\Users\Test\quo"ted\desktop-chats`,
			wantCmd: `"C:\Program Files\schroedinger-sync.exe" supervise "C:\Users\Test\quo"ted\desktop-chats"`,
		},
		{
			name:    "tray subcommand still supported (manual, non-autostart use)",
			exe:     `C:\Program Files\schroedinger-sync.exe`,
			subcmd:  "tray",
			wantCmd: `"C:\Program Files\schroedinger-sync.exe" tray`,
		},
	}
	runRe := regexp.MustCompile(`\.Run "(.*)", 0, False`)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content := vbsLauncherContent(c.exe, c.subcmd, c.outDir)
			m := runRe.FindStringSubmatch(content)
			if m == nil {
				t.Fatalf("could not find a .Run(...) call in generated VBS:\n%s", content)
			}
			// VBScript de-escapes a doubled quote ("") to a single literal quote when it
			// parses a string literal — this mirrors exactly what the interpreter does,
			// giving us the actual argument .Run() would receive at runtime.
			gotCmd := strings.ReplaceAll(m[1], `""`, `"`)
			if gotCmd != c.wantCmd {
				t.Fatalf("Run() would execute %q, want %q", gotCmd, c.wantCmd)
			}
		})
	}
}

// TestNextSuperviseState proves supervise's start/stop hysteresis: active the instant
// either Desktop or VS Code is seen, but staying active through up to
// superviseGraceOffPolls-1 consecutive "neither seen" polls before actually going idle —
// so a brief gap (VS Code restarting, a Desktop auto-update relaunch) doesn't prematurely
// pause right before the next legitimate sync window.
func TestNextSuperviseState(t *testing.T) {
	// Either signal present -> immediately active, miss count reset regardless of prior state.
	if active, miss := nextSuperviseState(true, false, false, 2); !active || miss != 0 {
		t.Errorf("desktop up from idle: active=%v miss=%d, want true/0", active, miss)
	}
	if active, miss := nextSuperviseState(false, true, false, 0); !active || miss != 0 {
		t.Errorf("vscode up from idle: active=%v miss=%d, want true/0", active, miss)
	}
	// Was idle, neither running -> stays idle (never "wakes up" on its own).
	if active, miss := nextSuperviseState(false, false, false, 0); active || miss != 0 {
		t.Errorf("neither up, already idle: active=%v miss=%d, want false/0", active, miss)
	}
	// Was active, neither running now -> stays active through the grace window, counting misses.
	if active, miss := nextSuperviseState(false, false, true, 0); !active || miss != 1 {
		t.Errorf("first miss while active: active=%v miss=%d, want true/1", active, miss)
	}
	if active, miss := nextSuperviseState(false, false, true, 1); !active || miss != 2 {
		t.Errorf("second miss while active: active=%v miss=%d, want true/2", active, miss)
	}
	// Reaching the grace threshold -> actually goes idle, miss count resets.
	if active, miss := nextSuperviseState(false, false, true, superviseGraceOffPolls-1); active || miss != 0 {
		t.Errorf("miss count reaching grace threshold: active=%v miss=%d, want false/0", active, miss)
	}
}

// TestOutDirHasLineBreak proves installTask's line-break guard actually rejects the input
// class it exists for. A `"` is already handled by vbsLauncherContent's quote-doubling
// (TestVbsLauncherContent above); a raw CR/LF is a different, sharper problem — it would
// terminate the generated VBScript STATEMENT itself, not just the quoted argument, and
// splice whatever follows as new code into a file Windows runs unattended at every logon.
func TestOutDirHasLineBreak(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"ordinary path", `C:\Users\Test\My Data\desktop-chats`, false},
		{"path with embedded quote (handled elsewhere, not here)", `C:\Users\Test\quo"ted`, false},
		{"embedded CRLF", "C:\\Users\\Test\\desktop-chats\r\nWScript.Shell.Run \"calc.exe\"", true},
		{"embedded bare LF", "C:\\Users\\Test\\desktop-chats\ninjected", true},
		{"embedded bare CR", "C:\\Users\\Test\\desktop-chats\rinjected", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := outDirHasLineBreak(c.in); got != c.want {
				t.Errorf("outDirHasLineBreak(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestRemoveResidueTreeMissingIsNotAnError proves cleanupTemp's sweep of a location that
// simply doesn't exist (the common case -- no crash residue to find) reports existed=false
// with no error, rather than treating "nothing to clean up" as a failure.
func TestRemoveResidueTreeMissingIsNotAnError(t *testing.T) {
	existed, err := removeResidueTree(filepath.Join(t.TempDir(), "never-created"))
	if existed {
		t.Error("existed = true for a path that was never created")
	}
	if err != nil {
		t.Errorf("err = %v, want nil -- a missing location is not a cleanup failure", err)
	}
}

// TestRemoveResidueTreeRemovesPlainDirectory proves the normal case: an ordinary leftover
// directory (e.g. a crashed process's chrome-profile\<old-PID> or a copyCookieDB temp dir)
// is actually removed.
func TestRemoveResidueTreeRemovesPlainDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "leftover")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Cookies"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	existed, err := removeResidueTree(dir)
	if !existed {
		t.Error("existed = false for a directory that was actually there")
	}
	if err != nil {
		t.Errorf("unexpected err: %v", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("removeResidueTree left %s behind (statErr=%v)", dir, statErr)
	}
}

// TestRemoveResidueTreeRefusesReparsePoint proves removeResidueTree shares
// sweepProfileDir's reparse-point refusal (cdp.go's isReparsePoint) -- same guard, same
// reasoning: chromeProfileDir's parent and copyCookieDB's %TEMP% path are both fully
// predictable, so a reparse point sitting at that exact path should be refused, not
// silently unlinked. Uses a real NTFS junction (mklink /J, no elevation required).
func TestRemoveResidueTreeRefusesReparsePoint(t *testing.T) {
	if _, err := exec.LookPath("cmd"); err != nil {
		t.Skip("cmd.exe not available to create a junction (mklink /J)")
	}

	root := t.TempDir()
	real := filepath.Join(root, "real-target")
	if err := os.MkdirAll(real, 0o750); err != nil {
		t.Fatalf("MkdirAll real target: %v", err)
	}
	sentinel := filepath.Join(real, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("must survive"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	junction := filepath.Join(root, "chrome-profile")
	// #nosec G204 -- real/junction are both t.TempDir()-derived paths in this test, never external input
	out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, real).CombinedOutput()
	if err != nil {
		t.Skipf("could not create junction (mklink /J): %v: %s", err, out)
	}

	existed, rerr := removeResidueTree(junction)
	if !existed {
		t.Error("existed = false for a junction that was actually there")
	}
	if rerr == nil {
		t.Error("expected an error refusing to remove a reparse point, got nil")
	}
	if _, statErr := os.Lstat(junction); statErr != nil {
		t.Errorf("junction itself should still exist (refused, not removed): %v", statErr)
	}
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Errorf("sentinel did not survive: %v", statErr)
	}
}

// TestProcessAliveDetectsSelfAndBogusPID proves the fix for the concurrency bug cleanupTemp
// would otherwise have (a blind RemoveAll of the whole chrome-profile tree could corrupt a
// STILL-RUNNING instance's live Chrome profile, since Chromium refuses FILE_SHARE_* on some
// of its own files while running -- see cleanupTemp's doc comment). processAlive is what
// lets cleanupTemp tell "this PID's process is gone, safe to sweep" apart from "some
// instance -- maybe this very test binary -- is still using it".
func TestProcessAliveDetectsSelfAndBogusPID(t *testing.T) {
	self := uint32(os.Getpid())
	if !processAlive(self) {
		t.Errorf("processAlive(%d) = false for this test's own running PID, want true", self)
	}
	// Not a security-critical exact value -- just needs to be a PID essentially guaranteed
	// not to be alive on any real machine.
	const bogus = 0xFFFFFFF0
	if processAlive(bogus) {
		t.Errorf("processAlive(%#x) = true for a PID that should not exist, want false", bogus)
	}
}

// TestCleanupTempSweepsOnlyDeadAndOldEntries is the end-to-end assembled test both review
// agents flagged as missing (2026-07-18): unit tests for processAlive/removeResidueTree in
// isolation don't exercise cleanupTemp's own orchestration -- the exact logic path that had
// a HIGH concurrency regression (a blind whole-tree RemoveAll) hours earlier the same
// night. Redirects LOCALAPPDATA/TMP to t.TempDir() fixtures via t.Setenv (auto-restored)
// rather than touching the real user profile, then asserts: a chrome-profile subdirectory
// named after a dead PID is removed, one named after this TEST's own live PID survives, an
// old %TEMP%\schroedinger_* leftover is removed, and a fresh one survives.
func TestCleanupTempSweepsOnlyDeadAndOldEntries(t *testing.T) {
	localAppData := t.TempDir()
	tempRoot := t.TempDir()
	t.Setenv("LOCALAPPDATA", localAppData)
	t.Setenv("TMP", tempRoot)
	t.Setenv("TEMP", tempRoot)

	chromeProfileParent := filepath.Join(localAppData, "SchroedingerSync", "chrome-profile")
	deadPIDDir := filepath.Join(chromeProfileParent, "4294967280") // matches TestProcessAliveDetectsSelfAndBogusPID's bogus PID
	livePIDDir := filepath.Join(chromeProfileParent, strconv.Itoa(os.Getpid()))
	for _, d := range []string{deadPIDDir, livePIDDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("MkdirAll(%s): %v", d, err)
		}
	}

	oldTempDir := filepath.Join(tempRoot, "schroedinger_old12345")
	freshTempDir := filepath.Join(tempRoot, "schroedinger_fresh6789")
	for _, d := range []string{oldTempDir, freshTempDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("MkdirAll(%s): %v", d, err)
		}
	}
	oldTime := time.Now().Add(-2 * residueMinAge)
	if err := os.Chtimes(oldTempDir, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	cleanupTemp()

	if _, err := os.Stat(deadPIDDir); !os.IsNotExist(err) {
		t.Errorf("dead-PID chrome-profile dir %s survived cleanupTemp (err=%v), want removed", deadPIDDir, err)
	}
	if _, err := os.Stat(livePIDDir); err != nil {
		t.Errorf("live-PID (this test's own PID) chrome-profile dir %s did not survive: %v", livePIDDir, err)
	}
	if _, err := os.Stat(oldTempDir); !os.IsNotExist(err) {
		t.Errorf("old %%TEMP%% leftover %s survived cleanupTemp (err=%v), want removed", oldTempDir, err)
	}
	if _, err := os.Stat(freshTempDir); err != nil {
		t.Errorf("fresh %%TEMP%% dir %s did not survive (too young to sweep): %v", freshTempDir, err)
	}
}
