#!/usr/bin/env python3
"""
Schroedinger Sync v0.1 — Claude Code Session Sync

Reads Claude Code JSONL session transcripts and generates readable Markdown summaries.
Optionally commits to Git.

https://github.com/KeilerHirsch/schroedinger-sync

Author: KeilerHirsch
License: MIT
"""

import argparse
import json
import hashlib
import logging
import os
import re
import subprocess
import sys
from pathlib import Path
from datetime import datetime, timezone

VERSION = "0.1.0"

log = logging.getLogger("schroedinger")


# === CONFIGURATION ===

def get_sessions_dir():
    return Path(os.environ.get("USERPROFILE", "") or Path.home()) / ".claude" / "projects"


def get_state_path():
    return Path(os.environ.get("USERPROFILE", "") or Path.home()) / ".claude" / ".schroedinger_state.json"


def load_state(state_path):
    """Load sync state with recovery for corrupted files."""
    try:
        if state_path.exists():
            with open(state_path, "r", encoding="utf-8") as f:
                data = json.load(f)
            return {
                "synced_sessions": data.get("synced_sessions", {}),
                "last_sync": data.get("last_sync"),
            }
    except (json.JSONDecodeError, KeyError, ValueError) as e:
        log.warning("Corrupted state file, starting fresh: %s", e)
    return {"synced_sessions": {}, "last_sync": None}


def save_state(state_path, state):
    """Save sync state."""
    state["last_sync"] = datetime.now(timezone.utc).isoformat()
    state_path.parent.mkdir(parents=True, exist_ok=True)
    with open(state_path, "w", encoding="utf-8") as f:
        json.dump(state, f, indent=2, ensure_ascii=False)


def find_session_files(sessions_dir):
    """Find all JSONL session files across all projects (2-level deep)."""
    sessions = []
    if not sessions_dir.exists():
        log.warning("Sessions directory not found: %s", sessions_dir)
        return sessions

    for project_dir in sessions_dir.iterdir():
        if not project_dir.is_dir():
            continue

        for entry in project_dir.iterdir():
            if entry.is_dir():
                # Level 2: projects/<hash>/<session-uuid>.jsonl
                for jsonl_file in entry.glob("*.jsonl"):
                    stat = jsonl_file.stat()
                    sessions.append({
                        "path": jsonl_file,
                        "project": project_dir.name,
                        "session_id": jsonl_file.stem,
                        "size": stat.st_size,
                        "modified": datetime.fromtimestamp(stat.st_mtime, tz=timezone.utc),
                    })
            elif entry.suffix == ".jsonl":
                # Level 1 fallback: projects/<folder>/<file>.jsonl
                stat = entry.stat()
                sessions.append({
                    "path": entry,
                    "project": project_dir.name,
                    "session_id": entry.stem,
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

    skipped_lines = 0
    with open(jsonl_path, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                skipped_lines += 1
                continue

            msg_type = obj.get("type")
            timestamp = obj.get("timestamp")

            if timestamp:
                if metadata["start_time"] is None or timestamp < metadata["start_time"]:
                    metadata["start_time"] = timestamp
                if metadata["end_time"] is None or timestamp > metadata["end_time"]:
                    metadata["end_time"] = timestamp

            if msg_type == "system":
                metadata["cwd"] = metadata["cwd"] or obj.get("cwd")
                metadata["git_branch"] = metadata["git_branch"] or obj.get("gitBranch")
                metadata["version"] = metadata["version"] or obj.get("version")
                metadata["slug"] = metadata["slug"] or obj.get("slug")

            elif msg_type == "user":
                msg = obj.get("message", {})
                content = msg.get("content", "")
                if isinstance(content, list):
                    text_parts = [p.get("text", "") for p in content if isinstance(p, dict) and p.get("type") == "text"]
                    content = "\n".join(text_parts)
                if content and content.strip():
                    messages.append({
                        "role": "user",
                        "content": content.strip(),
                        "timestamp": timestamp,
                    })
                    metadata["total_turns"] += 1

            elif msg_type == "assistant":
                msg = obj.get("message", {})
                content_blocks = msg.get("content", [])
                text_parts = []
                for block in (content_blocks if isinstance(content_blocks, list) else []):
                    if isinstance(block, dict):
                        if block.get("type") == "text":
                            text_parts.append(block.get("text", ""))
                        elif block.get("type") == "tool_use":
                            metadata["tool_uses"].append(block.get("name", "unknown"))
                if text_parts:
                    messages.append({
                        "role": "assistant",
                        "content": "\n".join(text_parts).strip(),
                        "timestamp": timestamp,
                    })

    if skipped_lines > 0:
        log.warning("%d malformed JSONL line(s) skipped in %s", skipped_lines, jsonl_path.name)

    return metadata, messages


def generate_summary(metadata, messages, max_preview=500):
    """Generate a Markdown summary of a session."""
    lines = []

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
    lines.append("")
    lines.append(f"**Session ID:** `{metadata['session_id']}`")
    lines.append(f"**Date:** {date_str}")
    lines.append(f"**Project:** `{metadata.get('cwd', '?')}`")
    lines.append(f"**Branch:** {metadata.get('git_branch', '?')}")
    lines.append(f"**Claude Code:** v{metadata.get('version', '?')}")
    lines.append(f"**Turns:** {metadata['total_turns']}")
    lines.append("")

    if metadata["tool_uses"]:
        tool_counts = {}
        for t in metadata["tool_uses"]:
            tool_counts[t] = tool_counts.get(t, 0) + 1
        lines.append("## Tool Usage")
        lines.append("")
        for tool, count in sorted(tool_counts.items(), key=lambda x: -x[1])[:15]:
            lines.append(f"- {tool}: {count}x")
        lines.append("")

    lines.append("## Conversation Flow")
    lines.append("")
    user_msgs = [m for m in messages if m["role"] == "user"]
    for i, msg in enumerate(user_msgs[:50], 1):
        preview = msg["content"][:200].replace("\n", " ")
        if len(msg["content"]) > 200:
            preview += "..."
        lines.append(f"{i}. **User:** {preview}")

    if len(user_msgs) > 50:
        lines.append(f"\n*... and {len(user_msgs) - 50} more messages*")

    lines.append("")

    assistant_msgs = [m for m in messages if m["role"] == "assistant"]
    if assistant_msgs:
        lines.append("## First Response (excerpt)")
        lines.append("")
        first = assistant_msgs[0]["content"][:max_preview]
        if len(assistant_msgs[0]["content"]) > max_preview:
            first += "\n\n*[...truncated]*"
        lines.append(first)
        lines.append("")

        if len(assistant_msgs) > 1:
            lines.append("## Last Response (excerpt)")
            lines.append("")
            last = assistant_msgs[-1]["content"][:max_preview]
            if len(assistant_msgs[-1]["content"]) > max_preview:
                last += "\n\n*[...truncated]*"
            lines.append(last)
            lines.append("")

    lines.append("---")
    now_str = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
    lines.append(f"*Generated by Schroedinger Sync v{VERSION} | {now_str}*")

    return "\n".join(lines)


def file_hash(filepath):
    """Quick hash of file for change detection."""
    stat = filepath.stat()
    return hashlib.md5(f"{stat.st_size}:{stat.st_mtime}".encode()).hexdigest()


def git_commit(output_dir, message):
    """Auto-commit sync output in a git repo."""
    try:
        cwd = str(Path(output_dir).parent)
        add_result = subprocess.run(
            ["git", "add", str(output_dir)],
            cwd=cwd, capture_output=True, text=True, timeout=30
        )
        if add_result.returncode != 0:
            log.error("git add failed: %s", add_result.stderr[:200])
            return
        result = subprocess.run(
            ["git", "commit", "-m", message],
            cwd=cwd, capture_output=True, text=True, timeout=30
        )
        if result.returncode == 0:
            log.info("Committed: %s", message)
        elif "nothing to commit" in (result.stdout + result.stderr):
            log.info("Nothing to commit")
        else:
            log.error("Commit failed: %s", result.stderr[:200])
    except (OSError, subprocess.TimeoutExpired) as e:
        log.error("Git error: %s", e)


def sync(output_dir, git_auto_commit, max_sessions):
    """Main sync function. Returns (new_synced, total_found)."""
    sessions_dir = get_sessions_dir()
    state_path = get_state_path()

    state = load_state(state_path)
    sessions = find_session_files(sessions_dir)

    if not sessions:
        log.info("No sessions found.")
        return 0, 0

    log.info("%d session(s) found", len(sessions))

    output_dir.mkdir(parents=True, exist_ok=True)

    processed = 0
    new_synced = 0

    for session in sessions:
        if max_sessions > 0 and processed >= max_sessions:
            break

        session_id = session["session_id"]
        current_hash = file_hash(session["path"])

        if session_id in state["synced_sessions"]:
            if state["synced_sessions"][session_id].get("hash") == current_hash:
                continue

        if session["size"] < 1024:
            continue

        processed += 1
        log.info("Processing: %s... (%d KB)", session_id[:12], session["size"] // 1024)

        try:
            metadata, messages = parse_session(session["path"])

            if metadata["total_turns"] == 0:
                log.debug("No user messages, skipped: %s", session_id[:12])
                continue

            summary = generate_summary(metadata, messages)

            slug = metadata.get("slug") or session_id[:12]
            date_prefix = session["modified"].strftime("%Y%m%d")
            safe_slug = re.sub(r'[<>:"/\\|?*\s]', '-', slug)
            output_file = output_dir / f"{date_prefix}_{safe_slug}.md"

            with open(output_file, "w", encoding="utf-8") as f:
                f.write(summary)

            log.info("  -> %s (%d turns, %d tool calls)",
                     output_file.name, metadata["total_turns"], len(metadata["tool_uses"]))

            state["synced_sessions"][session_id] = {
                "hash": current_hash,
                "output": str(output_file),
                "turns": metadata["total_turns"],
                "synced_at": datetime.now(timezone.utc).isoformat(),
            }
            new_synced += 1

        except (OSError, json.JSONDecodeError, ValueError, KeyError) as e:
            log.error("Failed: %s — %s", session_id[:12], e)

    save_state(state_path, state)

    log.info("Done: %d new session(s) synced to %s", new_synced, output_dir)

    if git_auto_commit and new_synced > 0:
        git_commit(
            str(output_dir),
            f"sync: {new_synced} session(s) synced by Schroedinger Sync"
        )

    return new_synced, len(sessions)


def main():
    parser = argparse.ArgumentParser(
        prog="sync_schroedinger",
        description=f"Schroedinger Sync v{VERSION} — Claude Code Session Sync",
    )
    parser.add_argument("--git", action="store_true",
                        help="Auto-commit after sync")
    parser.add_argument("--max", type=int, default=0, metavar="N",
                        help="Max sessions to process (0 = all)")
    parser.add_argument("--output", type=Path,
                        default=Path(os.environ.get("SCHROEDINGER_OUTPUT", "./sync")),
                        help="Output directory for summaries (default: ./sync)")
    parser.add_argument("--verbose", "-v", action="store_true",
                        help="Verbose output (DEBUG level)")
    parser.add_argument("--quiet", "-q", action="store_true",
                        help="Suppress output (only errors)")
    parser.add_argument("--version", action="version",
                        version=f"Schroedinger Sync v{VERSION}")
    args = parser.parse_args()

    if args.quiet:
        level = logging.ERROR
    elif args.verbose:
        level = logging.DEBUG
    else:
        level = logging.INFO
    logging.basicConfig(
        level=level,
        format="[%(levelname)s] %(message)s",
    )

    git_auto_commit = args.git or os.environ.get("SCHROEDINGER_GIT_COMMIT", "false").lower() == "true"

    new_synced, total = sync(args.output, git_auto_commit, args.max)

    if total == 0:
        sys.exit(3)  # No sessions found at all
    sys.exit(0)


if __name__ == "__main__":
    main()
