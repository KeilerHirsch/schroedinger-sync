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
	"strconv"
	"testing"
)

// TestChromeProfileDirIsPIDScoped proves the fix for the go-reviewer HIGH finding
// (2026-07-18): chromeProfileDir must be unique per running process, not a single
// machine-wide path, or one instance's sweepChromeProfile could RemoveAll a profile
// another concurrently-running instance's live Chrome still has open (supervise autostart
// + a manually-launched tray/watch/one-shot command is an explicitly supported combination
// per README).
func TestChromeProfileDirIsPIDScoped(t *testing.T) {
	dir := chromeProfileDir()
	want := strconv.Itoa(os.Getpid())
	if filepath.Base(dir) != want {
		t.Errorf("chromeProfileDir() = %q, want a path ending in this process's PID %q", dir, want)
	}
}

// TestSweepProfileDirRefusesReparsePoint proves sweepProfileDir does not recurse into (or
// delete through) a reparse point at the target path. Uses a real NTFS directory junction
// (mklink /J, no elevation required) against a t.TempDir() fixture -- not
// chromeProfileDir()'s real %LOCALAPPDATA% path -- so this asserts actual Windows
// filesystem behavior rather than reasoning about os.RemoveAll semantics from memory.
func TestSweepProfileDirRefusesReparsePoint(t *testing.T) {
	if _, err := exec.LookPath("cmd"); err != nil {
		t.Skip("cmd.exe not available to create a junction (mklink /J)")
	}

	root := t.TempDir()
	real := filepath.Join(root, "real-target")
	if err := os.MkdirAll(real, 0o750); err != nil {
		t.Fatalf("MkdirAll real target: %v", err)
	}
	sentinel := filepath.Join(real, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("must survive the sweep"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	junction := filepath.Join(root, "junction")
	// #nosec G204 -- real/junction are both t.TempDir()-derived paths in this test, never external input
	out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, real).CombinedOutput()
	if err != nil {
		t.Skipf("could not create junction (mklink /J): %v: %s", err, out)
	}

	sweepProfileDir(junction)

	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel file did not survive sweepProfileDir(junction): %v -- reparse point was followed/removed through instead of refused", err)
	}
	if _, err := os.Lstat(junction); err != nil {
		t.Errorf("junction itself should still exist (refused, not removed): %v", err)
	}
}

// TestSweepProfileDirRemovesPlainDirectory proves the refusal in
// TestSweepProfileDirRefusesReparsePoint is specific to reparse points -- an ordinary
// directory at the swept path is still removed normally.
func TestSweepProfileDirRemovesPlainDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "leftover.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	sweepProfileDir(dir)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("sweepProfileDir(plain dir) left %s behind (err=%v), want it removed", dir, err)
	}
}
