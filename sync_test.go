// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// Tests for the sync engine: the per-conversation decision state machine (the tool's most
// important logic, previously 0% covered), the shared list/filename primitives, and the
// retry/backoff/variant-fallback logic — all exercised with in-memory mocks and injected
// millisecond delays, no browser, no network, no real sleeps.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- convAction: the sync state machine, incl. the seed-bug regression -------

func TestConvActionSkipsWhenCurrent(t *testing.T) {
	c := convSummary{UUID: "u1", UpdatedAt: "2026-07-14T10:00:00.000000Z"}
	if got := convAction(c, c.UpdatedAt, true, true, trunc(c.UpdatedAt, 19)); got != actionSkip {
		t.Errorf("seen+known==updated+fileOK+header-match should skip, got %v", got)
	}
}

// TestConvActionRefetchesSeenPoisonedFile guards F2: a file recorded in state whose on-disk
// header does NOT match the server (e.g. a raw HTML/error page a pre-fix build wrote and then
// recorded) must re-fetch to heal, not be skipped forever on size alone.
func TestConvActionRefetchesSeenPoisonedFile(t *testing.T) {
	c := convSummary{UUID: "u1", UpdatedAt: "2026-07-14T10:00:00.000000Z"}
	if got := convAction(c, c.UpdatedAt, true, true, ""); got != actionFetch {
		t.Errorf("seen file with a missing/mismatched header must re-fetch (heal), got %v", got)
	}
}

func TestConvActionSeedsWhenFileMatchesServer(t *testing.T) {
	c := convSummary{UUID: "u1", UpdatedAt: "2026-07-14T10:00:00.000000Z"}
	// On the first daemon cycle (unseen) an M2 file whose header matches the server version
	// is trusted (seeded), not re-downloaded.
	if got := convAction(c, "", false, true, trunc(c.UpdatedAt, 19)); got != actionSeed {
		t.Errorf("unseen file matching server should seed, got %v", got)
	}
}

// TestConvActionRefetchesStaleM2File is the regression guard for the data-loss bug: a
// conversation harvested by the one-shot M2 run, then CHANGED on the server before the first
// daemon cycle, has an OLD on-disk header but a NEW server updated_at. The old code seeded it
// (recording the new timestamp against old content) and never re-fetched the delta. It must
// now FETCH.
func TestConvActionRefetchesStaleM2File(t *testing.T) {
	c := convSummary{UUID: "u1", UpdatedAt: "2026-07-14T10:00:00.000000Z"}
	staleHeader := "2026-07-13T09:00:00" // an earlier version than the server's UpdatedAt
	if got := convAction(c, "", false, true, staleHeader); got != actionFetch {
		t.Errorf("stale M2 file must be re-fetched (seed-bug regression), got %v", got)
	}
}

func TestConvActionFetchesWhenNoFile(t *testing.T) {
	c := convSummary{UUID: "u1", UpdatedAt: "2026-07-14T10:00:00.000000Z"}
	if got := convAction(c, "", false, false, ""); got != actionFetch {
		t.Errorf("missing file should fetch, got %v", got)
	}
}

func TestConvActionRefetchesWhenChanged(t *testing.T) {
	// Seen before, but the server updated_at moved on -> re-fetch (changed conversation).
	c := convSummary{UUID: "u1", UpdatedAt: "2026-07-14T11:00:00.000000Z"}
	if got := convAction(c, "2026-07-14T10:00:00.000000Z", true, true, "2026-07-14T10:00:00"); got != actionFetch {
		t.Errorf("changed conversation should re-fetch, got %v", got)
	}
}

// --- fileConvUpdatedAt --------------------------------------------------------

func TestFileConvUpdatedAt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "conv.md")
	body := "# My Chat\n\n- Created: 2026-07-14T09:00:00\n- Updated: 2026-07-14T10:00:00\n- Model: claude\n\n---\n\n## Human [x]\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fileConvUpdatedAt(p); got != "2026-07-14T10:00:00" {
		t.Errorf("got %q, want the Updated header value", got)
	}
	if got := fileConvUpdatedAt(filepath.Join(dir, "missing.md")); got != "" {
		t.Errorf("missing file should give empty string, got %q", got)
	}
	noHeader := filepath.Join(dir, "plain.md")
	if err := os.WriteFile(noHeader, []byte("just prose, no header\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fileConvUpdatedAt(noHeader); got != "" {
		t.Errorf("file without header should give empty string, got %q", got)
	}
}

// fileConvUpdatedAt must round-trip with what convToMarkdown actually writes.
func TestFileConvUpdatedAtRoundTripsWithConvToMarkdown(t *testing.T) {
	dir := t.TempDir()
	raw := `{"name":"C","created_at":"2026-07-14T09:00:00Z","updated_at":"2026-07-14T10:30:00Z","model":"claude","chat_messages":[{"sender":"human","created_at":"2026-07-14T09:00:00Z","text":"hi"}]}`
	p := filepath.Join(dir, "c.md")
	if err := os.WriteFile(p, []byte(convToMarkdown(raw)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fileConvUpdatedAt(p); got != trunc("2026-07-14T10:30:00Z", 19) {
		t.Errorf("got %q, want %q", got, trunc("2026-07-14T10:30:00Z", 19))
	}
}

// --- convFilename -------------------------------------------------------------

func TestConvFilename(t *testing.T) {
	c := convSummary{CreatedAt: "2026-07-14T09:00:00Z", UUID: "abcdef1234567890", Name: "My Chat: Draft?"}
	got := convFilename("/out", c)
	want := filepath.Join("/out", "2026-07-14_abcdef12_My_Chat_Draft.md")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- listAllConversations: hard error, never a silent partial list -----------

func TestListAllConversationsShortPage(t *testing.T) {
	get := func(string) (string, error) {
		return `[{"uuid":"a","updated_at":"T1"},{"uuid":"b","updated_at":"T2"}]`, nil
	}
	all, err := listAllConversations(get, "ORG")
	if err != nil || len(all) != 2 {
		t.Fatalf("got %d convs err %v", len(all), err)
	}
}

func TestListAllConversationsHardErrorsOnFetchFailure(t *testing.T) {
	get := func(string) (string, error) { return "", fmt.Errorf("network down") }
	if _, err := listAllConversations(get, "ORG"); err == nil {
		t.Error("a listing fetch failure must be a hard error, not a silent partial list")
	}
}

func TestListAllConversationsHardErrorsOnParseFailure(t *testing.T) {
	get := func(string) (string, error) { return "<html>not json</html>", nil }
	if _, err := listAllConversations(get, "ORG"); err == nil {
		t.Error("an unparseable list page must be a hard error")
	}
}

// --- retry / backoff / variant fallback (injected ms delays) -----------------

func TestGetWithRetryDelayImmediateOK(t *testing.T) {
	b, err := getWithRetryDelay(func(string) (string, error) { return "OK", nil }, "/x", 3, time.Millisecond)
	if err != nil || b != "OK" {
		t.Errorf("got %q err %v", b, err)
	}
}

func TestGetWithRetryDelayRetriesThenSucceeds(t *testing.T) {
	calls := 0
	get := func(string) (string, error) {
		calls++
		if calls < 3 {
			return `{"type":"rate_limit_error"}`, nil
		}
		return "OK", nil
	}
	b, err := getWithRetryDelay(get, "/x", 5, time.Millisecond)
	if err != nil || b != "OK" || calls != 3 {
		t.Errorf("got %q err %v after %d calls", b, err, calls)
	}
}

func TestGetWithRetryDelayExhaustsToSentinel(t *testing.T) {
	get := func(string) (string, error) { return "rate_limit_error", nil }
	_, err := getWithRetryDelay(get, "/x", 2, time.Millisecond)
	if !errors.Is(err, errRateLimited) {
		t.Errorf("exhausted retries should wrap errRateLimited, got %v", err)
	}
}

func TestFetchConvBodyDelayFallsBackToLighterVariant(t *testing.T) {
	// The full-fat first variant persistently rate-limits; the lighter variants succeed.
	get := func(path string) (string, error) {
		if strings.Contains(path, "render_all_tools=true") {
			return "rate_limit_error", nil
		}
		return "FULLBODY", nil
	}
	b, err := fetchConvBodyDelay(get, "ORG", "u1", time.Millisecond, time.Millisecond)
	if err != nil || b != "FULLBODY" {
		t.Errorf("expected fallback to succeed, got %q err %v", b, err)
	}
}

func TestFetchConvBodyDelayReturnsImmediatelyOnNonRateLimit(t *testing.T) {
	calls := 0
	get := func(string) (string, error) { calls++; return "", fmt.Errorf("network") }
	_, err := fetchConvBodyDelay(get, "ORG", "u1", time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("a non-rate-limit error must propagate, not trigger the variant fallback")
	}
	if calls != 1 {
		t.Errorf("non-rate-limit error should not try lighter variants, but get was called %d times", calls)
	}
}
