// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// Tests for the write-side half of the MemPalace ingest handshake (README roadmap #1):
// every exported Markdown file gets a SHA-256 recorded in a per-outDir manifest at write
// time, so a future ingest-side re-hash can prove the bytes MemPalace mined are exactly
// the bytes this program wrote.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestHashContentMatchesStdlibSHA256(t *testing.T) {
	cases := [][]byte{
		[]byte(""),
		[]byte("hello"),
		[]byte("# Conversation\n\nsome markdown body\n"),
	}
	for _, c := range cases {
		sum := sha256.Sum256(c)
		want := hex.EncodeToString(sum[:])
		if got := hashContent(c); got != want {
			t.Errorf("hashContent(%q) = %q, want %q", c, got, want)
		}
	}
}

func TestWriteMarkdownRecordsHashInManifest(t *testing.T) {
	dir := t.TempDir()
	fname := filepath.Join(dir, "chat_abc123.md")
	content := []byte("# Test conversation\n\nbody\n")

	if ok, err := writeMarkdown(dir, fname, content); err != nil || !ok {
		t.Fatalf("writeMarkdown: ok=%v err=%v", ok, err)
	}

	if got, err := os.ReadFile(fname); err != nil || string(got) != string(content) {
		t.Fatalf("file content mismatch: got %q, err %v", got, err)
	}

	m := loadManifest(dir)
	want := hashContent(content)
	if got := m["chat_abc123.md"]; got != want {
		t.Fatalf("manifest[%q] = %q, want %q", "chat_abc123.md", got, want)
	}
}

func TestWriteMarkdownOverwriteUpdatesHash(t *testing.T) {
	dir := t.TempDir()
	fname := filepath.Join(dir, "chat.md")

	if _, err := writeMarkdown(dir, fname, []byte("version one")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := writeMarkdown(dir, fname, []byte("version two, longer content")); err != nil {
		t.Fatalf("second write: %v", err)
	}

	m := loadManifest(dir)
	want := hashContent([]byte("version two, longer content"))
	if got := m["chat.md"]; got != want {
		t.Fatalf("manifest not updated after overwrite: got %q, want %q", got, want)
	}
}

// TestWriteMarkdownManifestSurvivesRestart proves the per-file load/save (not a
// batched save-at-the-end) is what makes the manifest crash-safe: simulating two
// separate process lifetimes (fresh loadManifest each time, as a real restart would)
// must not lose the first write's entry.
func TestWriteMarkdownManifestSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	if _, err := writeMarkdown(dir, filepath.Join(dir, "a.md"), []byte("A")); err != nil {
		t.Fatalf("write a.md: %v", err)
	}
	// Simulate a crash/restart between writes: nothing here carries in-memory state
	// forward except what writeMarkdown itself persisted.
	if _, err := writeMarkdown(dir, filepath.Join(dir, "b.md"), []byte("B")); err != nil {
		t.Fatalf("write b.md: %v", err)
	}

	m := loadManifest(dir)
	if m["a.md"] != hashContent([]byte("A")) {
		t.Errorf("a.md hash lost after simulated restart: %v", m)
	}
	if m["b.md"] != hashContent([]byte("B")) {
		t.Errorf("b.md hash missing: %v", m)
	}
}

// TestWriteMarkdownReportsManifestFailureWithoutFailingTheWrite proves the go-reviewer's
// MEDIUM finding is addressed: a manifest-save failure must surface via the manifestOK
// return value (so a caller can count it), but must NOT be reported as if the export
// itself failed -- the Markdown file is already safely on disk by the time saveManifest
// runs. Failure is induced deterministically (no OS-specific mocking): pre-creating the
// manifest path AS A DIRECTORY makes saveManifest's final os.Rename fail (you cannot
// rename a file onto an existing directory), while outDir itself stays fully writable so
// the real content file write is unaffected.
func TestWriteMarkdownReportsManifestFailureWithoutFailingTheWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(manifestPath(dir), 0o750); err != nil {
		t.Fatalf("seed manifest-path-is-a-directory: %v", err)
	}

	fname := filepath.Join(dir, "chat.md")
	ok, err := writeMarkdown(dir, fname, []byte("body"))

	if err != nil {
		t.Fatalf("writeMarkdown must not fail the export over a manifest error, got: %v", err)
	}
	if ok {
		t.Fatal("expected manifestOK=false when the manifest path is unwritable")
	}
	if got, rerr := os.ReadFile(fname); rerr != nil || string(got) != "body" {
		t.Fatalf("content file must still be written correctly: got %q, err %v", got, rerr)
	}
}

func TestLoadManifestCorruptFallsBackToEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(manifestPath(dir), []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("seed corrupt manifest: %v", err)
	}

	m := loadManifest(dir)
	if len(m) != 0 {
		t.Fatalf("expected empty manifest on corrupt file, got %v", m)
	}
}

func TestLoadManifestMissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	m := loadManifest(dir)
	if m == nil || len(m) != 0 {
		t.Fatalf("expected empty (non-nil) manifest for first run, got %v", m)
	}
}
