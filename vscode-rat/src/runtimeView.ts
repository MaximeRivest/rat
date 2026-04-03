/**
 * runtimeView.ts — TreeView sidebar showing running kernels + saved runtimes.
 *
 *   RAT RUNTIMES
 *   ├── 🟢 py@autoprogramming  (py · ~/Projects/auto · .venv)
 *   ├── 🟢 sh                   (sh · ~/Projects/scratch)
 *   ├── ⚪ py-ml                 (py · ~/ml · .venv)  [saved]
 */

import * as vscode from "vscode";
import * as path from "path";
import {
  readState,
  ratStart,
  ratStop,
  ratRestart,
  ratRm,
  type KernelInfo,
  type SavedRuntime,
} from "./rat";

// ── TreeView provider ──────────────────────────────────────

export class RuntimeTreeProvider
  implements vscode.TreeDataProvider<RuntimeNode>
{
  private _onChange = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this._onChange.event;
  private refreshTimer: ReturnType<typeof setInterval> | null = null;

  refresh(): void {
    this._onChange.fire();
  }

  /** Auto-refresh every N seconds while the view is visible. */
  startAutoRefresh(intervalMs = 3000): void {
    this.stopAutoRefresh();
    this.refreshTimer = setInterval(() => this.refresh(), intervalMs);
  }

  stopAutoRefresh(): void {
    if (this.refreshTimer) {
      clearInterval(this.refreshTimer);
      this.refreshTimer = null;
    }
  }

  getTreeItem(el: RuntimeNode): vscode.TreeItem {
    return el;
  }

  getChildren(): RuntimeNode[] {
    const state = readState();
    const runningNames = new Set(state.kernels.map((k) => k.name));
    const nodes: RuntimeNode[] = [];

    // Running kernels
    for (const k of state.kernels) {
      nodes.push(
        new RuntimeNode(k.name, k.lang, k.cwd, k.venv, true, k.port),
      );
    }

    // Saved runtimes that aren't currently running
    for (const r of state.runtimes) {
      if (!runningNames.has(r.name)) {
        nodes.push(
          new RuntimeNode(r.name, r.lang, r.cwd, r.venv, false, 0),
        );
      }
    }

    return nodes;
  }
}

export class RuntimeNode extends vscode.TreeItem {
  constructor(
    public readonly runtimeName: string,
    public readonly lang: string,
    public readonly cwd: string,
    public readonly venv: string,
    public readonly running: boolean,
    public readonly port: number,
  ) {
    super(runtimeName, vscode.TreeItemCollapsibleState.None);

    const icon = running ? "circle-filled" : "circle-outline";
    const color = running
      ? new vscode.ThemeColor("testing.iconPassed")
      : undefined;
    this.iconPath = new vscode.ThemeIcon(icon, color);

    const parts = [lang];
    if (cwd) parts.push(shortPath(cwd));
    if (venv) parts.push(".venv");
    if (!running) parts.push("saved");
    this.description = parts.join(" · ");

    this.tooltip = [
      `Name: ${runtimeName}`,
      `Language: ${lang}`,
      `CWD: ${cwd}`,
      venv ? `Venv: ${venv}` : null,
      running ? `Port: ${port}` : "Status: stopped",
    ]
      .filter(Boolean)
      .join("\n");

    this.contextValue = running ? "runningKernel" : "savedRuntime";
  }
}

// ── Commands wired to tree items ───────────────────────────

export async function startRuntimeCmd(node?: RuntimeNode): Promise<void> {
  const name = node?.runtimeName ?? (await pickName("Start which runtime?"));
  if (!name) return;
  try {
    const cwd = node?.cwd || process.cwd();
    await ratStart(name, cwd);
    vscode.window.showInformationMessage(`Rat: ${name} started`);
  } catch (err: unknown) {
    vscode.window.showErrorMessage(`Rat: ${msg(err)}`);
  }
}

export async function stopRuntimeCmd(node?: RuntimeNode): Promise<void> {
  const name = node?.runtimeName ?? (await pickName("Stop which runtime?"));
  if (!name) return;
  try {
    await ratStop(name);
    vscode.window.showInformationMessage(`Rat: ${name} stopped`);
  } catch (err: unknown) {
    vscode.window.showErrorMessage(`Rat: ${msg(err)}`);
  }
}

export async function restartRuntimeCmd(node?: RuntimeNode): Promise<void> {
  const name = node?.runtimeName ?? (await pickName("Restart which runtime?"));
  if (!name) return;
  try {
    const cwd = node?.cwd || process.cwd();
    await ratRestart(name, cwd);
    vscode.window.showInformationMessage(`Rat: ${name} restarted`);
  } catch (err: unknown) {
    vscode.window.showErrorMessage(`Rat: ${msg(err)}`);
  }
}

export async function removeRuntimeCmd(node?: RuntimeNode): Promise<void> {
  const name = node?.runtimeName ?? (await pickName("Remove which runtime?"));
  if (!name) return;
  const confirm = await vscode.window.showWarningMessage(
    `Remove runtime "${name}"? This stops the kernel and deletes the saved config.`,
    { modal: true },
    "Remove",
  );
  if (confirm !== "Remove") return;
  try {
    await ratRm(name);
    vscode.window.showInformationMessage(`Rat: ${name} removed`);
  } catch (err: unknown) {
    vscode.window.showErrorMessage(`Rat: ${msg(err)}`);
  }
}

// ── helpers ────────────────────────────────────────────────

async function pickName(prompt: string): Promise<string | undefined> {
  const state = readState();
  const names = [
    ...state.kernels.map((k) => k.name),
    ...state.runtimes.map((r) => r.name),
  ];
  const unique = [...new Set(names)];
  if (unique.length === 0) {
    vscode.window.showInformationMessage("No runtimes found");
    return undefined;
  }
  return vscode.window.showQuickPick(unique, { placeHolder: prompt });
}

function shortPath(p: string): string {
  const home = process.env.HOME || process.env.USERPROFILE || "";
  if (home && p.startsWith(home)) return "~" + p.slice(home.length);
  return p;
}

function msg(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
