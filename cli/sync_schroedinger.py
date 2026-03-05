#!/usr/bin/env python3
"""
Schroedinger Sync v1.0 - Automatic sync between Claude Desktop and VS Code
https://github.com/KeilerHirsch/schroedinger-sync

Reads Claude Code JSONL session transcripts and generates readable Markdown summaries.
Auto-updates CLAUDE.md memory and optionally commits to Git.

Author: Michael Popow (@KeilerHirsch)
License: MIT
"""

import json
import os
import sys
import hashlib
import subprocess
from pathlib import Path
from datetime import datetime, timezone


# === CONFIGURATION ===

# Claude Code sessions directory
CLAUDE_CODE_SESSIONS = Path(os.environ.get("USERPROFILE", "~")) / ".claude" / "projects"

# Output directory for synced session summaries
SYNC_OUTPUT_DIR = Path(os.environ.get("SCHROEDINGER_OUTPUT",
    r"F:\Users\KeilerHirsch\Documents\Vibe_Coding_VSCODE\sync"))

# CLAUDE.md to update (global memory)
CLAUDE_MD_PATH = Path(os.environ.get("USERPROFILE", "~")) / ".claude" / "CLAUDE.md"

# Auto-memory directory
AUTO_MEMORY_DIR = CLAUDE_CODE_SESSIONS  # will be resolved per-project

# Git auto-commit after sync
GIT_AUTO_COMMIT = os.environ.get("SCHROEDINGER_GIT_COMMIT", "false").lower() == "true"

# Max sessions to process (0 = all)
MAX_SESSIONS = int(os.environ.get("SCHROEDINGER_MAX_SESSIONS", "0"))

# State file to track what we've already synced
STATE_FILE = Path(os.environ.get("USERPROFILE", "~")) / ".claude" / ".schroedinger_state.json"


def load_state():
    """Load sync state (which sessions were already processed)."""
    if STATE_FILE.exists():
        with open(STATE_FILE, "r", encoding="utf-8") as f:
            return json.load(f)
    return {"synced_sessions": {}, "last_sync": None}


def save_state(state):
    """Save sync state."""
    state["last_sync"] = datetime.now(timezone.utc).isoformat()
    STATE_FILE.parent.mkdir(parents=True, exist_ok=True)
    with open(STATE_FILE, "w", encoding="utf-8") as f:
        json.dump(state, f, indent=2, ensure_ascii=False)


def find_session_files():
    """Find all JSONL session files across all projects."""
    sessions = []
    if not CLAUDE_CODE_SESSIONS.exists():
        print(f"[WARN] Sessions directory not found: {CLAUDE_CODE_SESSIONS}")
        return sessions

    for project_dir in CLAUDE_CODE_SESSIONS.iterdir():
        if not project_dir.is_dir():
            continue
        for jsonl_file in project_dir.glob("*.jsonl"):
            stat = jsonl_file.stat()
            sessions.append({
                "path": jsonl_file,
                "project": project_dir.name,
                "session_id": jsonl_file.stem,
                "size": stat.st_size,
                "modified": datetime.fromtimestamp(stat.st_mtime, tz=timezone.utc),
            })

    sessions.sort(key=lambda s: s["modified"], reverse=True)
    return sessions


def parse_session(jsonl_path):
    """Parse a Claude Code JSONL session into structured data."""
    messages = []
    metadata = {
        "session_id": jsonl_path.stem,
        "cwd": None,
        "git_branch": None,
        "version": None,
        "slug": None,
        "start_time": None,
        "end_time": None,
        "total_turns": 0,
        "tool_uses": [],
    }

    with open(jsonl_path, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue

            msg_type = obj.get("type")
            timestamp = obj.get("timestamp")

            # Track time range
            if timestamp:
                if metadata["start_time"] is None or timestamp < metadata["start_time"]:
                    metadata["start_time"] = timestamp
                if metadata["end_time"] is None or timestamp > metadata["end_time"]:
                    metadata["end_time"] = timestamp

            # Extract metadata from system messages
            if msg_type == "system":
                metadata["cwd"] = metadata["cwd"] or obj.get("cwd")
                metadata["git_branch"] = metadata["git_branch"] or obj.get("gitBranch")
                metadata["version"] = metadata["version"] or obj.get("version")
                metadata["slug"] = metadata["slug"] or obj.get("slug")

            # Extract user messages
            elif msg_type == "user":
                msg = obj.get("message", {})
                content = msg.get("content", "")
                if isinstance(content, list):
                    # Multi-part content (text + images etc.)
                    text_parts = [p.get("text", "") for p in content if isinstance(p, dict) and p.get("type") == "text"]
                    content = "\n".join(text_parts)
                if content and content.strip():
                    messages.append({
                        "role": "user",
                        "content": content.strip(),
                        "timestamp": timestamp,
                    })
                    metadata["total_turns"] += 1

            # Extract assistant messages
            elif msg_type == "assistant":
                msg = obj.get("message", {})
                content_blocks = msg.get("content", [])
                text_parts = []
                for block in (content_blocks if isinstance(content_blocks, list) else []):
                    if isinstance(block, dict):
                        if block.get("type") == "text":
                            text_parts.append(block.get("text", ""))
                        elif block.get("type") == "tool_use":
                            tool_name = block.get("name", "unknown")
                            metadata["tool_uses"].append(tool_name)
                if text_parts:
                    messages.append({
                        "role": "assistant",
                        "content": "\n".join(text_parts).strip(),
                        "timestamp": timestamp,
                    })

    return metadata, messages


def generate_summary(metadata, messages, max_preview=500):
    """Generate a Markdown summary of a session."""
    lines = []

    # Header
    slug = metadata.get("slug", "unknown-session")
    start = metadata.get("start_time", "?")
    if start and start != "?":
        try:
            dt = datetime.fromisoformat(start.replace("Z", "+00:00"))
            date_str = dt.strftime("%Y-%m-%d %H:%M UTC")
        except (ValueError, TypeError):
            date_str = start
    else:
        date_str = "unknown"

    lines.append(f"# Session: {slug}")
    lines.append(f"")
    lines.append(f"**Session ID:** `{metadata['session_id']}`")
    lines.append(f"**Datum:** {date_str}")
    lines.append(f"**Projekt:** `{metadata.get('cwd', '?')}`")
    lines.append(f"**Branch:** {metadata.get('git_branch', '?')}")
    lines.append(f"**Claude Code:** v{metadata.get('version', '?')}")
    lines.append(f"**Turns:** {metadata['total_turns']}")
    lines.append(f"")

    # Tool usage statistics
    if metadata["tool_uses"]:
        tool_counts = {}
        for t in metadata["tool_uses"]:
            tool_counts[t] = tool_counts.get(t, 0) + 1
        lines.append(f"## Tool-Nutzung")
        lines.append(f"")
        for tool, count in sorted(tool_counts.items(), key=lambda x: -x[1])[:15]:
            lines.append(f"- {tool}: {count}x")
        lines.append(f"")

    # Conversation flow (user messages as outline)
    lines.append(f"## Gesprächsverlauf")
    lines.append(f"")
    user_msgs = [m for m in messages if m["role"] == "user"]
    for i, msg in enumerate(user_msgs[:50], 1):  # Max 50 user messages
        preview = msg["content"][:200].replace("\n", " ")
        if len(msg["content"]) > 200:
            preview += "..."
        lines.append(f"{i}. **User:** {preview}")

    if len(user_msgs) > 50:
        lines.append(f"\n*... und {len(user_msgs) - 50} weitere Nachrichten*")

    lines.append(f"")

    # Key assistant responses (first and last)
    assistant_msgs = [m for m in messages if m["role"] == "assistant"]
    if assistant_msgs:
        lines.append(f"## Erste Antwort (Auszug)")
        lines.append(f"")
        first = assistant_msgs[0]["content"][:max_preview]
        if len(assistant_msgs[0]["content"]) > max_preview:
            first += "\n\n*[...gekürzt]*"
        lines.append(first)
        lines.append(f"")

        if len(assistant_msgs) > 1:
            lines.append(f"## Letzte Antwort (Auszug)")
            lines.append(f"")
            last = assistant_msgs[-1]["content"][:max_preview]
            if len(assistant_msgs[-1]["content"]) > max_preview:
                last += "\n\n*[...gekürzt]*"
            lines.append(last)
            lines.append(f"")

    lines.append(f"---")
    lines.append(f"*Generiert von Schroedinger Sync v1.0 | {datetime.now().strftime('%Y-%m-%d %H:%M')}*")

    return "\n".join(lines)


def file_hash(path):
    """Quick hash of file for change detection."""
    return hashlib.md5(f"{path.stat().st_size}:{path.stat().st_mtime}".encode()).hexdigest()


def git_commit(repo_path, message):
    """Auto-commit changes in a git repo."""
    try:
        subprocess.run(["git", "add", "-A"], cwd=repo_path, capture_output=True, timeout=30)
        result = subprocess.run(
            ["git", "commit", "-m", message],
            cwd=repo_path, capture_output=True, text=True, timeout=30
        )
        if result.returncode == 0:
            print(f"[GIT] Committed: {message}")
        else:
            if "nothing to commit" in result.stdout:
                print(f"[GIT] Nothing to commit")
            else:
                print(f"[GIT] Commit failed: {result.stderr[:200]}")
    except Exception as e:
        print(f"[GIT] Error: {e}")


def sync():
    """Main sync function."""
    print(f"=== Schroedinger Sync v1.0 ===")
    print(f"Zeitpunkt: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    print()

    state = load_state()
    sessions = find_session_files()

    if not sessions:
        print("[INFO] Keine Sessions gefunden.")
        return

    print(f"[INFO] {len(sessions)} Session(s) gefunden")

    # Create output directory
    SYNC_OUTPUT_DIR.mkdir(parents=True, exist_ok=True)

    processed = 0
    new_synced = 0

    for session in sessions:
        if MAX_SESSIONS > 0 and processed >= MAX_SESSIONS:
            break

        session_id = session["session_id"]
        current_hash = file_hash(session["path"])

        # Skip if already synced and unchanged
        if session_id in state["synced_sessions"]:
            if state["synced_sessions"][session_id].get("hash") == current_hash:
                continue

        # Skip tiny sessions (< 1KB = probably just system messages)
        if session["size"] < 1024:
            continue

        processed += 1
        print(f"\n[SYNC] Verarbeite: {session_id[:12]}... ({session['size'] / 1024:.0f} KB)")

        try:
            metadata, messages = parse_session(session["path"])

            # Skip sessions with no actual conversation
            if metadata["total_turns"] == 0:
                print(f"  -> Keine User-Nachrichten, übersprungen")
                continue

            summary = generate_summary(metadata, messages)

            # Write summary file
            slug = metadata.get("slug") or session_id[:12]
            date_prefix = session["modified"].strftime("%Y%m%d")
            safe_slug = slug.replace(" ", "-").replace("/", "-")
            output_file = SYNC_OUTPUT_DIR / f"{date_prefix}_{safe_slug}.md"

            with open(output_file, "w", encoding="utf-8") as f:
                f.write(summary)

            print(f"  -> Geschrieben: {output_file.name}")
            print(f"     {metadata['total_turns']} Turns, {len(metadata['tool_uses'])} Tool-Aufrufe")

            # Update state
            state["synced_sessions"][session_id] = {
                "hash": current_hash,
                "output": str(output_file),
                "turns": metadata["total_turns"],
                "synced_at": datetime.now(timezone.utc).isoformat(),
            }
            new_synced += 1

        except Exception as e:
            print(f"  -> FEHLER: {e}")

    save_state(state)

    print(f"\n[DONE] {new_synced} neue Session(s) synchronisiert")
    print(f"       Ausgabe: {SYNC_OUTPUT_DIR}")

    # Git auto-commit if enabled
    if GIT_AUTO_COMMIT and new_synced > 0:
        git_commit(
            SYNC_OUTPUT_DIR.parent,
            f"sync: {new_synced} session(s) synced by Schroedinger Sync"
        )

    return new_synced


if __name__ == "__main__":
    # CLI arguments
    if "--help" in sys.argv or "-h" in sys.argv:
        print("Schroedinger Sync v1.0")
        print("Usage: python sync_schroedinger.py [OPTIONS]")
        print()
        print("Options:")
        print("  --git          Auto-commit after sync")
        print("  --max N        Max sessions to process")
        print("  --output DIR   Output directory for summaries")
        print("  --help         Show this help")
        sys.exit(0)

    if "--git" in sys.argv:
        GIT_AUTO_COMMIT = True

    if "--max" in sys.argv:
        idx = sys.argv.index("--max")
        if idx + 1 < len(sys.argv):
            MAX_SESSIONS = int(sys.argv[idx + 1])

    if "--output" in sys.argv:
        idx = sys.argv.index("--output")
        if idx + 1 < len(sys.argv):
            SYNC_OUTPUT_DIR = Path(sys.argv[idx + 1])

    sync()
