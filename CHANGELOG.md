# Changelog

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
  business plan. v2 is MIT-licensed and free, permanently — see SECURITY.md
  "Business model" for why.

## v1.0 (superseded, code removed)

VS Code extension + Python CLI. Converted local Claude Code session JSONL
files into readable Markdown summaries. Never had access to claude.ai
Desktop/Web conversations. Frozen since 2026-03-17.
