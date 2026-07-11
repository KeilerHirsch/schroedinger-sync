# Schroedinger Sync v2

[![CI](https://github.com/KeilerHirsch/schroedinger-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/KeilerHirsch/schroedinger-sync/actions/workflows/ci.yml)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)

Exports your own claude.ai conversations, project knowledge docs, and memory to local
Markdown — for feeding into your own local AI memory system (e.g.
[MemPalace](https://github.com/MemPalace/mempalace)). Windows-only, single Go binary.

See [CHANGELOG.md](CHANGELOG.md) for what's new in v2 versus the retired v1
(VS Code extension + Python CLI).

**Read [SECURITY.md](SECURITY.md) before running this.** This tool decrypts a
DPAPI-protected credential store — the same primitive class as credential-stealing
malware. SECURITY.md explains exactly why it can only ever be used against your own
account, and how that's enforced in code, not just promised in prose.

## What it exports

- **Conversations** — every chat_conversation on your account, full turn history
  including tool calls/results, converted to per-conversation Markdown.
- **Project knowledge docs** — every file attached to your claude.ai Projects.
- **Memory** — the claude.ai memory feature's current content.

There is no separate "Cowork"/"Code"/"Design" store to export from — those surfaces
live inside the same `chat_conversations` API, distinguished only by a `platform` field
(`CLAUDE_AI` / `VOICE`). One harvest command covers all of it.

## How it works (short version)

1. Decrypts your own `sessionKey` cookie from Claude Desktop's local, DPAPI-encrypted
   cookie store (`readSessionKey()` in `main.go`) — requires Desktop to be **closed**
   (Chromium holds the file locked while it runs).
2. Launches a real, visible Chrome, injects that cookie, navigates to claude.ai — Chrome
   clears Cloudflare's JS challenge itself and earns a fresh `cf_clearance` (`cdp.go`).
3. Uses same-origin in-page `fetch()` calls against claude.ai's own API to pull
   conversations, project docs, and memory.
4. Writes everything to `%LOCALAPPDATA%\SchroedingerSync\desktop-chats\` as Markdown
   — a stable per-user path, not the current working directory (see `defaultOutDir()`
   in `daemon.go`). Override with an explicit `outDir` argument where commands accept one.

## Requirements

- Windows 10/11, x64.
- Claude Desktop installed (the tool reads its local, DPAPI-encrypted cookie
  store — nothing else counts as a valid source).
- Google Chrome installed (CDP needs a real browser to clear Cloudflare's JS
  challenge; see "How it works" above).

## Installation

**Recommended — installer:** grab `SchroedingerSyncSetup.exe` from the
[latest release](https://github.com/KeilerHirsch/schroedinger-sync/releases/latest)
and run it. Per-user, no admin rights needed. It installs to
`%LOCALAPPDATA%\SchroedingerSync`, optionally adds a desktop icon, and
optionally registers itself to start in the tray on logon — all opt-in
checkboxes in the wizard, nothing silent. Uninstalling never touches your
already-harvested data.

**From source — for anyone who wants to read the code before running it:**

```
go build -trimpath -ldflags "-s -w" -o schroedinger-sync.exe .
# -trimpath: strips local filesystem paths from the binary (no build-machine info leaks)
# -s -w: strips debug symbols/DWARF (smaller binary; nothing to reverse-engineer for free)
```

## Usage

```
.\schroedinger-sync.exe            # auth smoke test (org + first 3 conversation titles)
.\schroedinger-sync.exe harvest    # full export: chats + project docs + memory
.\schroedinger-sync.exe probe      # dump the raw API schema + scan for new surfaces
.\schroedinger-sync.exe watch      # headless live-sync daemon, no GUI
.\schroedinger-sync.exe tray       # recommended: same daemon, with a system-tray icon
```

All commands require Claude Desktop to be **closed** — the cookie store is locked
while it's running (see SECURITY.md, "It cannot target another user's account").

## Live sync (`tray` / `watch`)

```
.\schroedinger-sync.exe tray [outDir] [intervalMinutes]     # recommended: visible tray icon
.\schroedinger-sync.exe watch [outDir] [intervalMinutes]    # headless, no GUI — default: see below, 30 min
.\schroedinger-sync.exe install-task                        # register logon autostart (uses tray)
.\schroedinger-sync.exe uninstall-task
```

`tray` puts an icon in the notification area with a right-click menu: "Jetzt
synchronisieren" (sync now), "Status anzeigen" (toast with the last cycle's result),
"Logs öffnen" (opens sync.log), "Beenden" (quit). Hovering the icon shows live status
in the tooltip. Both `tray` and `watch` run the exact same sync engine (`runCycle` in
`daemon.go`) — `tray` just adds a visible, dismissible presence instead of a silent
background process.

The daemon checks whether Claude Desktop is currently running before every cycle
(`isDesktopRunning()` in `daemon.go`) and skips cleanly if it is — no failed sync
attempts, no popped-up Chrome window while you're actively using Desktop. It only
actually syncs in the windows where Desktop happens to be closed. If you keep Desktop
open most of the time, this daemon will fire rarely by design — use `harvest` on demand
for anything time-sensitive.

Project docs and memory are refreshed once every 24h (they change far less often than
chats), independent of the chat-sync cycle.

## Testing

```
go test -v ./...
```

Runs the security invariant tests described in SECURITY.md — redaction, the hardcoded
headless flag, the claude.ai-only network egress check, and the non-importable-package
check.

## Scope

Claude/Anthropic only, for now. See SECURITY.md "Scope" section.

## License

AGPLv3 — free and open forever, no paid tier. Strong copyleft: any derivative must stay open-source under the same license. See [LICENSE](LICENSE) and SECURITY.md's
"Business model" section for why.
