import * as vscode from "vscode";
import * as path from "path";
import * as fs from "fs";
import * as cp from "child_process";
import { findSessionFiles, parseSession } from "./parser";
import { generateSummary } from "./generator";
import { loadState, saveState, fileHash } from "./state";

let statusBarItem: vscode.StatusBarItem;
let fileWatcher: vscode.FileSystemWatcher | undefined;
let debounceTimer: ReturnType<typeof setTimeout> | undefined;

function getSessionsDir(): string {
  const home = process.env.USERPROFILE ?? process.env.HOME ?? "~";
  return path.join(home, ".claude", "projects");
}

function getStatePath(): string {
  const home = process.env.USERPROFILE ?? process.env.HOME ?? "~";
  return path.join(home, ".claude", ".schroedinger_state.json");
}

function getOutputDir(): string {
  const config = vscode.workspace.getConfiguration("schroedingerSync");
  const outputDir = config.get<string>("outputDir", "./sync");

  if (path.isAbsolute(outputDir)) {
    return outputDir;
  }

  const workspaceFolder = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (workspaceFolder) {
    return path.join(workspaceFolder, outputDir);
  }

  return path.resolve(outputDir);
}

function updateStatusBar(newCount: number): void {
  if (newCount > 0) {
    statusBarItem.text = `$(sync) Schroedinger: ${newCount} new`;
    statusBarItem.tooltip = `${newCount} new session(s) to sync. Click to sync now.`;
  } else {
    statusBarItem.text = "$(check) Schroedinger: synced";
    statusBarItem.tooltip = "All sessions are synced. Click to check again.";
  }
}

async function countNewSessions(): Promise<number> {
  const sessionsDir = getSessionsDir();
  const statePath = getStatePath();
  const state = loadState(statePath);
  const sessions = findSessionFiles(sessionsDir);

  let newCount = 0;
  for (const session of sessions) {
    if (session.size < 1024) {
      continue;
    }
    const currentHash = fileHash(session.path);
    const existing = state.syncedSessions[session.sessionId];
    if (!existing || existing.hash !== currentHash) {
      newCount++;
    }
  }
  return newCount;
}

async function runSync(): Promise<number> {
  const sessionsDir = getSessionsDir();
  const statePath = getStatePath();
  const outputDir = getOutputDir();
  const config = vscode.workspace.getConfiguration("schroedingerSync");
  const gitAutoCommit = config.get<boolean>("gitAutoCommit", false);

  const state = loadState(statePath);
  const sessions = findSessionFiles(sessionsDir);

  if (sessions.length === 0) {
    vscode.window.showInformationMessage("Schroedinger Sync: No sessions found.");
    return 0;
  }

  if (!fs.existsSync(outputDir)) {
    fs.mkdirSync(outputDir, { recursive: true });
  }

  let newSynced = 0;
  const outputChannel = vscode.window.createOutputChannel("Schroedinger Sync");

  for (const session of sessions) {
    if (session.size < 1024) {
      continue;
    }

    const currentHash = fileHash(session.path);
    const existing = state.syncedSessions[session.sessionId];
    if (existing && existing.hash === currentHash) {
      continue;
    }

    try {
      const { metadata, messages } = await parseSession(session.path);
      if (metadata.totalTurns === 0) {
        continue;
      }

      const summary = generateSummary(metadata, messages);
      const slug = metadata.slug ?? session.sessionId.slice(0, 12);
      const datePrefix = formatDate(session.modified);
      const safeSlug = slug.replace(/ /g, "-").replace(/\//g, "-");
      const outputFile = path.join(outputDir, `${datePrefix}_${safeSlug}.md`);

      fs.writeFileSync(outputFile, summary, "utf-8");
      outputChannel.appendLine(
        `[SYNC] ${safeSlug} — ${metadata.totalTurns} turns, ${metadata.toolUses.length} tool calls`
      );

      state.syncedSessions[session.sessionId] = {
        hash: currentHash,
        output: outputFile,
        turns: metadata.totalTurns,
        syncedAt: new Date().toISOString(),
      };
      newSynced++;
    } catch (err: any) {
      outputChannel.appendLine(`[ERROR] ${session.sessionId}: ${err.message}`);
    }
  }

  saveState(statePath, state);

  if (gitAutoCommit && newSynced > 0) {
    gitCommit(outputDir, `sync: ${newSynced} session(s) synced by Schroedinger Sync`);
  }

  if (newSynced > 0) {
    vscode.window.showInformationMessage(
      `Schroedinger Sync: ${newSynced} session(s) synced.`
    );
  }

  updateStatusBar(0);
  return newSynced;
}

function formatDate(date: Date): string {
  const y = date.getFullYear();
  const m = String(date.getMonth() + 1).padStart(2, "0");
  const d = String(date.getDate()).padStart(2, "0");
  return `${y}${m}${d}`;
}

function gitCommit(repoPath: string, message: string): void {
  try {
    const cwd = path.dirname(repoPath);
    cp.execSync("git add -A", { cwd, timeout: 30000 });
    cp.execSync(`git commit -m "${message}"`, { cwd, timeout: 30000 });
  } catch {
    // Git commit failed silently
  }
}

function scheduleDebouncedSync(): void {
  if (debounceTimer) {
    clearTimeout(debounceTimer);
  }
  debounceTimer = setTimeout(async () => {
    const config = vscode.workspace.getConfiguration("schroedingerSync");
    if (config.get<boolean>("autoSync", true)) {
      await runSync();
    }
  }, 5000);
}

export function activate(context: vscode.ExtensionContext): void {
  statusBarItem = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 100);
  statusBarItem.command = "schroedingerSync.syncNow";
  statusBarItem.text = "$(sync) Schroedinger";
  statusBarItem.show();
  context.subscriptions.push(statusBarItem);

  const syncCommand = vscode.commands.registerCommand("schroedingerSync.syncNow", async () => {
    statusBarItem.text = "$(sync~spin) Syncing...";
    await runSync();
  });
  context.subscriptions.push(syncCommand);

  const sessionsDir = getSessionsDir();
  if (fs.existsSync(sessionsDir)) {
    const pattern = new vscode.RelativePattern(sessionsDir, "**/*.jsonl");
    fileWatcher = vscode.workspace.createFileSystemWatcher(pattern);
    fileWatcher.onDidChange(() => scheduleDebouncedSync());
    fileWatcher.onDidCreate(() => scheduleDebouncedSync());
    context.subscriptions.push(fileWatcher);
  }

  countNewSessions().then((count) => updateStatusBar(count));
}

export function deactivate(): void {
  if (debounceTimer) {
    clearTimeout(debounceTimer);
  }
}
