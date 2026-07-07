# Security Policy & Threat Model

This tool decrypts a Windows DPAPI-protected cookie store and uses the extracted
`sessionKey` to authenticate as you against claude.ai. **That is, byte-for-byte, the
same technique a credential-stealing trojan uses.** We're not going to pretend
otherwise, and a license disclaimer alone would not make it safe. What actually makes
it safe is the architecture below — enforced in code (`security_test.go`), not just
promised in this document.

## What this tool cannot do

**1. It cannot target another user's account.**
DPAPI master keys are bound to the Windows user account that created them. There is no
code path anywhere in this project that can decrypt another user's cookie store — it
only ever works within the OS session it's run in, against whatever account is logged
into Claude Desktop on *that* machine. See `loadMasterKey()` in `main.go`.

**2. It cannot run covertly.**
Claude.ai sits behind a Cloudflare managed JS challenge that headless browsers cannot
pass. This tool must launch a real, visible Chrome window to clear it
(`chromedp.Flag("headless", false)` — hardcoded, never a flag or env var; enforced by
`TestHeadlessIsHardcoded`). An attacker who had already compromised a victim's session
well enough to run this tool covertly would see a browser window pop up on the victim's
screen — a terrible trade against any purpose-built infostealer, which doesn't need a
GUI at all. This friction is the point, not an oversight.

**3. It cannot exfiltrate anything.**
Every output goes to local disk (`desktop-chats/`) or local MemPalace. There is no
network client anywhere in this codebase capable of sending data to a third-party
server — no webhook, no telemetry, no "phone home." Every outbound network call targets
`claude.ai` and nothing else, enforced by `TestNetworkEgressIsClaudeOnly`, which scans
the source for every *literal* request destination and fails the build if one doesn't
resolve to `claude.ai`. (That test's actual boundary: it only catches literal string
URLs — a request destination built via a variable or string concatenation wouldn't be
caught. The stronger, structural guarantee is that there is no `net/http` import
anywhere in this module — see `go.mod` — so there is no general-purpose HTTP client
to redirect in the first place.)

**4. The sessionKey is never observable in this program's own output.**
The moment it's decrypted, it's registered with a redactor (`security.go`) that scrubs
it from stdout (`installStdoutRedactor` — replaces `os.Stdout` with a pipe so *every*
write, from this code or any future third-party library targeting stdout, is scrubbed
before reaching the terminal), from chromedp's own internal logging
(`chromedp.WithErrorf`/`WithLogf`, wired in `cdp.go`'s `openClaudeSession` — without
this, chromedp's library defaults to Go's stdlib `log` package, which targets *stderr*
and would otherwise bypass the stdout redactor entirely while `network.Enable()` is
processing the injected sessionKey cookie for the whole session), and the daemon's log
file and `probe-report.txt` (both route through the same `redact()` call). Enforced by
`TestRedactionScrubsRegisteredSecret` and `TestStdoutRedactorSurvivesSplitWrites`.
(Scope: this covers every output path this program controls. It does not protect
against out-of-band forensics of the process's own memory — a debugger or memory dump
taken by someone who already has code-execution as the same Windows user, who could
just call `CryptUnprotectData` on `Local State`/`Cookies` directly instead.)

**5. Only the minimum secret is ever decrypted.**
Earlier versions of this tool decrypted the *entire* claude.ai cookie jar (session key,
Cloudflare tokens, everything). `readSessionKey()` now decrypts exactly one row — the
`sessionKey` cookie — because CDP only ever needs that one value; Chrome earns its own
`cf_clearance` by solving the challenge itself. Smaller blast radius: exactly one
*decrypted (plaintext)* secret is ever held in process memory. (Note: `copyCookieDB()`
does copy the *entire* Cookies SQLite file — every domain, still DPAPI/AES-GCM-encrypted
at rest — to a temp directory for every session, with best-effort cleanup that isn't
guaranteed if the process is killed mid-run. That's a broader on-disk footprint than
"one secret" might suggest by itself, though nothing in that copy is ever decrypted
except the single sessionKey row.)

**6. It's not published as a reusable library.**
Everything lives in `package main`, not an importable module (`TestNoImportableCookiePackage`).
Someone wanting to reuse the DPAPI-decrypt or Cloudflare-bypass primitives for an
unrelated purpose has to read and copy the code deliberately — not `go get` it as a
drop-in ingredient.

**7. Rate-limited by design.**
`getWithRetry` applies exponential backoff on `rate_limit_error` responses. This exists
because heavy sessions legitimately hit transient rate limits, but it also means this
tool cannot be trivially turned into a hammer against Anthropic's infrastructure.

## What "enforced by tests" actually means

`TestHeadlessIsHardcoded` and `TestNetworkEgressIsClaudeOnly` are regex/substring scans
of the source, not a general-purpose static analyzer. They're regression tripwires —
built to catch an *accidental* reintroduction of headless mode or a stray non-claude.ai
network call during ordinary maintenance — not a hard security boundary that would
survive a deliberately adversarial change. Concretely: splitting an override across two
lines specifically to dodge `TestHeadlessIsHardcoded`'s same-line proximity check, or
building a request URL via a variable instead of a literal (invisible to
`TestNetworkEgressIsClaudeOnly`), would both slip through undetected. For a
solo-maintained project where the realistic risk is "I changed something and didn't
notice the consequence," not "a malicious contributor with commit access," that's an
honest and appropriate scope for what these two tests buy you.
`TestNoImportableCookiePackage` is the one test in this set that's robust against
deliberate evasion — dodging it requires literally restructuring the module out of
`package main`.

## What this license does *not* do

We deliberately did not add an "ethical use only" clause to the LICENSE. Such clauses
are legally close to unenforceable and mostly function as security theater — they make
the author feel better without stopping anyone. The actual protection is everything
above: architectural constraints, enforced by tests, that make this tool a poor choice
for anything other than exporting your own data from your own account on your own
machine.

## Business model: free, forever

This project's original 2026-03 plan was a freemium SaaS (free tier, paid Pro/Team
tiers). That plan is retired. Schroedinger Sync v2 is MIT-licensed and free — no paid
tier, no telemetry, no upsell — permanently. Reasons, briefly:

- **The real addressable audience is a technical niche** (people running Claude Desktop
  + VS Code Claude Code + a local memory system like MemPalace), not a mass market.
  A paid tier only makes sense at a scale this project doesn't have and isn't chasing.
- **A tool that decrypts your own credentials should not also be asking you to pay
  a stranger for the privilege.** Free + open source lets anyone read exactly what it
  does before trusting it with DPAPI access — that's the actual value exchange here,
  not a price tag.
- **Charging money changes the liability picture.** Free software under MIT ships
  "AS IS, WITHOUT WARRANTY" (see LICENSE) — a reasonable basis for a solo-maintained
  security-adjacent tool. A paid product would carry consumer-rights and warranty
  obligations disproportionate to what a hobby project can responsibly support.

## Scope: Claude/Anthropic only (for now)

This is intentionally scoped to claude.ai. Other providers (ChatGPT, Gemini, ...) each
have their own auth model, cookie format, and challenge/anti-bot behavior — building
correct, equally-hardened support for each is a separate effort, not a quick extension
of this codebase. Not a limitation we plan to paper over; a deliberate v2 scope
decision.

## Reporting a vulnerability

Open a private security advisory on this repository, or contact
[@KeilerHirsch](https://github.com/KeilerHirsch) directly. Do not open a public issue
for a security report.

## Supported versions

| Version | Supported |
|---------|-----------|
| v2.x (CDP-based) | Yes |
| v1.x (tls-client impersonation, removed) | No — superseded, code deleted |
