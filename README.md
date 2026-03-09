# Schroedinger Sync

**Your AI instances finally share a brain.**

> *"Is your AI synchronized? Yes. And no. -- until you install Schroedinger Sync."*

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![VS Code](https://img.shields.io/badge/VS%20Code-Extension-007ACC?logo=visualstudiocode)](https://marketplace.visualstudio.com/items?itemName=KeilerHirsch.schroedinger-sync)
[![Python 3.10+](https://img.shields.io/badge/Python-3.10+-3776AB?logo=python&logoColor=white)](cli/)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://github.com/KeilerHirsch/schroedinger-sync/pulls)

---

## The Problem

Every AI instance starts from zero. **Claude Desktop doesn't talk to Claude Code. Neither talks to Gemini. Or ChatGPT. Or DeepSeek.**

If you use more than one AI tool, you already know:

- You brainstorm in Claude Desktop, then manually copy context into VS Code
- You fact-check in Gemini, but the correction never reaches your Claude session
- You lose your entire conversation context when hitting the **75% token limit** -- forced to start over
- One AI confidently states something another already corrected three sessions ago

This isn't a minor inconvenience. There are [at least 6 open feature requests](https://github.com/anthropics/claude-code/issues) on Anthropic's official repo for exactly this -- Desktop-to-Code session sync. Anthropic's own workarounds (`&` prefix, `--teleport`, `claude mcp serve`) are all **one-way**. No one has solved bidirectional sync yet.

**Community projects have tried and failed:**

| Project | Approach | Limitation |
|---------|----------|------------|
| [Claude_Automation](https://github.com/dimascior/Claude_Automation) | File-based messaging (Windows/WSL) | Platform-locked, no multi-AI |
| [claude-sync](https://github.com/tawanorg/claude-sync) | Cloud sync of `~/.claude/` | Breaks on different filesystem paths |
| [claude-memory-mcp](https://github.com/randall-gross/claude-memory-mcp) | Shared SQLite knowledge graph | Memory only, no session continuity |

Schroedinger Sync takes a different approach entirely.

---

## The Solution

Instead of trying to sync live sessions (which every API restricts), Schroedinger Sync builds a **shared knowledge layer** across all your AI tools.

```
                    Your AI Ecosystem
                          |
        +-----------------+-----------------+
        |         |         |         |
     Claude    Gemini   DeepSeek   ChatGPT
     (JSONL)   (JSON)    (MD)      (JSON)
        |         |         |         |
        +----+----+----+----+----+----+
             |              |
        +---------+    +---------+
        | Memory  |    |Knowledge|
        | Layer   |    | Graph   |
        | (Free)  |    | (Pro)   |
        | .md     |    | .json   |
        +---------+    +---------+
             |              |
        +----+--------------+----+
        |   Conflict Detector    |
        |  "GdB 20 vs 30?"  |
        +------------------------+
             |
        Export to any AI
```

**How it works:**

1. Reads session transcripts from Claude Code, Gemini, DeepSeek, ChatGPT
2. Extracts structured knowledge: facts, decisions, corrections, tasks
3. Detects contradictions across AI sources ("Claude says X, Gemini says Y")
4. Generates clean Markdown topic files -- your AI-agnostic memory
5. One-click export to feed any AI tool with verified context

**Privacy-first:** Everything stays local. No cloud. No server. No account. GDPR by design.

---

## Quick Start

### VS Code Extension (v1.5) -- Recommended

Install from the [VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=KeilerHirsch.schroedinger-sync) or search "Schroedinger Sync" in Extensions.

Works out of the box -- statusbar button appears, auto-sync watches for new sessions.

### Python CLI (v1.0)

```bash
git clone https://github.com/KeilerHirsch/schroedinger-sync.git
cd schroedinger-sync/cli

# Run (zero dependencies -- pure Python 3.10+)
python sync_schroedinger.py

# Options
python sync_schroedinger.py --max 5        # Process max 5 sessions
python sync_schroedinger.py --git           # Auto-commit after sync
python sync_schroedinger.py --output ./out  # Custom output directory
```

---

## What It Does Today (v1.5)

1. Finds all Claude Code JSONL session transcripts in `~/.claude/projects/`
2. Parses conversations: user messages, assistant responses, tool usage
3. Generates clean Markdown summaries per session
4. Tracks sync state (only processes new/changed sessions)
5. Optionally auto-commits to Git

### Output Example

Each session becomes a Markdown file like `20260305_parsed-booping-ullman.md`:

```markdown
# Session: parsed-booping-ullman

**Session ID:** e1835fc4-dc62-444a-b908-70c61fb79825
**Date:** 2026-03-05 07:28 UTC
**Turns:** 88

## Tool Usage
- Bash: 237x
- Read: 146x
- Edit: 113x
- Agent: 73x

## Conversation Flow
1. **User:** Fix the LTeX extension...
2. **User:** Check all extensions...
...
```

---

## Configuration

Environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `SCHROEDINGER_OUTPUT` | `./sync` | Output directory for summaries |
| `SCHROEDINGER_GIT_COMMIT` | `false` | Auto-commit after sync |
| `SCHROEDINGER_MAX_SESSIONS` | `0` (all) | Max sessions to process |

---

## Roadmap

| Version | Status | What |
|---------|--------|------|
| **v1.0** | Done | Python CLI -- session parsing and Markdown export |
| **v1.5** | Done | VS Code Extension -- auto-sync, statusbar integration |
| **v0.3** | **New!** | **Desktop Chat Extraction** -- reads Claude Desktop conversations via official API (DPAPI + AES-256-GCM) |
| **v2.0** | In Design | **AI Knowledge Graph** -- multi-AI parsing, fact extraction, conflict detection, encrypted storage |
| **v2.5** | Planned | Browser extension, additional AI format support |
| **v3.0** | Planned | Enterprise self-hosted, SSO/LDAP, audit logs, on-prem LLM support |

### v0.3: Desktop Chat Extraction (NEW)

While everyone else tries to hack local databases (LevelDB, IndexedDB, JSONL), we walked through the **front door**:

```bash
# Extract ALL your Claude Desktop conversations to Markdown
python cli/extract_desktop_chats.py --output ./my-chats

# Silent mode for automation (hooks, scheduled tasks)
python cli/extract_desktop_chats.py --quiet
```

**How it works:**
1. Reads the session cookie from Claude Desktop's Electron app (Windows DPAPI + AES-256-GCM)
2. Calls the official `claude.ai` API -- your data, your app, your cookies
3. Converts every conversation to clean, searchable Markdown
4. Incremental: only downloads new conversations on subsequent runs

**Why this matters:** 10+ developers tried reverse-engineering local file formats. We use the same API that claude.ai uses in your browser. No hack, no reverse engineering, no breaking changes when Anthropic updates their app.

**This approach scales to ANY Electron/Chromium AI app:** ChatGPT, Gemini, DeepSeek -- they all have APIs + session cookies.

Requirements: `pip install pycryptodome pywin32` (Windows only for now)

---

### v2.0 Preview: "Your AI Knowledge Graph"

The next major version transforms Schroedinger Sync from a session dumper into a **cross-AI knowledge platform**:

- **Multi-AI Parsing** -- Claude, Gemini, DeepSeek, ChatGPT in one unified store
- **Fact Extraction** -- Automatically extracts people, dates, decisions, corrections from sessions
- **Conflict Detection** -- Flags when two AIs contradict each other (Pro)
- **Knowledge Graph** -- Structured facts with confidence levels, sources, and time decay (Pro)
- **AES-256 Encryption** -- Sensitive knowledge encrypted at rest, OS keyring integration (Pro)
- **One-Click Export** -- Format-specific output for each AI tool

**Pricing model:** Open Core. The Free tier covers multi-AI parsing, memory files, and clipboard export. Pro adds the knowledge graph, conflict detection, LLM-assisted extraction, and encryption.

---

## Why Not Just Use the API Directly?

A common counterargument: *"Just build a bot that talks to the API -- no sync needed."*

That's like saying a bicycle is better than a car because it has fewer parts. A direct API wrapper trades away:

- **Artifacts** -- Claude Desktop's live-editing pane for code, documents, prototypes
- **Projects** -- Persistent workspace organization with curated knowledge
- **MCP Ecosystem** -- Filesystem, GitHub, databases, Sentry, Figma, and hundreds of MCP servers
- **Agentic Code Loop** -- Claude Code writes code, runs tests, sees failures, fixes bugs autonomously
- **Automatic Context Management** -- Built-in conversation history, compaction, and memory

Schroedinger Sync is for power users who need **both** Desktop's rich UI **and** Code's agentic capabilities -- and want context to flow between them. And between every other AI they use.

---

## Origin Story

> *"I had a breakdown because ChatGPT confidently lied about something Claude had already corrected three sessions ago. The correction existed -- but the AIs didn't talk to each other. So I built Schroedinger Sync."*

This project started as a personal fix. A manual system of memory files, topic files, correction tables, and rules that kept multiple AI instances aligned. It worked. v2.0 automates exactly that.

---

## Requirements

- **VS Code Extension:** VS Code 1.85+
- **Python CLI:** Python 3.10+ (zero external dependencies)
- **Platform:** Windows, macOS, Linux

---

## Contributing

PRs are welcome. Check the [issues](https://github.com/KeilerHirsch/schroedinger-sync/issues) for open tasks or propose new features.

For bugs and feedback, please open an issue on GitHub.

---

## License

MIT -- see [LICENSE](LICENSE)

## Author

**KeilerHirsch** ([@KeilerHirsch](https://github.com/KeilerHirsch))

Built with Claude Code. Because Schroedinger Sync didn't exist yet.
