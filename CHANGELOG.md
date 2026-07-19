# Changelog

## v2.2.0 — ingest integrity + sessionKey hardening + gold-standard review pass

Full ECC multi-agent review (Go + security reviewer) against HEAD, followed by a fix pass.
Security surface came through clean (0 CRITICAL/HIGH/MEDIUM); Go review found 3 MEDIUM +
5 LOW, all fixed. A second, independent review pass on the fix pass itself then caught one
HIGH regression the first pass introduced (chrome-profile single-instance race, see below)
before it ever shipped. Statement coverage 27.8% → 35.0%.

**Fixed:**
- **`cdpSmoke` (the default, no-argument command) reported "SMOKE TEST: GREEN" even after a
  listing or parse failure** — an auth/Cloudflare error at exactly that step was signalled
  as success. Now fails hard (`fatal`) on either error instead.
- **9 `#nosec` annotations used a gosec rule ID (`G304`) that doesn't suppress this
  codebase's actual path-taint finding (`G703`)** on 3 of those lines, silently leaving them
  unsuppressed — caught by re-running gosec locally against the exact CI-pinned version
  (v2.27.1) rather than trusting the claim that produced the wrong ID.
- **The core harvest decision loop (`harvestOnce`) was untestable** — it opened its own
  Chrome session internally, so the actionSkip/Seed/Fetch dispatch and progress counting had
  no test coverage. Pulled into `syncConversations(get, org, outDir, s, all, delay)`, which
  takes an already-open session as a parameter; now covered by `TestSyncConversations` and
  `TestSyncConversationsDeadlineExceededAborts`.
- **`probe-report.txt` was written to the current working directory**, not the tool's stable
  data directory — scattered wherever the binary happened to be launched from. Now written
  under `defaultOutDir()`.
- **`probe` silently swallowed fetch errors** on two endpoints, producing an empty/misleading
  report section with no indication the fetch itself had failed.
- **`install-task`'s generated VBScript launcher had no defense against an embedded raw
  CR/LF** in `outDir` (a literal `"` was already handled by existing quote-doubling) — such
  a line break terminates the generated statement itself. Verified via actual `cscript.exe`
  execution of the pre-fix-shaped output: it fails with a compile error
  ("unterminated string constant"), i.e. the practical pre-fix impact is autostart silently
  failing to install (a self-inflicted break), not attacker code execution — since the
  quote-doubling already closes the only actual injection path. Still rejected outright
  before the launcher is written, rather than shipping a launcher that can be broken by its
  own input.
- **The tray "Beenden" (quit) handler could race the background sync goroutine** into calling
  `SetTooltip` on an already-`Remove()`d tray icon. A `shuttingDown` flag, checked under the
  same mutex that already serializes both goroutines, closes the window.
- **`logSink` was a plain package variable relying on an unenforced ordering invariant**
  (`setupFileLog` must run before any goroutine reads it) — now behind an `atomic.Pointer`.
- **A hard-killed run (crash/SIGKILL/panic — no `recover()` in this codebase) left an
  anonymous Chrome profile in `%TEMP%`** holding the injected sessionKey cookie
  (Chrome-encrypted, not plaintext, but unbounded on-disk footprint). Chrome now launches
  with a pinned, per-PID `UserDataDir` under `%LOCALAPPDATA%`, swept at the start of every
  session. Scoped by PID rather than one shared path: this tool has no single-instance
  guard, and `supervise` autostart running alongside a manually-launched `tray`/`watch`/
  one-shot command is an explicitly supported combination (see below) — a shared path would
  let one instance's sweep delete a profile another instance's live Chrome still has open
  (go-reviewer catch). The sweep also refuses to touch a reparse point/symlink rather than
  blindly `RemoveAll`-ing through it, in case a future change ever makes that path less
  fully predictable than it is today.
- **`fetch()`'s JS string was built with Go's `%q`**, relying on an implicit "Go `%q` ≈ JS
  escaping" equivalence. Replaced with `encoding/json`-based encoding — the documented,
  canonical way to embed a Go string into JS source (also covers U+2028/U+2029, which `%q`
  happened to escape correctly but not for a JS-specific reason).
- **`isDesktopRunning()` could not tell Claude Desktop apart from Claude Code's own CLI**,
  which also runs as a process literally named `claude.exe` (bundled inside the VS Code
  extension) — so a sync cycle would report "Desktop is running" and skip, purely because
  the CLI was active, even with Desktop fully closed. Now matched by executable path
  (`WindowsApps\Claude_...`), not just image name.
- **`install-task`'s autostart had no lifecycle at all** — it wired straight to `tray`, which
  ran a sync cycle on a fixed interval forever regardless of whether anyone was at the
  machine. New `supervise` subcommand (what `install-task` now registers) only runs cycles
  while Claude Desktop or VS Code is open, and goes idle otherwise.

**Added:**
- CI: a separate `race` job (`go test -race`, `CGO_ENABLED=1` via provisioned mingw-w64) —
  the concurrency this project has real hardening history with (`os.Stdout` redaction pipe,
  tray shutdown race, `logSink`) is now checked by the actual detector, not by reading code.
- **New `cleanup-temp` subcommand** — install/uninstall completeness gap: the installer's
  `[UninstallRun]` only ever removed the autostart launcher, leaving two runtime-only
  residue locations behind forever once nothing ever runs again to sweep them: the
  chrome-profile tree under `%LOCALAPPDATA%` (only the *current* PID's subdirectory is
  swept during normal operation, by design — see the PID-scoping fix above — so an old,
  crashed PID's leftover is never revisited), and any `%TEMP%\schroedinger_*` copy
  `copyCookieDB` (main.go) leaves behind from a hard-killed run (each gets a fresh random
  name every session, so there's no fixed location for a future run to even check). Wired
  into the installer's `[UninstallRun]` alongside `uninstall-task`, and also runs once at
  every `watch`/`supervise`/`tray` startup for ongoing hygiene between uninstalls.
  Deliberately does NOT touch `desktop-chats/` — that's the user's own exported
  conversation history, not installer/runtime state.
  **Caught and fixed before it ever shipped:** the first draft swept the ENTIRE
  chrome-profile tree with one blind `RemoveAll`, which would have reintroduced the exact
  class of bug the PID-scoping fix above exists to prevent — Chromium refuses `FILE_SHARE_*`
  on some of its own profile files while running, so a concurrently-running instance's live
  Chrome profile wouldn't cleanly resist deletion, it could have files removed out from
  under it before the walk ever reached the one that blocks it. Now checks each
  subdirectory's PID against `processAlive` (`windows.OpenProcess` +
  `GetExitCodeProcess`/`STILL_ACTIVE`) and only sweeps ones whose process is actually gone;
  the `%TEMP%\schroedinger_*` matches (no PID to check against — each name is random) are
  filtered by age instead (`residueMinAge`, 5 minutes — far longer than any single
  `copyCookieDB` call runs). Independently re-verified by a second Go-reviewer +
  security-reviewer pass on this specific fix: `removeResidueTree`'s existence check used
  `os.Stat` (follows a reparse point to its target), which for a *dangling* junction would
  return not-exist and skip the `isReparsePoint` refusal entirely — switched to `os.Lstat`
  to match `isReparsePoint`'s own non-following semantics. `main.go`'s usage string was
  missing `supervise` (present in the dispatch switch, just not the help text) — added.
  Added `TestCleanupTempSweepsOnlyDeadAndOldEntries`, an end-to-end test of the actual
  assembled sweep logic (not just its primitives in isolation), via `t.Setenv`-redirected
  `LOCALAPPDATA`/`TMP` fixtures. **Known, deliberately deferred residual gap** (same class
  as this project's already-documented "single-instance guard" open question):
  `processAlive` checks whether the schroedinger-sync.exe process that created a
  chrome-profile subdirectory is still alive, not whether an orphaned Chrome *child*
  outlived a hard-killed parent (os/exec doesn't tie child lifetime to parent via a Job
  Object here) and is still using that directory under a different PID — closing that needs
  scanning running processes' command lines for a matching `--user-data-dir`, real design
  work, not folded into this round.
- **Installer now sweeps previous-version residue *before* installing, not just on
  uninstall.** `cleanup-temp` closed the gap for residue left behind between runs and on
  uninstall, but `DefaultDirName`/`AppId` never change across versions, so a plain upgrade
  silently reused `{app}` with no equivalent pre-install check — residue from the version
  being replaced (stale autostart launcher, orphaned chrome-profile subdir) could sit next
  to a fresh install indefinitely. A new `[Code]` `CurStepChanged(ssInstall)` handler now
  asks whichever version is *currently* installed to run its own `uninstall-task` +
  `cleanup-temp` before `[Files]` overwrites it — delegated to the old exe rather than
  reimplemented in Pascal, so the PID-liveness/reparse-point/age-gating safety logic above
  is reused, not duplicated. `ssInstall` is documented to fire "just before the actual
  installation starts," i.e. strictly before the old exe is overwritten (verified against
  the Inno Setup Pascal Scripting reference, not assumed). Known gap: upgrading *from* a
  version older than this one no-ops the `cleanup-temp` half of the pre-install sweep (that
  command didn't exist yet in the old exe) — harmless, since the new exe's own
  `cleanup-temp` still runs automatically on its first `watch`/`supervise`/`tray` start
  regardless; every upgrade from this version onward gets the full pre-install sweep.
- README/roadmap sharpened against fresh research (2026-07-17/18): the target-audience
  paragraph now names the two sharpest buyer wedges specifically (law firms/§203 StGB,
  healthcare/§393 SGB V+BSI C5) instead of a general EU/regulated-industries gesture; the
  encryption-at-rest roadmap item is now a concrete envelope-encryption design (AES-256-GCM
  exports, TPM-backed CNG key via the Platform Crypto Provider, DPAPI fallback); the
  Ada/SPARK item is marked resolved (pure Go, SPARK stays conceptual — see the same-day
  scoping findings) instead of left open-ended; added a session-key `[]byte`-and-zero
  hardening item and a SHA-256 round-trip verbatim-integrity design for the native
  MemPalace ingest handshake. SECURITY.md gained a short addition distinguishing the
  offensive-shaped `CryptUnprotectData` this tool already does (point 1/2) from the planned
  defensive `CryptProtectData`-wrapped-own-key use in the new encryption work, since
  conflating the two is an easy mistake for a scanner or a skimming reviewer to make.

**Added:**
- **Write-side SHA-256 manifest — the first half of the native MemPalace ingest handshake**
  (README roadmap #1). Every export write (`cdpHarvest`'s one-shot chat harvest,
  `syncConversations`' daemon-cycle writes, `harvestProjects`/`harvestMemory`'s project-doc
  and memory-blob writes) now routes through a single new choke point,
  `writeMarkdown(outDir, fname, content)` (`integrity.go`), which writes the file and
  records its SHA-256 in `outDir/.content-hashes.json`, keyed by filename. Loaded and saved
  *per file*, not batched like `.sync-state.json` — deliberately, so a harvest interrupted
  mid-way (`context.DeadlineExceeded` is an existing, handled failure mode in this codebase)
  never loses the hash record for a file that already safely reached disk; the manifest is
  exactly as durable as the files it describes. Only the write side is shipped here — the
  read-back half (a re-hash pulled from MemPalace's own store, compared against this
  manifest, reported as an X/Y scorecard) needs a corresponding change in the separate
  mempalace-src ingest pipeline and is intentionally not built in this repo; README marks it
  open rather than implying the full round-trip exists.
- **sessionKey/master-key zero-hardening.** The raw AES master key (`loadMasterKey`) and the
  raw decrypted sessionKey plaintext (`decryptValue`'s return) are `[]byte` — unlike the
  immutable `string` `readSessionKey` ultimately returns, a `[]byte` can actually be
  overwritten. New `zeroBytes()` (`security.go`) is now `defer`red onto both, right after
  each buffer's last read (`loadMasterKey`'s key right after acquisition, `pt` right after
  `cleanValue` extracts its own independent string copy — a Go `string(byteSlice)`
  conversion always copies, never aliases, so zeroing the source slice afterward cannot
  corrupt the value already handed to the caller). Deliberately does NOT touch the redactor's
  own `secrets []string` registry (`RegisterSecret`/`redact`) — that copy is kept alive for
  the whole process lifetime on purpose, so stdout/log output stays scrubbed for as long as
  the program runs; converting it to zeroable bytes would break that single-choke-point
  redaction guarantee for no real gain. Narrows, does not close, the window covered by
  SECURITY.md point 4's existing "does not protect against a memory dump" caveat. A tripwire
  test (`TestReadSessionKeyZeroesLocalSecrets`, same static-source-scan idiom as the existing
  `TestHeadlessIsHardcoded`) fails CI if a future refactor drops either `defer`.

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
