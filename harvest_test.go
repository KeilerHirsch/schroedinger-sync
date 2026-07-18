// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// Tests for the fetch-dependent logic using an in-memory mock `get` function — no browser,
// no credentials, no network. Covers org resolution (including the Cloudflare-challenge and
// multi-org paths), the retry happy path, and the memory-surface harvest/parse/write.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveOrg(t *testing.T) {
	if org, err := resolveOrg(func(string) (string, error) { return `[{"uuid":"ORG1"}]`, nil }); err != nil || org != "ORG1" {
		t.Errorf("single org: got %q err %v", org, err)
	}
	// A Cloudflare "Just a moment" HTML challenge must be reported as an error, never
	// mis-parsed into an org id.
	if _, err := resolveOrg(func(string) (string, error) { return `<html>Just a moment...</html>`, nil }); err == nil {
		t.Error("Cloudflare challenge body should error")
	}
	if _, err := resolveOrg(func(string) (string, error) { return `[]`, nil }); err == nil {
		t.Error("empty org list should error")
	}
	// Multi-org accounts harvest the first org (and log a warning); assert the returned id.
	if org, err := resolveOrg(func(string) (string, error) { return `[{"uuid":"A"},{"uuid":"B"}]`, nil }); err != nil || org != "A" {
		t.Errorf("multi-org: got %q err %v", org, err)
	}
}

func TestGetWithRetryReturnsBodyWhenOK(t *testing.T) {
	body, err := getWithRetry(func(string) (string, error) { return "OK-BODY", nil }, "/x", 3)
	if err != nil || body != "OK-BODY" {
		t.Errorf("got %q err %v", body, err)
	}
}

func TestHarvestMemory(t *testing.T) {
	dir := t.TempDir()
	get := func(string) (string, error) { return `{"memory":"# my memory blob"}`, nil }
	if _, err := harvestMemory(get, "ORG", dir); err != nil {
		t.Fatalf("harvestMemory: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "claude-ai-memory.md"))
	if err != nil {
		t.Fatalf("memory file not written: %v", err)
	}
	if !strings.Contains(string(b), "my memory blob") {
		t.Errorf("memory content missing: %s", b)
	}
	// An empty memory blob is a legitimate account state -> a no-op (nil), not an error
	// (M4 fix; TestHarvestMemoryEmptyIsNoop asserts it also writes nothing).
	if _, err := harvestMemory(func(string) (string, error) { return `{"memory":""}`, nil }, "ORG", dir); err != nil {
		t.Errorf("empty memory must be a no-op, got err %v", err)
	}
}

// writeFixtureFile writes an on-disk export for c that fileIsCurrent/fileConvUpdatedAt
// will recognize as CURRENT (header matches c.UpdatedAt, size clears minFileSizeBytes) —
// the same fixture shape TestFileIsCurrent (harden_test.go) already relies on.
func writeFixtureFile(t *testing.T, dir string, c convSummary) {
	t.Helper()
	fname := convFilename(dir, c)
	body := "# " + c.Name + "\n\n- Created: " + trunc(c.CreatedAt, 19) + "\n- Updated: " + trunc(c.UpdatedAt, 19) +
		"\n- Model: claude\n\n---\n\n## Human [x]\n\nfixture body long enough to clear the minFileSizeBytes threshold for this test\n"
	if err := os.WriteFile(fname, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSyncConversations exercises the M3 testability fix: the per-conversation decision
// loop pulled out of harvestOnce into its own function specifically so it could be driven
// with an in-memory `get` mock and a fixture []convSummary instead of a live browser. Covers
// all three convAction outcomes reaching syncConversations end to end (a real fetch actually
// happening for actionFetch, a real skip for actionSkip, a real seed-without-fetch for
// actionSeed) plus the per-item error path — the exact loop wiring go-reviewer flagged as
// previously untested (M3).
func TestSyncConversations(t *testing.T) {
	dir := t.TempDir()

	cNew := convSummary{UUID: "uuid-new", Name: "New Conv", CreatedAt: "2026-07-15T09:00:00Z", UpdatedAt: "2026-07-15T10:00:00.000000Z"}
	cSkip := convSummary{UUID: "uuid-skip", Name: "Skip Conv", CreatedAt: "2026-07-10T09:00:00Z", UpdatedAt: "2026-07-10T10:00:00.000000Z"}
	cChanged := convSummary{UUID: "uuid-changed", Name: "Changed Conv", CreatedAt: "2026-07-14T09:00:00Z", UpdatedAt: "2026-07-16T10:00:00.000000Z"}
	cSeed := convSummary{UUID: "uuid-seed", Name: "Seed Conv", CreatedAt: "2026-07-12T09:00:00Z", UpdatedAt: "2026-07-12T10:00:00.000000Z"}
	cErr := convSummary{UUID: "uuid-err", Name: "Err Conv", CreatedAt: "2026-07-11T09:00:00Z", UpdatedAt: "2026-07-11T10:00:00.000000Z"}

	// cSkip: seen + state matches + on-disk header matches -> must be skipped, no fetch.
	// cSeed: NOT seen, but an M2 file already on disk matching the current server version ->
	//   must be seeded (state recorded) without a fetch.
	writeFixtureFile(t, dir, cSkip)
	writeFixtureFile(t, dir, cSeed)

	s := &syncState{Conversations: map[string]string{
		"uuid-skip":    cSkip.UpdatedAt,
		"uuid-changed": "2026-07-14T10:00:00.000000Z", // seen, but server has since moved on
	}}

	fetched := map[string]int{}
	get := func(path string) (string, error) {
		switch {
		case strings.Contains(path, "uuid-skip"), strings.Contains(path, "uuid-seed"):
			t.Fatalf("fetch must not happen for an actionSkip/actionSeed conversation: %s", path)
			return "", nil // unreachable (t.Fatalf calls runtime.Goexit) — satisfies the compiler's missing-return check
		case strings.Contains(path, "uuid-new"):
			fetched["uuid-new"]++
			return `{"name":"New Conv","created_at":"2026-07-15T09:00:00Z","updated_at":"2026-07-15T10:00:00.000000Z","chat_messages":[{"sender":"human","text":"hi"}]}`, nil
		case strings.Contains(path, "uuid-changed"):
			fetched["uuid-changed"]++
			return `{"name":"Changed Conv","created_at":"2026-07-14T09:00:00Z","updated_at":"2026-07-16T10:00:00.000000Z","chat_messages":[{"sender":"human","text":"updated"}]}`, nil
		case strings.Contains(path, "uuid-err"):
			return "", errors.New("network boom")
		default:
			t.Fatalf("unexpected fetch path: %s", path)
			return "", nil
		}
	}

	all := []convSummary{cNew, cSkip, cChanged, cSeed, cErr}
	newN, changedN, seedN, errN, err := syncConversations(get, "ORG", dir, s, all, time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if newN != 1 {
		t.Errorf("newN = %d, want 1 (cNew)", newN)
	}
	if changedN != 1 {
		t.Errorf("changedN = %d, want 1 (cChanged)", changedN)
	}
	if seedN != 1 {
		t.Errorf("seedN = %d, want 1 (cSeed)", seedN)
	}
	if errN != 1 {
		t.Errorf("errN = %d, want 1 (cErr)", errN)
	}
	if fetched["uuid-new"] != 1 || fetched["uuid-changed"] != 1 {
		t.Errorf("fetch call counts = %+v, want exactly one fetch each for uuid-new/uuid-changed", fetched)
	}

	// State must reflect the outcome: fetched/seeded conversations recorded at the new
	// server updated_at; the skipped one untouched.
	if s.Conversations["uuid-new"] != cNew.UpdatedAt {
		t.Errorf("uuid-new state = %q, want %q", s.Conversations["uuid-new"], cNew.UpdatedAt)
	}
	if s.Conversations["uuid-changed"] != cChanged.UpdatedAt {
		t.Errorf("uuid-changed state = %q, want %q", s.Conversations["uuid-changed"], cChanged.UpdatedAt)
	}
	if s.Conversations["uuid-seed"] != cSeed.UpdatedAt {
		t.Errorf("uuid-seed state = %q, want %q (seeded without a fetch)", s.Conversations["uuid-seed"], cSeed.UpdatedAt)
	}
	if s.Conversations["uuid-skip"] != cSkip.UpdatedAt {
		t.Errorf("uuid-skip state changed unexpectedly: %q", s.Conversations["uuid-skip"])
	}

	// A fetched conversation must actually have been written to disk.
	if b, e := os.ReadFile(convFilename(dir, cNew)); e != nil || !strings.Contains(string(b), "hi") {
		t.Errorf("cNew was not written to disk correctly: err=%v content=%s", e, b)
	}
}

// TestSyncConversationsDeadlineExceededAborts proves a mid-harvest session timeout aborts
// the whole loop with a hard, wrapped error instead of quietly treating the unreached tail
// as per-item errors — runCycle relies on errors.Is(err, context.DeadlineExceeded) (via
// cookieWatermark) to NOT advance the sync watermark for a genuinely incomplete cycle.
func TestSyncConversationsDeadlineExceededAborts(t *testing.T) {
	dir := t.TempDir()
	get := func(string) (string, error) {
		return "", fmt.Errorf("session timed out: %w", context.DeadlineExceeded)
	}
	all := []convSummary{
		{UUID: "u1", Name: "A", CreatedAt: "2026-07-01T00:00:00Z", UpdatedAt: "2026-07-01T00:00:00.000000Z"},
		{UUID: "u2", Name: "B", CreatedAt: "2026-07-02T00:00:00Z", UpdatedAt: "2026-07-02T00:00:00.000000Z"},
	}
	s := &syncState{Conversations: map[string]string{}}
	_, _, _, _, err := syncConversations(get, "ORG", dir, s, all, time.Millisecond)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected an error wrapping context.DeadlineExceeded, got %v", err)
	}
	if _, seen := s.Conversations["u2"]; seen {
		t.Error("a conversation after the one that timed out must never be reached, let alone recorded")
	}
}
