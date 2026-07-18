// Schroedinger Sync -- export your own claude.ai data to local Markdown.
// Copyright (C) 2026 KeilerHirsch
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU Affero General Public License as published by the Free
// Software Foundation, either version 3 of the License, or (at your option) any
// later version. It is distributed WITHOUT ANY WARRANTY; without even the implied
// warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// Affero General Public License <https://www.gnu.org/licenses/> for more details.

// Schroedinger Sync v2 — Claude.ai chat/project/memory export engine.
//
// The Man, The Mythos, The Legend : KeilerHirsch
//
// Reads YOUR OWN claude.ai sessionKey from Claude Desktop's DPAPI-encrypted cookie
// store, drives a real (visible) Chrome to clear Cloudflare's JS challenge, and
// exports your conversations, project knowledge docs, and memory to local Markdown
// for ingestion into MemPalace. See SECURITY.md for the full threat model — in short:
// this only ever works against the account of the user running it, on their own
// machine, requires a visible browser window, and never sends data anywhere but disk.
//
// Run it YOURSELF (it reads Cookies/Local State — the credential step stays in YOUR
// hand, never in an agent's context). The sessionKey is never printed; see security.go.
//
//	go build -o schroedinger-sync.exe .
//	.\schroedinger-sync.exe            # smoke: sessionKey -> org -> 3 titles
//	.\schroedinger-sync.exe harvest    # full export: chats + project docs + memory
//	.\schroedinger-sync.exe probe      # surface discovery (schema dump + endpoint scan)
//	.\schroedinger-sync.exe watch      # live-sync daemon (Desktop-closed gated)
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/billgraziano/dpapi"
	"golang.org/x/sys/windows"
	_ "modernc.org/sqlite"
)

func claudeDir() string { return filepath.Join(os.Getenv("APPDATA"), "Claude") }

// readSharedFile reads a file Chromium currently holds open, using Windows CreateFile
// with FILE_SHARE_READ|WRITE|DELETE — the same sharing native SQLite (Python) uses.
// modernc.org/sqlite's pure-Go VFS opens with stricter flags and fails on the live
// locked file, so we copy via shared read and parse the copy.
func readSharedFile(path string) ([]byte, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	// Close error on a shared read-only handle we've finished reading carries nothing
	// actionable — best-effort teardown, same as the other cookie-DB read paths below.
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// copyCookieDB copies Cookies(+wal,+shm) to a temp dir via shared read, so we can read
// it even while Claude Desktop holds a lock. Returns the copied DB path + cleanup.
// NOTE: this only ever succeeds for the row we actually query (see readSessionKey) —
// Claude Desktop still holds the live file exclusively while running; this shared-read
// trick reads the on-disk bytes at rest, it does not bypass a live in-memory lock.
func copyCookieDB() (string, func(), error) {
	srcDir := filepath.Join(claudeDir(), "Network")
	tmpDir, err := os.MkdirTemp("", "schroedinger_")
	if err != nil {
		return "", nil, err
	}
	// Best-effort cleanup of our own temp dir: if this fails, the OS temp-cleaning
	// eventually reclaims it — not a security issue, so the error is intentionally
	// unhandled (nothing meaningful to do with it here).
	cleanup := func() { _ = os.RemoveAll(tmpDir) } // #nosec G104 -- best-effort temp cleanup, see comment above
	main, err := readSharedFile(filepath.Join(srcDir, "Cookies"))
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("shared-read Cookies: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "Cookies"), main, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} { // optional, keeps recent writes
		if b, e := readSharedFile(filepath.Join(srcDir, "Cookies"+suffix)); e == nil {
			// Optional sidecar copy — if it fails we still proceed with just the main
			// Cookies file, which is all readSessionKey actually needs.
			_ = os.WriteFile(filepath.Join(tmpDir, "Cookies"+suffix), b, 0o600) // #nosec G104 -- optional, see comment above
		}
	}
	return filepath.Join(tmpDir, "Cookies"), cleanup, nil
}

func openCookieDB() (*sql.DB, func(), error) {
	dbPath, cleanup, err := copyCookieDB()
	if err != nil {
		return nil, nil, err
	}
	db, oerr := sql.Open("sqlite", dbPath)
	if oerr != nil {
		cleanup()
		return nil, nil, oerr
	}
	// Close() error on a read-only copy we're about to delete anyway carries no
	// actionable information — best-effort teardown.
	return db, func() { _ = db.Close(); cleanup() }, nil // #nosec G104 -- best-effort teardown, see comment above
}

// loadMasterKey: Local State -> base64 os_crypt.encrypted_key -> strip "DPAPI" -> user DPAPI.
// This is scoped to the CURRENT WINDOWS USER's own profile by construction — DPAPI keys
// are bound to the user who encrypted them; there is no code path here that can target
// another account. See SECURITY.md "Same-user-only" invariant.
func loadMasterKey() ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(claudeDir(), "Local State"))
	if err != nil {
		return nil, err
	}
	var ls struct {
		OsCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}
	if err := json.Unmarshal(b, &ls); err != nil {
		return nil, err
	}
	enc, err := base64.StdEncoding.DecodeString(ls.OsCrypt.EncryptedKey)
	if err != nil {
		return nil, err
	}
	if len(enc) < 5 || string(enc[:5]) != "DPAPI" {
		return nil, fmt.Errorf("unexpected encrypted_key prefix")
	}
	return dpapi.DecryptBytes(enc[5:])
}

// decryptValue: AES-256-GCM (v10/v20) cookie value, else legacy whole-value DPAPI.
func decryptValue(enc, key []byte) ([]byte, error) {
	if len(enc) > 15 && (string(enc[:3]) == "v10" || string(enc[:3]) == "v20") {
		isV20 := string(enc[:3]) == "v20"
		nonce := enc[3:15]
		ct := enc[15:] // ciphertext||tag — Go's GCM Open expects them concatenated
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		pt, err := gcm.Open(nil, nonce, ct, nil)
		if err != nil {
			return nil, err
		}
		// Chrome's v20 App-Bound Encryption scheme (not v10 — v10 has no such prefix)
		// prepends a 32-byte SHA-256(domain) to the plaintext; strip it for v20 ONLY,
		// so a v10 value (which has no prefix) can never be truncated by 32 bytes.
		if isV20 && len(pt) >= 32 {
			pt = pt[32:]
		}
		return pt, nil
	}
	return dpapi.DecryptBytes(enc)
}

// cleanValue: slice the sessionKey out from its sk-ant- prefix onward, trim padding.
func cleanValue(pt []byte) string {
	s := string(pt)
	if i := strings.Index(s, "sk-ant-"); i >= 0 {
		s = s[i:]
	}
	return strings.TrimSpace(strings.TrimRight(s, "\x00"))
}

// headerSafe: only printable ASCII (no control bytes) — guards against a corrupted or
// truncated decrypt producing an unusable/dangerous value.
func headerSafe(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// ErrDesktopNotFound is a sentinel (not a string match — see errors.Is usage in
// daemon.go's runCycle) for the single most common real-world first-run failure: Claude
// Desktop was never installed, or has never been opened/logged into, on this machine.
// Caught early with a clear, actionable message instead of letting a three-functions-
// deep "file not found" bubble up raw — see the Apple/defensive-design principle this
// project adopted 2026-07-02: friction points get a clear message, not a stack of
// unexplained OS errors.
// chained with other text — checked: readSessionKey/runCycle/exitOnSessionFailure all
// surface it as-is), and "Claude" is a capitalized proper noun, not a mid-sentence typo.
//
//lint:ignore ST1005 this is a complete, user-facing German sentence (never wrapped or
var ErrDesktopNotFound = errors.New("Claude Desktop wurde nicht gefunden — bitte installieren, einmal öffnen und einloggen: https://claude.ai/download")

// checkClaudeDesktopInstalled is a fast pre-check (no DPAPI decrypt attempted yet) for
// the one failure mode worth a dedicated friendly message: Local State only exists once
// Desktop has been installed AND launched at least once.
func checkClaudeDesktopInstalled() error {
	if _, err := os.Stat(filepath.Join(claudeDir(), "Local State")); err != nil {
		return ErrDesktopNotFound
	}
	return nil
}

// readSessionKey decrypts ONLY the sessionKey cookie row — not the full claude.ai
// cookie jar. CDP injects this one value as a cookie into a real Chrome session (see
// cdp.go); it never needs cf_clearance/__cf_bm/etc., so we never decrypt them. Smaller
// blast radius: exactly one secret is ever held in process memory. Registers the value
// with the redactor (security.go) before returning it, so it can never leak into any
// diagnostic output from this point on.
func readSessionKey() (string, error) {
	if err := checkClaudeDesktopInstalled(); err != nil {
		return "", err
	}
	key, err := loadMasterKey()
	if err != nil {
		return "", fmt.Errorf("master key: %w", err)
	}
	db, closeDB, err := openCookieDB()
	if err != nil {
		return "", err
	}
	defer closeDB()
	row := db.QueryRow(
		"SELECT encrypted_value FROM cookies WHERE host_key LIKE '%claude.ai%' AND name = 'sessionKey' LIMIT 1")
	var enc []byte
	if err := row.Scan(&enc); err != nil {
		return "", fmt.Errorf("no sessionKey cookie found (open Claude Desktop once to log in): %w", err)
	}
	pt, err := decryptValue(enc, key)
	if err != nil {
		return "", fmt.Errorf("decrypt sessionKey: %w", err)
	}
	val := cleanValue(pt)
	if !headerSafe(val) {
		return "", fmt.Errorf("decrypted sessionKey failed sanity check")
	}
	RegisterSecret(val)
	return val, nil
}

func main() {
	stopRedactor = installStdoutRedactor()
	defer stopRedactor()

	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "harvest":
		cdpHarvest() // full export: chats + project docs + memory -> Markdown
	case "probe":
		cdpProbe() // surface discovery: schema dump + sibling-endpoint scan
	case "watch":
		watchLoop() // headless live-sync daemon, no GUI (Desktop-process-gated incremental harvest), runs forever once started
	case "supervise":
		superviseLoop() // what install-task registers: like watch, but only while Desktop or VS Code is open — idle otherwise
	case "tray":
		trayMain() // same daemon, with a visible system-tray icon — for a manually-launched, always-on run
	case "install-task":
		installTask() // register logon autostart (user session, DPAPI + interactive Chrome)
	case "uninstall-task":
		uninstallTask()
	case "cleanup-temp":
		cleanupTemp() // sweep chrome-profile tree + %TEMP%\schroedinger_* leftovers (also wired into the installer's uninstall step)
	case "", "smoke":
		cdpSmoke() // default: auth smoke test (org + first 3 titles)
	default:
		fatal("unknown command:", cmd,
			"\nvalid: (no arg)|smoke, harvest, probe, watch, supervise, tray, install-task, uninstall-task, cleanup-temp")
	}
}

// Observer's note (the cat is out of the box): Gur ZNA, Gur ZLGU, Gur YRTRAQ; XrvyreUvefpu
