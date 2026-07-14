# Changelog

## v2.1.2 — sync-engine hardening round 2 (harvest data integrity)

A second correctness pass on the harvest/sync engine, driven by a full byte-level teardown
plus an independent adversarial review. The security surface is unchanged (gosec, staticcheck
and govulncheck still clean); every fix is in the harvest/robustness path. Tests-first;
statement coverage 27.2% → 27.9%.

**Fixed:**
- **A Cloudflare/login/WAF HTML page could be saved as a conversation — permanently.** A
  challenge triggered mid-harvest returns HTTP 200 with an HTML body; it was neither a fetch
  error nor a rate-limit, so it was written verbatim as the conversation's Markdown and then
  locked in (state recorded it as current, and thereafter both the daemon and the one-shot
  harvest skipped it forever). Non-JSON bodies (anything starting with `<`) are now rejected
  before being treated as content or written, and `convToMarkdown` refuses to persist a
  non-JSON body.
- **Failed conversations now retry on the next cycle.** The daemon advanced its cookie-DB
  activity watermark even when some conversations had failed, so those failures waited until
  the user next opened/closed Claude Desktop (possibly days). A partial cycle now keeps the
  previous watermark so the next interval retries the failed items — bounded, so a
  permanently-failing item can't make the daemon re-list and pop Chrome every interval forever.
- **The one-shot `harvest` now refreshes changed conversations.** It skipped any existing
  file by size alone and so never re-exported a conversation that had changed server-side; it
  now compares the file's recorded `updated_at` header against the server, matching the
  daemon's freshness check (shared `fileIsCurrent`).
- **A partial project-docs refresh is no longer reported as a completed 24 h refresh** — it
  returns an error so the next cycle retries instead of swallowing the failures.
- **Empty claude.ai memory is a no-op**, not an error that re-tried the surfaces refresh
  every cycle forever.
- **A mid-harvest session-timeout is now a hard error**, not a "successful" partial export.

**Internal:**
- `saveState` writes to a unique temp file (was a fixed `.tmp`, which two instances sharing an
  output dir could race on). `fileConvUpdatedAt` reads a full bounded prefix (`io.ReadFull`)
  so a short read can't drop the header. The one-shot `harvest` now exits non-zero when any
  item failed. An invalid `watch` interval argument is logged instead of silently ignored.
- New regression tests: HTML/challenge rejection, the partial-cycle cookie watermark, the
  shared on-disk freshness check, and empty-memory-as-no-op.

**Still open (deliberately deferred):** a single-instance guard (a lockfile / named mutex so
two daemons can't share one output dir) — it needs its own careful, tested design and is
tracked for a follow-up.

## v2.1.1 — sync-engine correctness hardening

A focused correctness pass on the live-sync engine after a full multi-agent ECC review (Go
review + security review; gosec, staticcheck, and govulncheck all clean). The security surface
came through clean — every fix here is in the sync/robustness path.

**Fixed:**
- **Silent data loss on the first daemon cycle.** The daemon seeded its sync state from any
  existing on-disk file without checking that the file actually reflected the conversation's
  current server version. A conversation edited between the one-shot `harvest` and the first
  daemon cycle was recorded as up to date while the file still held the old content, and the
  change was never re-fetched. The daemon now compares the file's own recorded `updated_at`
  against the server's and re-fetches on any mismatch.
- **One-shot `harvest` no longer reports success on a partial export.** A mid-pagination
  listing failure used to break the loop and then report "DONE … 0 errors" over an incomplete
  conversation list. Listing failures are now fatal with a clear INCOMPLETE message, matching
  how the daemon already treated them.
- **Data race on `os.Stdout` in the tray daemon.** Project-doc and memory harvest diagnostics
  wrote straight to the `os.Stdout` package variable from the background sync goroutine while
  the tray "Beenden" handler reassigned it on another goroutine. They now route through the
  same `logf` sink as the rest of the daemon.

**Internal:**
- Extracted conversation-listing and filename construction into shared functions used by both
  the one-shot and daemon paths, so the two can no longer drift apart (the root cause of the
  partial-export bug).
- Made the retry/backoff and query-param fallback delays injectable so that logic is
  unit-testable in milliseconds. Test coverage 20.9% → 27.2%, with the previously-untested
  sync state machine now locked by regression tests.

## v2.1.0 — AGPLv3 relicense + gold-standard hardening

Relicensed from MIT to **AGPLv3** (still free and open, permanently) so any derivative —
including a hosted/network version — must stay open source. A full multi-tool + multi-model
review pass (Go review, security review, gosec, staticcheck, govulncheck, gofmt) with every
confirmed finding fixed and a unit-test suite that locks the fixes in.

**Security:**
- Re-pinned the Go toolchain to 1.26.5 to clear **GO-2026-5856** — a reachable Encrypted
  Client Hello privacy leak in `crypto/tls`.
- Hardened filename construction: every API-sourced path component (UUID/timestamp), not
  just the human title, is now stripped to `[0-9A-Za-z-]`, so a tampered claude.ai response
  could never escape the output directory via `..`.

**Fixed:**
- `extractText` no longer silently drops unknown conversation content-block types (e.g.
  extended-thinking or image blocks) — they are now preserved verbatim (was a data-loss bug).
- Fatal exits now flush the stdout redactor and tear down any open Chrome before exiting, so
  the last diagnostic line can't be lost to the async pipe and no visible browser is orphaned
  — including the tray "Beenden" click, which fires on a different goroutine.
- Added a mutex around all system-tray calls (the tray library has no internal locking),
  closing a potential Win32 icon-handle use-after-free between the sync goroutine and a menu click.
- `decryptValue` strips the 32-byte app-bound prefix for **v20** cookies only; a v10 cookie
  (which has no prefix) can no longer be truncated.
- `cleanValue` trims NUL padding before whitespace, so a stray trailing space can't survive
  into the sessionKey.
- `fetchConvBody` uses a sentinel error + `errors.Is` instead of matching an error string.
- Write failures are logged with their reason; an unknown subcommand prints usage instead of
  silently launching the smoke test; `explorer` is opened fire-and-forget.

**Tests:** new unit suite for the pure/parsing logic (truncation, sanitize/path-safety,
sessionKey cleaning, v10-vs-v20 decryption, Markdown conversion, org resolution incl. the
Cloudflare-challenge path, sync-state round-trip). Statement coverage 5% → 21%.

**Chore:** AGPL header on every source file; CI pins govulncheck/gosec/staticcheck to fixed
versions.

## v2.0.1 (unreleased)

Independent re-audit pass — two fresh review passes (general Go code quality, and a
security/threat-model review re-deriving its own judgment rather than trusting v2.0.0's
prior hardening claims) against the already-shipped v2.0.0 code, with all confirmed
findings fixed. `go build`/`vet`/`test`/`govulncheck`/`gosec`/`staticcheck` all still
report clean; nothing here was a tool-detectable issue — that's exactly why a second,
independent read mattered.

**Fixed:**

- **Data race** on the tray's status string (`tray.go`) — written by the background sync
  goroutine, read from a menu-click callback on a different goroutine, with no
  synchronization. Now a small mutex-guarded `statusHolder`.
- **Redaction blind spot:** chromedp's own internal logging defaulted to Go's stdlib
  `log.Printf` (targets stderr), entirely bypassing this program's stdout redactor while
  a live session held the injected sessionKey cookie. Wired `chromedp.WithErrorf`/
  `WithLogf` to route through the same `redact()` every other output path uses.
- **VBScript injection:** `install-task`'s optional `outDir` argument was spliced
  unescaped into the generated logon-autostart `.vbs` file; an embedded `"` could break
  out of the intended quoted argument. Now escaped per VBScript's own quote-doubling
  convention, with a test case that exercises exactly that input.
- **Silent multi-org gap:** the harvester always picked the account's first organization
  with no signal if a second (e.g. a Team workspace) existed. Now logs a warning so "did
  I get everything?" has an answer.
- **Non-atomic state writes:** `.sync-state.json` was written directly; now written to a
  temp file and renamed into place, so a crash or overlapping run can't leave a
  truncated/corrupt state file.
- **Byte-unsafe truncation:** `trunc()` sliced strings by byte index, which can split a
  multi-byte UTF-8 rune in half — reachable in practice via German titles (umlauts, ß)
  or emoji, producing a corrupted character in a filename. Now rune-safe.
- Documentation honesty pass on SECURITY.md: several claims read stronger than what the
  code actually enforces (the exact scope of `TestNetworkEgressIsClaudeOnly`'s literal-
  only URL matching, the redaction guarantee's stdout-vs-stderr boundary before the
  chromedp fix above, "one secret" undercounting the broader on-disk temp-copy
  footprint). Clarified each; added an explicit section on what the regex-based
  invariant tests do and don't protect against.
- Minor: an inaccurate code comment (attributed a Chrome cookie-encryption detail to
  "v10" when it's actually v20 App-Bound Encryption), an imprecise doc comment ("Task-
  Scheduler" when the actual mechanism is a Startup-folder `.vbs` drop), and an
  unbounded-growth nit in the redactor's secrets list across long daemon uptimes
  (deduped on register).

## v2.0.0

Complete rewrite. Nothing from v1 survives except the name and the goal
(export your own Claude conversations to local Markdown).

**Why the rewrite:** v1 was a VS Code extension (TypeScript) plus a Python CLI
that parsed local Claude Code session transcripts. It never touched claude.ai
itself — it had no way to reach Desktop/Web conversations, only what was
already sitting in local JSONL files. v2 solves the actual problem: pulling
your full claude.ai account (conversations, project docs, memory) via the
same API the web/desktop client uses.

**How it works now:**
- Decrypts your own `sessionKey` from Claude Desktop's DPAPI-protected cookie
  store — replaces v1's "read local files only" approach.
- An earlier v2 iteration tried raw TLS/JA3 impersonation
  (`bogdanfinn/tls-client`) to talk to the claude.ai API directly. Cloudflare's
  managed JS challenge defeated it — `cf_clearance` is fingerprint-bound and a
  borrowed cookie doesn't validate. That whole path (~12 dependencies) was
  deleted, not kept as a fallback.
- Replaced it with driving a real, visible Chrome via CDP (`chromedp`): inject
  the cookie, navigate to claude.ai, let Chrome solve the challenge itself,
  then call the API same-origin from inside the page. This is the only
  approach that actually works and it's the one this release ships.

**New in this release, none of it existed in v1:**
- `harvest` — full export: conversations, project knowledge docs, memory.
- `probe` — dumps the raw API schema, useful for finding new surfaces claude.ai
  adds later without guessing.
- `watch` / `tray` — a live-sync daemon (headless or with a system-tray icon),
  gated on Claude Desktop being closed (DPAPI needs the cookie file unlocked).
- Windows installer (`installer/schroedinger-sync.iss`, Inno Setup) — per-user,
  no admin required, autostart via the app's own `install-task` command.
- `security.go` / `security_test.go` — the threat model in SECURITY.md is
  enforced by tests (redaction, hardcoded non-headless flag, claude.ai-only
  egress, no importable package), not just asserted in prose.
- `govulncheck` / `gosec` / `staticcheck` all clean; see SECURITY.md and the
  CI workflow for the specifics.

**Removed:**
- The VS Code extension. It solved a different, smaller problem (local JSONL
  → Markdown) that `harvest`'s `platform` field doesn't even distinguish
  anymore — Code/Cowork/Design conversations all live in the same
  `chat_conversations` API v1 never had access to.
- The Python CLI and its `curl_cffi` dependency — same reason, superseded by
  the Go/CDP approach above.
- Freemium pricing plan (free/Pro €5/Team €15) from the original 2026-03
  business plan. v2 is AGPLv3-licensed and free, permanently — see SECURITY.md
  "Business model" for why.

## v1.0 (superseded, code removed)

VS Code extension + Python CLI. Converted local Claude Code session JSONL
files into readable Markdown summaries. Never had access to claude.ai
Desktop/Web conversations. Frozen since 2026-03-17.
