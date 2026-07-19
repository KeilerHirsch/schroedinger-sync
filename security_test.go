// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// Proves the SECURITY.md invariants hold in code, not just in prose:
//  1. headless is a hardcoded constant, never an overridable flag/env var.
//  2. every network destination resolves to claude.ai — no exfiltration path exists.
//  3. a registered secret never survives redact().
//
// Run with: go test ./...
package main

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRedactionScrubsRegisteredSecret(t *testing.T) {
	secretsMu.Lock()
	saved := secrets
	secrets = nil
	secretsMu.Unlock()
	defer func() {
		secretsMu.Lock()
		secrets = saved
		secretsMu.Unlock()
	}()

	fake := "sk-ant-THIS-IS-A-TEST-VALUE-NOT-REAL"
	RegisterSecret(fake)

	line := "sessionKey via DPAPI: OK (value=" + fake + ")"
	got := redact(line)
	if strings.Contains(got, fake) {
		t.Fatalf("redact() failed to scrub a registered secret: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("redact() did not insert the placeholder: %q", got)
	}
}

// TestStdoutRedactorSurvivesSplitWrites proves the fix for the chunk-boundary bug: a
// secret written across several separate os.Stdout writes (as real code might do, e.g.
// one Print for a label and another for the value) must still be fully redacted — it
// must never be possible for half the secret to slip through in one write and the
// other half in the next.
func TestStdoutRedactorSurvivesSplitWrites(t *testing.T) {
	secretsMu.Lock()
	saved := secrets
	secrets = nil
	secretsMu.Unlock()
	defer func() {
		secretsMu.Lock()
		secrets = saved
		secretsMu.Unlock()
	}()

	fake := "sk-ant-SPLIT-WRITE-TEST-VALUE-0123456789"
	RegisterSecret(fake)

	// installStdoutRedactor captures whatever os.Stdout is AT CALL TIME as its "real"
	// output target — substitute our own pipe first so we can inspect what it writes.
	capR, capW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	realStdout := os.Stdout
	os.Stdout = capW
	cleanup := installStdoutRedactor() // os.Stdout is now the redactor's own pipe; it writes redacted output to capW
	defer func() { os.Stdout = realStdout }()

	// Split the secret across separate writes.
	io.WriteString(os.Stdout, "prefix ")
	io.WriteString(os.Stdout, fake[:len(fake)/2])
	io.WriteString(os.Stdout, fake[len(fake)/2:])
	io.WriteString(os.Stdout, " suffix\n")

	cleanup() // stops the redactor goroutine, restores os.Stdout to capW
	capW.Close()
	buf := make([]byte, 4096)
	n, _ := capR.Read(buf)
	got := string(buf[:n])
	if strings.Contains(got, fake) {
		t.Fatalf("secret leaked across split writes: %q", got)
	}
}

func TestRegisterSecretIgnoresShortValues(t *testing.T) {
	secretsMu.Lock()
	before := len(secrets)
	secretsMu.Unlock()

	RegisterSecret("short") // < 8 chars — must be ignored, or it'd redact common substrings

	secretsMu.Lock()
	after := len(secrets)
	secretsMu.Unlock()

	if after != before {
		t.Fatalf("RegisterSecret should ignore values under 8 chars, got %d -> %d entries", before, after)
	}
}

// --- zeroBytes (sessionKey/master-key hardening) -----------------------------

func TestZeroBytesClearsSlice(t *testing.T) {
	b := []byte("super-secret-decrypted-value")
	zeroBytes(b)
	for i, v := range b {
		if v != 0 {
			t.Fatalf("zeroBytes left a non-zero byte at index %d: %v", i, b)
		}
	}
}

func TestZeroBytesHandlesNilAndEmpty(t *testing.T) {
	zeroBytes(nil)      // must not panic
	zeroBytes([]byte{}) // must not panic
}

// TestZeroBytesDoesNotAffectAStringAlreadyCopiedOut proves the specific ordering
// readSessionKey relies on is actually safe: string(pt) in cleanValue COPIES pt's bytes
// (a Go string conversion never aliases), so zeroing pt via defer -- which runs AFTER
// cleanValue has already returned its own independent string -- cannot corrupt the
// value the caller receives.
func TestZeroBytesDoesNotAffectAStringAlreadyCopiedOut(t *testing.T) {
	pt := []byte("sk-ant-test-value-0123456789")
	s := string(pt) // same conversion cleanValue performs
	zeroBytes(pt)
	if s != "sk-ant-test-value-0123456789" {
		t.Fatalf("zeroing the source slice corrupted the already-copied string: %q", s)
	}
}

// TestReadSessionKeyZeroesLocalSecrets is a tripwire (same idiom as
// TestHeadlessIsHardcoded / TestNetworkEgressIsClaudeOnly below): it reads main.go's own
// source and proves the master key and raw decrypted plaintext are deferred to
// zeroBytes, so a future edit that adds a new early-return path or refactors this
// function can't silently drop the hardening without failing CI.
func TestReadSessionKeyZeroesLocalSecrets(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(b)
	fn := src[strings.Index(src, "func readSessionKey("):]
	if end := strings.Index(fn, "\nfunc "); end > 0 {
		fn = fn[:end]
	}
	if !strings.Contains(fn, "defer zeroBytes(key)") {
		t.Error("readSessionKey must defer zeroBytes(key) right after loadMasterKey succeeds")
	}
	if !strings.Contains(fn, "defer zeroBytes(pt)") {
		t.Error("readSessionKey must defer zeroBytes(pt) right after decryptValue succeeds")
	}
}

// goSourceFiles returns this package's own .go files (excluding _test.go), so the static
// checks below scan exactly what ships, not test scaffolding.
func goSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			files = append(files, n)
		}
	}
	return files
}

// TestHeadlessIsHardcoded proves the CDP session always runs a VISIBLE Chrome — the
// property that makes this tool unattractive for a covert attack (SECURITY.md point 1):
// a remote attacker abusing this against a victim would pop a visible browser window on
// the victim's desktop. No flag, env var, or CLI arg may ever override it to true.
func TestHeadlessIsHardcoded(t *testing.T) {
	b, err := os.ReadFile("cdp.go")
	if err != nil {
		t.Fatalf("read cdp.go: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, `chromedp.Flag("headless", false)`) {
		t.Fatal(`expected the literal chromedp.Flag("headless", false) in cdp.go`)
	}
	// Per-line (not whole-file) proximity check: "headless" and an override mechanism
	// (Getenv/flag.Bool) must never appear on the SAME line — that's the actual danger
	// pattern. Whole-file substring presence produces false positives (e.g. daemon.go
	// has an unrelated os.Getenv("APPDATA") plus the word "headless" in a comment).
	for _, f := range goSourceFiles(t) {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			l := strings.ToLower(line)
			if !strings.Contains(l, "headless") {
				continue
			}
			if strings.Contains(l, "getenv") {
				t.Fatalf("%s:%d: possible headless env-var override — headless must stay hardcoded: %q", f, i+1, line)
			}
			if strings.Contains(l, "flag.bool") {
				t.Fatalf("%s:%d: possible headless CLI-flag override — headless must stay hardcoded: %q", f, i+1, line)
			}
		}
	}
}

// TestNetworkEgressIsClaudeOnly proves there is no exfiltration path: every literal
// absolute URL used in an actual outbound call (chromedp.Navigate, or the in-page fetch
// path — which is same-origin-relative by construction once navigated to claude.ai)
// targets claude.ai. A future contributor adding a call to any other host would fail
// this test, which is the point — it's a tripwire, not just documentation.
func TestNetworkEgressIsClaudeOnly(t *testing.T) {
	// Matches string literals passed directly to the functions that actually issue a
	// network request in this codebase.
	navigateRe := regexp.MustCompile(`chromedp\.Navigate\("([^"]+)"\)`)
	for _, f := range goSourceFiles(t) {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range navigateRe.FindAllStringSubmatch(string(b), -1) {
			url := m[1]
			if !strings.HasPrefix(url, "https://claude.ai") {
				t.Fatalf("%s: chromedp.Navigate targets a non-claude.ai URL: %q", f, url)
			}
		}
	}
	// The in-page fetch() calls (probe.go, cdp.go get/rawGet) take a `path` argument that
	// is always a relative path (e.g. "/api/organizations"), never an absolute URL — a
	// relative fetch() resolves against the page's own origin, which is pinned to
	// https://claude.ai by the one Navigate() call above. Guard against that invariant
	// breaking: no fetch(%q call site may be given an absolute-URL-looking literal.
	fetchRe := regexp.MustCompile(`fetch\(%q`)
	absPathRe := regexp.MustCompile(`(?:get\w*\(\s*get\s*,\s*|rawGet\(\s*|\bget\(\s*)"(https?://[^"]+)"`)
	for _, f := range goSourceFiles(t) {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		s := string(b)
		if fetchRe.MatchString(s) {
			for _, m := range absPathRe.FindAllStringSubmatch(s, -1) {
				if !strings.HasPrefix(m[1], "https://claude.ai") {
					t.Fatalf("%s: an in-page fetch call site passes a non-claude.ai absolute URL: %q", f, m[1])
				}
			}
		}
	}
}

// TestNoImportableCookiePackage proves the DPAPI-decrypt + Cloudflare-bypass primitives
// stay package-scoped (package main), not published as a reusable, `go get`-able library
// — SECURITY.md point 5: reduces how attractive this is as a drop-in ingredient for
// someone else's unrelated tool.
func TestNoImportableCookiePackage(t *testing.T) {
	for _, f := range goSourceFiles(t) {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		first := strings.SplitN(string(b), "\n", 40)
		found := false
		for _, line := range first {
			if strings.TrimSpace(line) == "package main" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s: expected `package main` — this codebase must not become an importable package", filepath.Base(f))
		}
	}
}
