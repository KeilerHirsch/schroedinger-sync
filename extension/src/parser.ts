import * as fs from "fs";
import * as path from "path";
import * as readline from "readline";

export interface SessionFile {
  path: string;
  project: string;
  sessionId: string;
  size: number;
  modified: Date;
}

export interface SessionMessage {
  role: "user" | "assistant";
  content: string;
  timestamp: string | null;
}

export interface SessionMetadata {
  sessionId: string;
  cwd: string | null;
  gitBranch: string | null;
  version: string | null;
  slug: string | null;
  startTime: string | null;
  endTime: string | null;
  totalTurns: number;
  toolUses: string[];
}

export function findSessionFiles(sessionsDir: string): SessionFile[] {
  const sessions: SessionFile[] = [];

  if (!fs.existsSync(sessionsDir)) {
    return sessions;
  }

  const topEntries = fs.readdirSync(sessionsDir, { withFileTypes: true });
  for (const topEntry of topEntries) {
    if (!topEntry.isDirectory()) {
      continue;
    }
    const projectDir = path.join(sessionsDir, topEntry.name);
    const subEntries = fs.readdirSync(projectDir, { withFileTypes: true });
    for (const subEntry of subEntries) {
      if (subEntry.isDirectory()) {
        // Level 2: projects/<hash>/<session-uuid>.jsonl
        const subDir = path.join(projectDir, subEntry.name);
        const files = fs.readdirSync(subDir);
        for (const file of files) {
          if (!file.endsWith(".jsonl")) {
            continue;
          }
          const filePath = path.join(subDir, file);
          const stat = fs.statSync(filePath);
          sessions.push({
            path: filePath,
            project: topEntry.name,
            sessionId: path.basename(file, ".jsonl"),
            size: stat.size,
            modified: new Date(stat.mtimeMs),
          });
        }
      } else if (subEntry.name.endsWith(".jsonl")) {
        // Level 1 fallback: projects/<folder>/<file>.jsonl
        const filePath = path.join(projectDir, subEntry.name);
        const stat = fs.statSync(filePath);
        sessions.push({
          path: filePath,
          project: topEntry.name,
          sessionId: path.basename(subEntry.name, ".jsonl"),
          size: stat.size,
          modified: new Date(stat.mtimeMs),
        });
      }
    }
  }

  sessions.sort((a, b) => b.modified.getTime() - a.modified.getTime());
  return sessions;
}

export async function parseSession(
  jsonlPath: string
): Promise<{ metadata: SessionMetadata; messages: SessionMessage[] }> {
  const messages: SessionMessage[] = [];
  const metadata: SessionMetadata = {
    sessionId: path.basename(jsonlPath, ".jsonl"),
    cwd: null,
    gitBranch: null,
    version: null,
    slug: null,
    startTime: null,
    endTime: null,
    totalTurns: 0,
    toolUses: [],
  };

  const fileStream = fs.createReadStream(jsonlPath, { encoding: "utf-8" });
  const rl = readline.createInterface({ input: fileStream, crlfDelay: Infinity });

  for await (const line of rl) {
    const trimmed = line.trim();
    if (!trimmed) {
      continue;
    }

    let obj: any;
    try {
      obj = JSON.parse(trimmed);
    } catch {
      continue;
    }

    const msgType: string | undefined = obj.type;
    const timestamp: string | undefined = obj.timestamp;

    if (timestamp) {
      if (metadata.startTime === null || timestamp < metadata.startTime) {
        metadata.startTime = timestamp;
      }
      if (metadata.endTime === null || timestamp > metadata.endTime) {
        metadata.endTime = timestamp;
      }
    }

    if (msgType === "system") {
      metadata.cwd = metadata.cwd ?? obj.cwd ?? null;
      metadata.gitBranch = metadata.gitBranch ?? obj.gitBranch ?? null;
      metadata.version = metadata.version ?? obj.version ?? null;
      metadata.slug = metadata.slug ?? obj.slug ?? null;
    } else if (msgType === "user") {
      const msg = obj.message ?? {};
      let content: string = "";
      const rawContent = msg.content;

      if (Array.isArray(rawContent)) {
        const textParts = rawContent
          .filter((p: any) => typeof p === "object" && p?.type === "text")
          .map((p: any) => p.text ?? "");
        content = textParts.join("\n");
      } else if (typeof rawContent === "string") {
        content = rawContent;
      }

      content = content.trim();
      if (content) {
        messages.push({ role: "user", content, timestamp: timestamp ?? null });
        metadata.totalTurns++;
      }
    } else if (msgType === "assistant") {
      const msg = obj.message ?? {};
      const contentBlocks = Array.isArray(msg.content) ? msg.content : [];
      const textParts: string[] = [];

      for (const block of contentBlocks) {
        if (typeof block !== "object" || block === null) {
          continue;
        }
        if (block.type === "text") {
          textParts.push(block.text ?? "");
        } else if (block.type === "tool_use") {
          metadata.toolUses.push(block.name ?? "unknown");
        }
      }

      const text = textParts.join("\n").trim();
      if (text) {
        messages.push({ role: "assistant", content: text, timestamp: timestamp ?? null });
      }
    }
  }

  return { metadata, messages };
}
