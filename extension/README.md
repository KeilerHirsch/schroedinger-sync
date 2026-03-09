# Schroedinger Sync

**Your Claude instances finally talk to each other.**

Schroedinger Sync automatically converts Claude Code session transcripts into readable Markdown summaries. Bridge the gap between Claude Desktop and Claude Code.

## Features

- **One-Click Sync** -- Statusbar button, no terminal needed
- **Auto-Sync** -- Watches for new sessions, syncs automatically (5s debounce)
- **Markdown Output** -- Clean, readable session summaries with tool statistics
- **Git Integration** -- Optional auto-commit after sync
- **Zero Config** -- Works out of the box, 3 optional settings
- **Lightweight** -- Pure TypeScript, no external dependencies, <10 KB

## How It Works

1. Claude Code saves session transcripts as JSONL files in `~/.claude/projects/`
2. Schroedinger Sync watches for new or changed files
3. Parses conversations: user messages, assistant responses, tool usage stats
4. Generates Markdown summaries in your workspace (`./sync/` by default)

## Settings

| Setting | Default | Description |
|---------|---------|-------------|
| `schroedingerSync.autoSync` | `true` | Auto-sync when new sessions are detected |
| `schroedingerSync.outputDir` | `./sync` | Output directory for Markdown summaries |
| `schroedingerSync.gitAutoCommit` | `false` | Auto-commit synced files to Git |

## Commands

- **Schroedinger Sync: Sync Now** -- Manually trigger a sync (also via Statusbar click)

## Statusbar

The statusbar shows:
- `Schroedinger: 3 new` -- 3 unsynced sessions detected
- `Schroedinger: synced` -- All sessions are up to date

Click the statusbar item to trigger a sync.

## Requirements

- VS Code 1.85+
- Claude Code (generates the JSONL session transcripts)

## Also Available

- [Python CLI (v0.1)](https://github.com/KeilerHirsch/schroedinger-sync/tree/main/cli) -- Terminal-based sync for automation and CI

## Links

- [GitHub](https://github.com/KeilerHirsch/schroedinger-sync)
- [Issues](https://github.com/KeilerHirsch/schroedinger-sync/issues)
- [Changelog](CHANGELOG.md)

## License

MIT -- [KeilerHirsch](https://github.com/KeilerHirsch)
