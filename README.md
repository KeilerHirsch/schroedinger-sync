# Schroedinger Sync

**Automatic sync between Claude Desktop and VS Code.**

> "Is your Claude synchronized? Yes. And no. -- until you install Schroedinger Sync."

## The Problem

Claude Desktop App and Claude Code (VS Code) are completely separate instances. They don't share memory, context, or conversation history. If you use both, you're stuck manually copying information between them.

## The Solution

Schroedinger Sync reads Claude Code session transcripts (JSONL) and generates readable Markdown summaries. These summaries bridge the gap between your Claude instances -- and any other AI tools you use.

## Quick Start

### VS Code Extension (v1.5) -- Recommended

Install from the [VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=KeilerHirsch.schroedinger-sync) or search "Schroedinger Sync" in Extensions.

Works out of the box -- Statusbar button appears, auto-sync watches for new sessions.

### Python CLI (v1.0)

```bash
# Clone
git clone https://github.com/KeilerHirsch/schroedinger-sync.git
cd schroedinger-sync/cli

# Run (no dependencies needed -- pure Python 3.10+)
python sync_schroedinger.py

# With options
python sync_schroedinger.py --max 5        # Process max 5 sessions
python sync_schroedinger.py --git           # Auto-commit after sync
python sync_schroedinger.py --output ./out  # Custom output directory
```

## What It Does

1. Finds all Claude Code JSONL session transcripts in `~/.claude/projects/`
2. Parses conversations: user messages, assistant responses, tool usage
3. Generates clean Markdown summaries per session
4. Tracks sync state (only processes new/changed sessions)
5. Optionally auto-commits to Git

## Output Example

Each session becomes a Markdown file like `20260305_parsed-booping-ullman.md`:

```markdown
# Session: parsed-booping-ullman

**Session ID:** e1835fc4-dc62-444a-b908-70c61fb79825
**Datum:** 2026-03-05 07:28 UTC
**Turns:** 88

## Tool-Nutzung
- Bash: 237x
- Read: 146x
- Edit: 113x
- Agent: 73x

## Conversation Flow
1. **User:** Fix the LTeX extension...
2. **User:** Check all extensions...
...
```

## Configuration

Environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `SCHROEDINGER_OUTPUT` | `./sync` | Output directory for summaries |
| `SCHROEDINGER_GIT_COMMIT` | `false` | Auto-commit after sync |
| `SCHROEDINGER_MAX_SESSIONS` | `0` (all) | Max sessions to process |

## Requirements

- Python 3.10+
- No external dependencies (stdlib only)
- Windows / macOS / Linux

## Roadmap

- **v1.0** -- Python CLI (stable)
- **v1.5** -- VS Code Extension (current)
- **v2.0** -- Multi-AI sync (Claude + Gemini + DeepSeek), Freemium

## License

MIT -- see [LICENSE](LICENSE)

## Author

**KeilerHirsch** ([@KeilerHirsch](https://github.com/KeilerHirsch))

Built with Claude Code. Because Schroedinger Sync didn't exist yet.
