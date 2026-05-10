import * as cp from "child_process";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import * as vscode from "vscode";

import { ensureRatInstalled, ratTerminalCommand, userPathRatPath } from "./installRat";
import { PiSessionMrmdEditor } from "./piSessionEditor";

const PI_PACKAGE = "@earendil-works/pi-coding-agent";

export function registerPiratUriHandler(
  ctx: vscode.ExtensionContext,
  piSessions: PiSessionMrmdEditor,
): vscode.Disposable {
  return vscode.window.registerUriHandler({
    async handleUri(uri: vscode.Uri): Promise<void> {
      if (uri.path !== "/pirat" && uri.path !== "pirat") return;
      const params = new URLSearchParams(uri.query);
      const cwd = params.get("cwd") || undefined;
      const session = params.get("session") || undefined;
      const fullscreen = params.get("fullscreen") !== "0";

      if (cwd && !workspaceContains(cwd)) {
        vscode.window.showInformationMessage(
          `PiRAT requested ${cwd}. If this is the wrong VS Code window, run: code ${shellQuote(cwd)} --open-url ${shellQuote(uri.toString())}`,
        );
      }

      if (session) {
        await piSessions.show(vscode.Uri.file(session));
      } else {
        await piSessions.show();
      }
      if (fullscreen) await focusPiratFullscreen();
    },
  });
}

export async function openPiratFullscreen(piSessions: PiSessionMrmdEditor, uri?: vscode.Uri): Promise<void> {
  await piSessions.show(uri);
  await focusPiratFullscreen();
}

export async function installPiratToolsCommand(ctx: vscode.ExtensionContext): Promise<void> {
  const picked = await vscode.window.showInformationMessage(
    "Install/update PiRAT tools? This installs rat/pi support, the pirat terminal command, and Pi helper extensions.",
    { modal: true },
    "Install PiRAT Tools",
  );
  if (picked !== "Install PiRAT Tools") return;

  await vscode.window.withProgress(
    { location: vscode.ProgressLocation.Notification, title: "PiRAT: installing tools…", cancellable: false },
    async (progress) => {
      progress.report({ message: "Installing rat CLI" });
      await ensureRatInstalled({ manual: true });

      progress.report({ message: "Installing pi runtime" });
      await runShell(ratTerminalCommand(["install", "pi"]));

      progress.report({ message: "Installing pirat command" });
      const pirat = await installPiratShim(ctx);

      progress.report({ message: "Installing Pi helper extensions" });
      await installPiHelperExtensions();

      vscode.window.showInformationMessage(`PiRAT tools installed. Command: ${pirat}`);
    },
  );
}

export async function promptInstallPiratToolsIfNeeded(ctx: vscode.ExtensionContext): Promise<void> {
  if (ctx.globalState.get<boolean>("piratToolsPrompted")) return;
  const missing = await missingPiratTools();
  if (missing.length === 0) return;
  await ctx.globalState.update("piratToolsPrompted", true);
  const picked = await vscode.window.showInformationMessage(
    `PiRAT helper tools are missing: ${missing.join(", ")}. Install them now?`,
    "Install",
    "Later",
  );
  if (picked === "Install") await installPiratToolsCommand(ctx);
}

async function missingPiratTools(): Promise<string[]> {
  const missing: string[] = [];
  if (!(await commandOk("rat", ["--version"]))) missing.push("rat");
  if (!(await commandOk("pi", ["--version"]))) missing.push("pi");
  if (!(await commandOk("pirat", ["--help"]))) missing.push("pirat");
  const extDir = path.join(agentDir(), "extensions");
  if (!fs.existsSync(path.join(extDir, "modes.ts"))) missing.push("modes.ts");
  if (!fs.existsSync(path.join(extDir, "pirat.ts"))) missing.push("pirat.ts");
  return missing;
}

async function installPiratShim(ctx: vscode.ExtensionContext): Promise<string> {
  const ratPath = userPathRatPath();
  if (!ratPath) throw new Error("Could not choose a user PATH install location.");
  const dir = path.dirname(ratPath);
  await fs.promises.mkdir(dir, { recursive: true });
  const extensionId = ctx.extension.id;

  if (process.platform === "win32") {
    const file = path.join(dir, "pirat.cmd");
    const content = `@echo off\r\nnode "${path.join(ctx.globalStorageUri.fsPath, "bin", "pirat.js")}" %*\r\n`;
    await fs.promises.mkdir(path.dirname(path.join(ctx.globalStorageUri.fsPath, "bin", "pirat.js")), { recursive: true });
    await fs.promises.writeFile(path.join(ctx.globalStorageUri.fsPath, "bin", "pirat.js"), piratNodeShim(extensionId), "utf8");
    await fs.promises.writeFile(file, content, "utf8");
    return file;
  }

  const file = path.join(dir, "pirat");
  await fs.promises.writeFile(file, piratShellShim(extensionId), "utf8");
  await fs.promises.chmod(file, 0o755);
  return file;
}

function piratShellShim(extensionId: string): string {
  return `#!/usr/bin/env bash
set -euo pipefail
cwd=""
session=""
fullscreen="1"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --cwd) cwd="\${2:-}"; shift 2 ;;
    --session) session="\${2:-}"; shift 2 ;;
    --no-fullscreen) fullscreen="0"; shift ;;
    -h|--help) echo "Usage: pirat [folder] [--cwd DIR] [--session FILE] [--no-fullscreen]"; exit 0 ;;
    *) if [[ -z "$cwd" ]]; then cwd="$1"; else echo "pirat: unexpected argument: $1" >&2; exit 2; fi; shift ;;
  esac
done
if [[ -z "$cwd" ]]; then cwd="$PWD"; fi
if command -v python3 >/dev/null 2>&1; then
  url=$(python3 - "$cwd" "$session" "$fullscreen" <<'PY'
import sys, urllib.parse
cwd, session, fullscreen = sys.argv[1:4]
q = {'cwd': cwd, 'fullscreen': fullscreen}
if session: q['session'] = session
print('vscode://${extensionId}/pirat?' + urllib.parse.urlencode(q))
PY
)
else
  url="vscode://${extensionId}/pirat?cwd=$cwd&session=$session&fullscreen=$fullscreen"
fi
if command -v code >/dev/null 2>&1; then
  code "$cwd" --open-url "$url"
elif [[ "$(uname -s)" == "Darwin" ]]; then
  open -a "Visual Studio Code" "$cwd"
  open "$url"
else
  echo "pirat: VS Code 'code' command not found" >&2
  exit 1
fi
`;
}

function piratNodeShim(extensionId: string): string {
  return `const cp = require('child_process');
const path = require('path');
let cwd = '', session = '', fullscreen = '1';
const args = process.argv.slice(2);
for (let i = 0; i < args.length; i++) {
  const a = args[i];
  if (a === '--cwd') cwd = args[++i] || '';
  else if (a === '--session') session = args[++i] || '';
  else if (a === '--no-fullscreen') fullscreen = '0';
  else if (a === '-h' || a === '--help') { console.log('Usage: pirat [folder] [--cwd DIR] [--session FILE] [--no-fullscreen]'); process.exit(0); }
  else if (!cwd) cwd = a;
  else { console.error('pirat: unexpected argument: ' + a); process.exit(2); }
}
if (!cwd) cwd = process.cwd();
const params = new URLSearchParams({ cwd, fullscreen });
if (session) params.set('session', session);
const url = 'vscode://${extensionId}/pirat?' + params.toString();
cp.spawn('code', [cwd, '--open-url', url], { detached: true, stdio: 'ignore' }).unref();
`;
}

async function installPiHelperExtensions(): Promise<void> {
  const extDir = path.join(agentDir(), "extensions");
  await fs.promises.mkdir(extDir, { recursive: true });
  await fs.promises.writeFile(path.join(extDir, "modes.ts"), modesExtensionSource(), "utf8");
  await fs.promises.writeFile(path.join(extDir, "pirat.ts"), piratExtensionSource(), "utf8");
}

function modesExtensionSource(): string {
  return `import type { ExtensionAPI } from "${PI_PACKAGE}";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { homedir } from "node:os";

const CUSTOM_TYPE = "mode-switch";
const SYSTEM_PROMPT_SECTIONS = new Set(["available_tools", "custom_tools_note", "guidelines", "pi_docs", "append_prompt", "project_context", "skills", "date", "cwd"]);

type ModeDef = { key: string; label: string; opener: string; appendix?: string; systemPrompt?: string; removeSections?: string[] };

const BUILTIN: ModeDef[] = [
  { key: "coding", label: "Coding", opener: "", appendix: "Focus on concise, practical coding help." },
  { key: "plan", label: "Plan", opener: "Make a concise implementation plan before changing files.", appendix: "Do not edit files unless the user asks you to proceed." },
  { key: "review", label: "Review", opener: "Review the current work for correctness, risks, and missing tests." },
  { key: "explain", label: "Explain", opener: "Explain the relevant code and decisions clearly before proposing changes." },
];

function agentDir() { return process.env.PI_AGENT_DIR || join(homedir(), ".pi", "agent"); }
function modeDir() { return join(agentDir(), "modes"); }
function valid(raw: any): ModeDef | null {
  const key = String(raw?.key ?? "").trim().toLowerCase();
  const label = String(raw?.label ?? "").trim();
  const opener = String(raw?.opener ?? "").trim();
  const appendix = typeof raw?.appendix === "string" ? raw.appendix : "";
  const systemPrompt = typeof raw?.systemPrompt === "string" ? raw.systemPrompt : "";
  const removeSections = Array.isArray(raw?.removeSections) ? raw.removeSections.filter((s: any) => typeof s === "string" && SYSTEM_PROMPT_SECTIONS.has(s)) : [];
  if (!/^[a-z][a-z0-9_-]*$/.test(key) || !label || (!opener && !systemPrompt)) return null;
  return { key, label, opener, ...(appendix.trim() ? { appendix } : {}), ...(systemPrompt.trim() ? { systemPrompt } : {}), ...(removeSections.length ? { removeSections } : {}) };
}
function loadModes(): ModeDef[] {
  const byKey = new Map(BUILTIN.map((m) => [m.key, m]));
  try {
    if (existsSync(modeDir())) for (const file of readdirSync(modeDir())) {
      if (!file.endsWith(".json")) continue;
      const mode = valid(JSON.parse(readFileSync(join(modeDir(), file), "utf8")));
      if (mode) byKey.set(mode.key, mode);
    }
  } catch {}
  return [...byKey.values()];
}
function modeFromEntries(entries: readonly any[]): string {
  let mode = "coding";
  for (const entry of entries) {
    if (entry?.type === "custom" && entry.customType === CUSTOM_TYPE && typeof entry.data?.mode === "string") mode = entry.data.mode;
  }
  return mode;
}

export default function(pi: ExtensionAPI) {
  let activeMode = "coding";
  pi.registerCommand("mode", {
    description: "Switch prompt mode",
    getArgumentCompletions: (prefix: string) => loadModes().filter((m) => m.key.startsWith(prefix.trim().toLowerCase())).map((m) => ({ value: m.key, label: m.label, opener: m.opener, appendix: m.appendix, systemPrompt: m.systemPrompt, removeSections: m.removeSections })),
    handler: async (args, ctx) => {
      const key = args.trim().toLowerCase();
      const mode = loadModes().find((m) => m.key === key);
      if (!mode) { ctx.ui.notify("Unknown mode: " + (key || "(empty)"), "error"); return; }
      activeMode = mode.key;
      pi.appendEntry(CUSTOM_TYPE, { mode: mode.key });
      ctx.ui.notify("Mode: " + mode.label, "info");
    },
  });
  pi.on("session_start", async (_event, ctx) => {
    activeMode = modeFromEntries(ctx.sessionManager.getEntries());
  });
  pi.on("before_agent_start", async (event, ctx) => {
    activeMode = modeFromEntries(ctx.sessionManager.getEntries());
    const mode = loadModes().find((m) => m.key === activeMode);
    if (!mode) return;
    let systemPrompt = mode.systemPrompt?.trim() ? mode.systemPrompt : event.systemPrompt;
    const extra = [mode.opener, mode.appendix].filter(Boolean).join("\n\n").trim();
    if (extra) systemPrompt += "\n\n# Current prompt mode: " + mode.label + "\n" + extra;
    return { systemPrompt };
  });
}
`;
}

function piratExtensionSource(): string {
  return `import type { ExtensionAPI } from "${PI_PACKAGE}";
import { spawn } from "node:child_process";

export default function(pi: ExtensionAPI) {
  pi.registerCommand("pirat", {
    description: "Open this session in VS Code PiRAT",
    handler: async (args, ctx) => {
      const session = ctx.sessionManager.getSessionFile();
      const command = process.platform === "win32" ? "pirat.cmd" : "pirat";
      const argv = ["--cwd", ctx.cwd, "--session", session];
      if (args.trim().includes("--no-fullscreen")) argv.push("--no-fullscreen");
      try {
        const child = spawn(command, argv, { detached: true, stdio: "ignore" });
        child.unref();
        ctx.ui.notify("Opened in PiRAT. Avoid sending more prompts in this terminal session to prevent concurrent edits.", "info");
      } catch (err) {
        ctx.ui.notify("Could not launch pirat: " + (err instanceof Error ? err.message : String(err)), "error");
      }
    },
  });
}
`;
}

async function focusPiratFullscreen(): Promise<void> {
  await vscode.commands.executeCommand("workbench.view.extension.ratPiSessionsContainer").then(undefined, () => undefined);
  await vscode.commands.executeCommand("workbench.action.closeSidebar").then(undefined, () => undefined);
  await vscode.commands.executeCommand("workbench.action.closePanel").then(undefined, () => undefined);
  await vscode.commands.executeCommand("workbench.action.maximizeEditor").then(undefined, () => undefined);
}

function workspaceContains(cwd: string): boolean {
  const resolved = path.resolve(cwd);
  return (vscode.workspace.workspaceFolders ?? []).some((folder) => {
    const rel = path.relative(folder.uri.fsPath, resolved);
    return rel === "" || (!rel.startsWith("..") && !path.isAbsolute(rel));
  });
}

function agentDir(): string {
  return process.env.PI_AGENT_DIR || path.join(os.homedir(), ".pi", "agent");
}

function runShell(command: string): Promise<void> {
  return new Promise((resolve, reject) => {
    cp.exec(command, { timeout: 180_000 }, (err, stdout, stderr) => {
      if (err) reject(new Error(`${command}\n${stderr || stdout || err.message}`));
      else resolve();
    });
  });
}

function commandOk(command: string, args: string[]): Promise<boolean> {
  return new Promise((resolve) => {
    const child = cp.spawn(command, args, { stdio: "ignore" });
    child.on("error", () => resolve(false));
    child.on("exit", (code) => resolve(code === 0));
  });
}

function shellQuote(value: string): string {
  if (process.platform === "win32") return `"${value.replace(/"/g, '\\"')}"`;
  return `'${value.replace(/'/g, `'\\''`)}'`;
}
