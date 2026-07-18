// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// Unit tests for the pure (credential-free, browser-free) logic: truncation, filename
// sanitization/path-safety, sessionKey cleaning, cookie decryption (v10 vs v20 32-byte
// prefix), conversation->Markdown conversion, and the sync-state round-trip. These lock in
// the gold-standard review fixes so a regression fails CI instead of shipping silently.
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestJsStringLiteral proves jsStringLiteral produces a JS-embeddable string literal for
// every character class that could otherwise break out of, or otherwise corrupt, the
// generated `fetch("...")` call — the fix for INFO-5 (previously fmt.Sprintf("%q", path),
// which relied on an implicit "Go %q ~= JS escaping" equivalence instead of the documented
// encoding/json guarantee).
func TestJsStringLiteral(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"double quote", "a\"b"},
		{"backslash", "a\\b"},
		{"line separator U+2028", "a b"},      // valid JSON, invalid raw in a JS string literal
		{"paragraph separator U+2029", "a b"}, // same class as U+2028, checked separately below
		{"newline", "a\nb"},
		{"ordinary API path", "/api/organizations/ORG/chat_conversations?limit=100&offset=0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lit := jsStringLiteral(c.in)
			if len(lit) < 2 || lit[0] != '"' || lit[len(lit)-1] != '"' {
				t.Fatalf("jsStringLiteral(%q) = %q, not a quoted literal", c.in, lit)
			}
			// The canonical round-trip check: what JSON.parse (and therefore a JS string
			// literal with the same syntax) would decode this back to must equal the input.
			var decoded string
			if err := json.Unmarshal([]byte(lit), &decoded); err != nil {
				t.Fatalf("jsStringLiteral(%q) produced invalid JSON/JS literal %q: %v", c.in, lit, err)
			}
			if decoded != c.in {
				t.Errorf("round-trip mismatch: got %q, want %q", decoded, c.in)
			}
			if strings.ContainsRune(lit, ' ') || strings.ContainsRune(lit, ' ') {
				t.Errorf("jsStringLiteral(%q) = %q still contains a raw line/paragraph separator", c.in, lit)
			}
		})
	}
}

func TestTruncIsRuneSafe(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 3, "hel"},
		{"hello", 10, "hello"},
		{"Müllärö", 4, "Müll"}, // 4 runes, not 4 bytes (ü is 2 bytes)
		{"😀😀😀", 2, "😀😀"},       // emoji: never split a rune
		{"", 5, ""},
	}
	for _, c := range cases {
		if got := trunc(c.in, c.n); got != c.want {
			t.Errorf("trunc(%q,%d)=%q want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize(`a<b>:"/\|?*c` + "\x01"); strings.ContainsAny(got, `<>:"/\|?*`+"\x01") {
		t.Errorf("sanitize left forbidden chars: %q", got)
	}
	if got := sanitize("   "); got != "Untitled" {
		t.Errorf("empty title -> %q want Untitled", got)
	}
	if got := sanitize("hello world"); got != "hello_world" {
		t.Errorf("spaces -> %q want hello_world", got)
	}
}

func TestPathSafeBlocksTraversal(t *testing.T) {
	for _, bad := range []string{"../../etc/passwd", "a/b", `a\b`, "..", "a..b", "x\x00y", "a b"} {
		got := pathSafe(bad)
		if strings.ContainsAny(got, `/\.`) {
			t.Errorf("pathSafe(%q)=%q still contains a path metacharacter", bad, got)
		}
	}
	// well-formed API fields must survive untouched
	if got := pathSafe("2026-07-11"); got != "2026-07-11" {
		t.Errorf("stripped valid date: %q", got)
	}
	if got := pathSafe("019c2dfd-62f8"); got != "019c2dfd-62f8" {
		t.Errorf("stripped valid uuid: %q", got)
	}
}

func TestHeaderSafe(t *testing.T) {
	if !headerSafe("sk-ant-abc123") {
		t.Error("valid printable-ASCII rejected")
	}
	for _, bad := range []string{"", "bad\x01ctrl", "ünïcödé", "line\nbreak"} {
		if headerSafe(bad) {
			t.Errorf("headerSafe accepted unsafe value %q", bad)
		}
	}
}

func TestCleanValueTrimOrder(t *testing.T) {
	// slices from the sk-ant- prefix and strips junk before it
	if got := cleanValue([]byte("noise sk-ant-KEY")); got != "sk-ant-KEY" {
		t.Errorf("cleanValue=%q want sk-ant-KEY", got)
	}
	// a trailing space BEFORE NUL padding must be trimmed (regression guard for the
	// fixed TrimSpace(TrimRight(...)) order — the old order left the stray space).
	if got := cleanValue([]byte("sk-ant-KEY \x00\x00")); got != "sk-ant-KEY" {
		t.Errorf("cleanValue trailing-space-before-NUL=%q want sk-ant-KEY", got)
	}
}

// encCookie builds a Chromium-style AES-256-GCM cookie blob: prefix + 12-byte nonce +
// ciphertext||tag. A fixed zero nonce keeps the test deterministic (test-only; never a
// pattern for real encryption).
func encCookie(t *testing.T, key, plaintext []byte, prefix string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 12)
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	return append(append([]byte(prefix), nonce...), ct...)
}

func TestDecryptValueV10DoesNotStrip(t *testing.T) {
	key := make([]byte, 32)
	val := []byte("sk-ant-sid02-0123456789abcdefghijklmnop") // > 32 bytes, no app-bound prefix
	got, err := decryptValue(encCookie(t, key, val, "v10"), key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(val) {
		t.Errorf("v10 decrypt=%q want %q — v10 has no 32-byte prefix and must not be truncated", got, val)
	}
}

func TestDecryptValueV20Strips32(t *testing.T) {
	key := make([]byte, 32)
	prefix32 := make([]byte, 32) // the app-bound SHA-256(domain) prefix v20 prepends
	val := []byte("sk-ant-sid02-realvalue")
	got, err := decryptValue(encCookie(t, key, append(prefix32, val...), "v20"), key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(val) {
		t.Errorf("v20 decrypt=%q want %q — the 32-byte app-bound prefix must be stripped", got, val)
	}
}

func TestExtractTextPreservesUnknownBlock(t *testing.T) {
	m := message{Content: []contentBlock{
		{Type: "text", Text: "hello"},
		{Type: "thinking", Content: json.RawMessage(`"deep thoughts"`)}, // unknown/new type
	}}
	out := extractText(m)
	if !strings.Contains(out, "hello") {
		t.Error("lost the text block")
	}
	if !strings.Contains(out, "deep thoughts") {
		t.Error("DROPPED the unknown 'thinking' block — silent data loss")
	}
	if !strings.Contains(out, "thinking") {
		t.Error("lost the block-type label for the preserved unknown block")
	}
}

func TestRawText(t *testing.T) {
	if got := rawText(json.RawMessage(`"plain"`)); got != "plain" {
		t.Errorf("string form=%q", got)
	}
	if got := rawText(json.RawMessage(`[{"text":"a"},{"text":"b"}]`)); got != "a b" {
		t.Errorf("array form=%q want 'a b'", got)
	}
	if got := rawText(nil); got != "" {
		t.Errorf("nil form=%q want empty", got)
	}
}

func TestConvToMarkdown(t *testing.T) {
	raw := `{"name":"Title","model":"claude","chat_messages":[` +
		`{"sender":"human","text":"question"},` +
		`{"sender":"assistant","content":[{"type":"text","text":"answer"}]}]}`
	md := convToMarkdown(raw)
	for _, want := range []string{"# Title", "Human", "Claude", "question", "answer"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n%s", want, md)
		}
	}
	// A non-JSON body (e.g. an HTML challenge/error page) must NOT be persisted as content
	// (C1 fix): convToMarkdown returns "" so the caller skips the write.
	if got := convToMarkdown("not json"); got != "" {
		t.Errorf("non-JSON body must be rejected (empty), got %q", got)
	}
	// A JSON body that starts with "{" but fails to unmarshal into a conversation (here a
	// type-mismatched field) is still kept verbatim rather than lost.
	if got := convToMarkdown(`{"name":123}`); got != `{"name":123}` {
		t.Errorf("unmapped JSON should pass through raw, got %q", got)
	}
}

func TestSyncStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &syncState{Conversations: map[string]string{"u1": "t1"}, LastSync: "x"}
	if err := saveState(dir, s); err != nil {
		t.Fatal(err)
	}
	got := loadState(dir)
	if got.Conversations["u1"] != "t1" || got.LastSync != "x" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	// a corrupt state file must degrade to a fresh, usable state (never a nil map)
	if err := os.WriteFile(statePath(dir), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := loadState(dir)
	if fresh.Conversations == nil {
		t.Error("corrupt state must yield a fresh non-nil Conversations map")
	}
}
