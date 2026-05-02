/**
 * runtimeView.ts — TreeView sidebar showing running kernels + saved runtimes.
 */

import * as path from "path";
import * as vscode from "vscode";
import {
  log,
  ratRemove,
  ratRestart,
  ratStart,
  ratStop,
  readState,
} from "./rat";
import { projectRuntimeName } from "./resolve";

export class RuntimeTreeProvider
  implements vscode.TreeDataProvider<RuntimeNode>
{
  private _onChange = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this._onChange.event;
  private refreshTimer: ReturnType<typeof setInterval> | null = null;

  refresh(): void {
    this._onChange.fire();
  }

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
    const workspaceDirs = (vscode.workspace.workspaceFolders ?? []).map(
      (f) => path.resolve(f.uri.fsPath),
    );

    log(`tree: workspaceDirs=${JSON.stringify(workspaceDirs)}`);
    log(`tree: kernels=${JSON.stringify(state.kernels.map(k => ({ name: k.name, cwd: k.cwd, port: k.port, status: k.status })))}`);
    log(`tree: runtimes=${JSON.stringify(state.runtimes.map(r => ({ name: r.name, cwd: r.cwd })))}`);

    const nodes: RuntimeNode[] = [];

    for (const k of state.kernels) {
      if (!isVisibleRuntime(k.name, k.cwd, workspaceDirs)) continue;
      nodes.push(new RuntimeNode(k.name, k.lang, k.cwd, k.venv, true, k.port));
    }

    for (const r of state.runtimes) {
      if (runningNames.has(r.name)) continue;
      if (!isVisibleRuntime(r.name, r.cwd, workspaceDirs)) continue;
      nodes.push(new RuntimeNode(r.name, r.lang, r.cwd, r.venv, false, 0));
    }

    return dedupeRuntimeNodes(nodes).sort((a, b) => {
      if (a.running !== b.running) return a.running ? -1 : 1;
      return a.runtimeName.localeCompare(b.runtimeName);
    });
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
    if (runtimeName.endsWith("-global")) parts.push("global");
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
    await ratRemove(name);
    vscode.window.showInformationMessage(`Rat: ${name} removed`);
  } catch (err: unknown) {
    vscode.window.showErrorMessage(`Rat: ${msg(err)}`);
  }
}

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

function dedupeRuntimeNodes(nodes: RuntimeNode[]): RuntimeNode[] {
  const grouped = new Map<string, RuntimeNode[]>();

  for (const node of nodes) {
    const key = `${node.lang}::${path.resolve(node.cwd)}::${aliasKey(node.runtimeName)}`;
    const group = grouped.get(key);
    if (group) group.push(node);
    else grouped.set(key, [node]);
  }

  const deduped: RuntimeNode[] = [];
  for (const group of grouped.values()) {
    if (group.length === 1) {
      deduped.push(group[0]);
      continue;
    }

    const preferredName = projectRuntimeName(group[0].lang, group[0].cwd);
    const preferred = group.find((node) => node.runtimeName === preferredName)
      ?? group.find((node) => node.running)
      ?? group[0];
    deduped.push(preferred);
  }

  return deduped;
}

function aliasKey(name: string): string {
  return name.toLowerCase().replace(/[-_]+/g, "_");
}

function isVisibleRuntime(name: string, cwd: string, workspaceDirs: string[]): boolean {
  if (name.endsWith("-global")) return true;
  if (workspaceDirs.length === 0) return true;
  return workspaceDirs.some((d) => path.resolve(cwd) === d);
}

function shortPath(p: string): string {
  const home = process.env.HOME || process.env.USERPROFILE || "";
  if (home && p.startsWith(home)) return "~" + p.slice(home.length);
  return p;
}

function msg(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
