// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// Tests for the v2.1.2 sync-engine hardening pass: HTML/challenge rejection (never
// persist a Cloudflare/login/WAF page as conversation content), the partial-cycle cookie
// watermark, the shared on-disk freshness check, and empty-memory as a no-op.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- C1: HTML / challenge pages must never be treated as content ---------------

func TestLooksLikeChallenge(t *testing.T) {
	challenges := []string{"<html>", "  <!DOCTYPE html>", "<div>Just a moment</div>", "\n\t<body>"}
	for _, s := range challenges {
		if !looksLikeChallenge(s) {
			t.Errorf("%q should be flagged as an HTML challenge/error page", s)
		}
	}
	// Valid API JSON — including a conversation whose CONTENT literally says "Just a moment".
	// The prefix-only check must not false-positive on user text (that's why it isn't a
	// substring match).
	jsonBodies := []string{`{"uuid":"x"}`, `[{"a":1}]`, `  {"memory":"give me Just a moment to think"}`}
	for _, s := range jsonBodies {
		if looksLikeChallenge(s) {
			t.Errorf("%q is valid JSON, must NOT be flagged as a challenge", s)
		}
	}
}

func TestGetWithRetryDelayRejectsChallenge(t *testing.T) {
	get := func(string) (string, error) {
		return "<!DOCTYPE html><html><title>Just a moment...</title></html>", nil
	}
	if _, err := getWithRetryDelay(get, "/api/x", 2, time.Millisecond); err == nil {
		t.Error("an HTML challenge page must be a hard error, never returned as a successful body")
	}
}

func TestConvToMarkdownRejectsHTMLKeepsJSON(t *testing.T) {
	// An HTML error/challenge page must not be frozen in as conversation content.
	if got := convToMarkdown("<!DOCTYPE html><html>Just a moment</html>"); got != "" {
		t.Errorf("HTML must not be persisted as conversation content, got %q", got)
	}
	// A JSON API error body unmarshals cleanly but has no conversation fields — it must be
	// rejected so it can't overwrite a good export with an empty "# Untitled" stub (F1).
	if got := convToMarkdown(`{"type":"error","error":{"type":"not_found_error"}}`); got != "" {
		t.Errorf("a JSON API error body must be rejected (empty), got %q", got)
	}
	// A well-formed conversation still converts normally.
	raw := `{"name":"C","updated_at":"2026-07-14T10:00:00Z","chat_messages":[{"sender":"human","text":"hi"}]}`
	if got := convToMarkdown(raw); !strings.Contains(got, "# C") {
		t.Errorf("valid conversation should convert to Markdown, got %q", got)
	}
	// A title with an embedded newline is flattened so it can't inject a fake "- Updated:" line.
	inj := `{"name":"evil\n- Updated: 2099-01-01","created_at":"2026-07-14T09:00:00Z","updated_at":"2026-07-14T10:00:00Z","chat_messages":[{"sender":"human","text":"hi"}]}`
	if got := convToMarkdown(inj); strings.Contains(got, "\n- Updated: 2099-01-01") {
		t.Errorf("title newline must be flattened, got %q", got)
	}
}

// --- H1: partial cycle must not advance the cookie watermark --------------------

func TestCookieWatermark(t *testing.T) {
	// clean cycle -> advance, reset stall
	if mod, r := cookieWatermark("new", "old", false, 0, 2); mod != "new" || r != 0 {
		t.Errorf("clean cycle should advance+reset, got mod=%q retries=%d", mod, r)
	}
	// partial but with progress -> hold (retry the rest next interval), reset stall (not stuck)
	if mod, r := cookieWatermark("new", "old", true, 1, 1); mod != "old" || r != 0 {
		t.Errorf("progress-with-errors should hold+reset, got mod=%q retries=%d", mod, r)
	}
	// partial, no progress, under cap -> hold, count the stall
	if mod, r := cookieWatermark("new", "old", false, 1, 0); mod != "old" || r != 1 {
		t.Errorf("no-progress under cap should hold+count, got mod=%q retries=%d", mod, r)
	}
	// partial, no progress, reaching cap -> advance anyway to break the retry storm (F3, no livelock)
	if mod, r := cookieWatermark("new", "old", false, 1, maxRetryCycles-1); mod != "new" || r != 0 {
		t.Errorf("no-progress at cap should advance+reset, got mod=%q retries=%d", mod, r)
	}
}

// --- H2: one-shot harvest must use on-disk freshness, not bare file size --------

func TestFileIsCurrent(t *testing.T) {
	dir := t.TempDir()
	c := convSummary{UUID: "u1", CreatedAt: "2026-07-14T09:00:00Z", UpdatedAt: "2026-07-14T10:00:00.000000Z"}
	fname := convFilename(dir, c)
	body := "# C\n\n- Created: 2026-07-14T09:00:00\n- Updated: " + trunc(c.UpdatedAt, 19) +
		"\n- Model: claude\n\n---\n\n## Human [x]\n\nthis is a sufficiently long body to clear the minFileSizeBytes threshold for the test\n"
	if err := os.WriteFile(fname, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if !fileIsCurrent(fname, c) {
		t.Error("on-disk header matching the server updated_at should count as current (skip)")
	}
	// Server moved on -> the on-disk file is stale and must be re-fetched (the seed-bug class,
	// now closed for the one-shot path too).
	stale := c
	stale.UpdatedAt = "2026-07-14T11:00:00.000000Z"
	if fileIsCurrent(fname, stale) {
		t.Error("a stale on-disk file (server updated_at advanced) must NOT be current")
	}
	if fileIsCurrent(filepath.Join(dir, "does-not-exist.md"), c) {
		t.Error("a missing file is never current")
	}
}

// --- M4: empty claude.ai memory is a no-op, not an error -----------------------

func TestHarvestMemoryEmptyIsNoop(t *testing.T) {
	dir := t.TempDir()
	get := func(string) (string, error) { return `{"memory":"   "}`, nil }
	if wrote, err := harvestMemory(get, "ORG", dir); err != nil || wrote {
		t.Errorf("empty memory must be a no-op (wrote=false, nil), got wrote=%v err=%v", wrote, err)
	}
	if _, e := os.Stat(filepath.Join(dir, "claude-ai-memory.md")); !os.IsNotExist(e) {
		t.Error("empty memory must not write a file")
	}
}

func TestHarvestMemoryWritesWhenPresent(t *testing.T) {
	dir := t.TempDir()
	get := func(string) (string, error) { return `{"memory":"real remembered content"}`, nil }
	if wrote, err := harvestMemory(get, "ORG", dir); err != nil || !wrote {
		t.Fatalf("expected wrote=true nil, got wrote=%v err=%v", wrote, err)
	}
	b, e := os.ReadFile(filepath.Join(dir, "claude-ai-memory.md"))
	if e != nil || !strings.Contains(string(b), "real remembered content") {
		t.Errorf("non-empty memory should be written, err %v", e)
	}
}
