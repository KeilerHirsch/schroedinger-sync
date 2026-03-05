import * as fs from "fs";
import * as path from "path";
import * as crypto from "crypto";

export interface SyncedSession {
  hash: string;
  output: string;
  turns: number;
  syncedAt: string;
}

export interface SyncState {
  syncedSessions: Record<string, SyncedSession>;
  lastSync: string | null;
}

export function loadState(statePath: string): SyncState {
  try {
    if (fs.existsSync(statePath)) {
      const raw = fs.readFileSync(statePath, "utf-8");
      const data = JSON.parse(raw);
      return {
        syncedSessions: data.synced_sessions ?? data.syncedSessions ?? {},
        lastSync: data.last_sync ?? data.lastSync ?? null,
      };
    }
  } catch {
    // Corrupted state file — start fresh
  }
  return { syncedSessions: {}, lastSync: null };
}

export function saveState(statePath: string, state: SyncState): void {
  state.lastSync = new Date().toISOString();
  const dir = path.dirname(statePath);
  if (!fs.existsSync(dir)) {
    fs.mkdirSync(dir, { recursive: true });
  }
  const data = {
    synced_sessions: state.syncedSessions,
    last_sync: state.lastSync,
  };
  fs.writeFileSync(statePath, JSON.stringify(data, null, 2), "utf-8");
}

export function fileHash(filePath: string): string {
  const stat = fs.statSync(filePath);
  const input = `${stat.size}:${stat.mtimeMs}`;
  return crypto.createHash("md5").update(input).digest("hex");
}
