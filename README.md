# Schroedinger Sync

**Your AI instances finally share a brain.**

> *"Is your AI synchronized? Yes. And no. -- until you install Schroedinger Sync."*

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![VS Code](https://img.shields.io/badge/VS%20Code-Extension-007ACC?logo=visualstudiocode)](https://marketplace.visualstudio.com/items?itemName=KeilerHirsch.schroedinger-sync)
[![Python 3.10+](https://img.shields.io/badge/Python-3.10+-3776AB?logo=python&logoColor=white)](cli/)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://github.com/KeilerHirsch/schroedinger-sync/pulls)

---

## TL;DR -- What Does This Do?

You use Claude Desktop to brainstorm. You use Claude Code in VS Code to actually build things. You use Gemini to double-check facts. **None of them know what the others said.**

Schroedinger Sync connects them. Every AI you use gets access to what every other AI already figured out. No more repeating yourself. No more "but I already told Claude about this 3 sessions ago."

**Before Schroedinger Sync:**
- You explain the same project context to every AI, every session
- Claude Desktop builds a plan, Claude Code has no idea it exists
- Gemini corrects a fact, but Claude keeps using the wrong version
- You hit the token limit and lose everything

**After Schroedinger Sync:**
- All your AI tools share one knowledge base
- Desktop brainstorm sessions are automatically readable by Code
- Facts, decisions, and corrections flow between all your AIs
- Your context survives token limits, session restarts, and app switches

**100% private.** Everything stays on your computer. No cloud. No account. No server. GDPR by design.

---

## The Problem (In Detail)

Every AI instance starts from zero. **Claude Desktop doesn't talk to Claude Code. Neither talks to Gemini. Or ChatGPT. Or DeepSeek.**

This isn't a minor inconvenience. There are [at least 6 open feature requests](https://github.com/anthropics/claude-code/issues) on Anthropic's official repo for exactly this -- Desktop-to-Code session sync. Anthropic's own workarounds (`&` prefix, `--teleport`, `claude mcp serve`) are all **one-way**. No one has solved bidirectional sync yet.

**Other projects have tried:**

| Project | Approach | Limitation |
|---------|----------|------------|
| [Claude_Automation](https://github.com/dimascior/Claude_Automation) | File-based messaging (Windows/WSL) | Platform-locked, no multi-AI |
| [claude-sync](https://github.com/tawanorg/claude-sync) | Cloud sync of `~/.claude/` | Breaks on different filesystem paths |
| [claude-memory-mcp](https://github.com/randall-gross/claude-memory-mcp) | Shared SQLite knowledge graph | Memory only, no session continuity |

Schroedinger Sync takes a different approach entirely.

---

## How It Works

Instead of trying to sync live sessions (which every API restricts), Schroedinger Sync builds a **shared knowledge layer** across all your AI tools.

```
        Your AI Tools
        |         |         |         |
     Claude    Gemini   DeepSeek   ChatGPT
        |         |         |         |
        +----+----+----+----+----+----+
             |              |
      +------------+  +------------+
      | Memory     |  | Knowledge  |
      | (Markdown) |  | Graph      |
      | Free       |  | Pro        |
      +------------+  +------------+
             |              |
        +------------------------+
        |  Conflict Detector     |
        | "Claude says A,        |
        |  Gemini says B -- who  |
        |  is right?"            |
        +------------------------+
             |
        Feed into any AI
```

1. **Reads** your conversation history from Claude Code, Claude Desktop, Gemini, ChatGPT
2. **Extracts** the important stuff: facts, decisions, corrections, open tasks
3. **Detects conflicts** when two AIs disagree about the same thing
4. **Generates** clean topic files -- your personal AI memory that works everywhere
5. **Feeds back** into any AI tool so it knows what the others already figured out

**Privacy-first:** Everything stays local. No cloud. No server. No account. GDPR by design.

---

## Quick Start

### VS Code Extension (v0.2) -- Recommended

Install from the [VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=KeilerHirsch.schroedinger-sync) or search "Schroedinger Sync" in Extensions.

Works out of the box -- statusbar button appears, auto-sync watches for new sessions.

### Python CLI (v0.1)

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

## What It Does Today (v0.3)

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
| **v0.1** | Done | Python CLI -- session parsing and Markdown export |
| **v0.2** | Done | VS Code Extension -- auto-sync, statusbar integration |
| **v0.3** | **New!** | **Desktop Chat Extraction** -- reads Claude Desktop conversations via official API (DPAPI + AES-256-GCM) |
| **v1.0** | In Design | **AI Knowledge Graph** -- multi-AI parsing, fact extraction, conflict detection, encrypted storage |
| **v1.5** | Planned | Browser extension, additional AI format support |
| **v2.0** | Planned | Enterprise self-hosted, SSO/LDAP, audit logs, on-prem LLM support |

### v0.3: Desktop Chat Extraction (NEW)

**The breakthrough:** Your Claude Desktop conversations are trapped inside the app. You can't search them, export them, or share them with Claude Code. Until now.

**What it does:**
1. Reads your login session from the Claude Desktop app (securely, using your OS credentials)
2. Downloads all your conversations through the same API that claude.ai uses in your browser
3. Saves each conversation as a clean, searchable Markdown file
4. Only downloads NEW conversations on subsequent runs (incremental)

**This is NOT a hack.** It reads YOUR cookies from YOUR app to access YOUR data through the official API. The same approach works for any AI desktop app -- ChatGPT, Gemini, DeepSeek.

**Why this matters:** 10+ developers tried cracking open local database files. We just walked through the front door.

> The Desktop Extraction engine is available as part of **Schroedinger Sync Pro** (coming with v1.0). [Join the waitlist](https://github.com/KeilerHirsch/schroedinger-sync/issues) to get early access.

---

### v1.0 Preview: "Your AI Knowledge Graph"

The first major release transforms Schroedinger Sync from a session dumper into a **cross-AI knowledge platform**:

- **Multi-AI Parsing** -- Claude, Gemini, DeepSeek, ChatGPT in one unified store
- **Fact Extraction** -- Automatically extracts people, dates, decisions, corrections from sessions
- **Conflict Detection** -- Flags when two AIs contradict each other (Pro)
- **Knowledge Graph** -- Structured facts with confidence levels, sources, and time decay (Pro)
- **AES-256 Encryption** -- Sensitive knowledge encrypted at rest, OS keyring integration (Pro)
- **One-Click Export** -- Format-specific output for each AI tool

**Pricing model:** Open Core.

| | Free | Pro |
|---|---|---|
| Multi-AI session parsing | Yes | Yes |
| Memory files & Markdown export | Yes | Yes |
| Clipboard export | Yes | Yes |
| **Desktop Chat Extraction** | -- | **Yes** |
| **Knowledge Graph** | -- | **Yes** |
| **Conflict Detection** | -- | **Yes** |
| **AES-256 Encryption** | -- | **Yes** |
| **LLM-assisted extraction** | -- | **Yes** |

---

## "Why Not Just Use One AI Tool?"

A fair question. If you only use ONE AI app, you don't need Schroedinger Sync. But most power users don't:

- **Claude Desktop** is great for brainstorming, documents, and visual artifacts
- **Claude Code (VS Code)** is great for actually building software -- it writes, tests, and fixes code autonomously
- **Gemini** has Google Search built in for fact-checking
- **ChatGPT** has a massive user base and plugin ecosystem

Each tool has strengths the others don't. The problem is they don't share context. Schroedinger Sync is the bridge.

**"But why not just use the API directly?"** Because a direct API wrapper loses everything that makes these tools powerful: Desktop's artifacts, Code's autonomous coding loop, Projects, MCP integrations, conversation history. You'd be trading a car for a bicycle just because it has fewer parts.

---

## Origin Story

> *"ChatGPT confidently repeated something Claude had already corrected three sessions ago. The correction existed -- but the AIs didn't talk to each other. I was done copy-pasting context between 4 different AI tools. So I built Schroedinger Sync."*

This project started as a personal fix. A manual system of memory files, topic files, and rules that kept multiple AI instances aligned. It worked so well that automating it became the obvious next step. That's v1.0.

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
