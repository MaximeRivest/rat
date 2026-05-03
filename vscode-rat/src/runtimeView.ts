/**
 * runtimeView.ts — TreeView sidebar showing running kernels + saved runtimes.
 */

import * as path from "path";
import * as vscode from "vscode";
import type { ExecutionController, QueueSnapshotItem } from "./queue";
import {
  ratCancel,
  ratRemove,
  ratRestart,
  ratStart,
  ratStop,
  readState,
} from "./rat";
import { projectRuntimeName } from "./resolve";

type RuntimeTreeNode = RuntimeNode | RuntimeQueueNode;

export class RuntimeTreeProvider
  implements vscode.TreeDataProvider<RuntimeTreeNode>
{
  private _onChange = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this._onChange.event;
  private refreshTimer: ReturnType<typeof setInterval> | null = null;

  constructor(private readonly queue?: ExecutionController) {}

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

  getTreeItem(el: RuntimeTreeNode): vscode.TreeItem {
    return el;
  }

  getChildren(el?: RuntimeTreeNode): RuntimeTreeNode[] {
    if (el instanceof RuntimeQueueNode) return [];
    if (el instanceof RuntimeNode) {
      let queuedOrdinal = 0;
      return el.queueItems.map((entry) => {
        const ordinal = entry.state === "queued" ? ++queuedOrdinal : 0;
        return new RuntimeQueueNode(entry, ordinal);
      });
    }

    const state = readState();
    const queueItems = this.queue?.snapshot() ?? [];
    const queueByRuntime = groupQueueItems(queueItems);
    const runningNames = new Set(state.kernels.map((k) => k.name));
    const runtimeNodeNames = new Set<string>();
    const workspaceDirs = (vscode.workspace.workspaceFolders ?? []).map(
      (f) => path.resolve(f.uri.fsPath),
    );

    const nodes: RuntimeNode[] = [];

    for (const k of state.kernels) {
      const entries = queueByRuntime.get(k.name) ?? [];
      if (!isVisibleRuntime(k.name, k.cwd, workspaceDirs) && entries.length === 0) continue;
      runtimeNodeNames.add(k.name);
      nodes.push(new RuntimeNode(
        k.name,
        k.lang,
        k.cwd,
        k.venv,
        true,
        k.port,
        entries,
        true,
        this.queue?.isRuntimeBlocked(k.name) ?? false,
      ));
    }

    for (const r of state.runtimes) {
      if (runningNames.has(r.name)) continue;
      const entries = queueByRuntime.get(r.name) ?? [];
      if (!isVisibleRuntime(r.name, r.cwd, workspaceDirs) && entries.length === 0) continue;
      runtimeNodeNames.add(r.name);
      nodes.push(new RuntimeNode(
        r.name,
        r.lang,
        r.cwd,
        r.venv,
        false,
        0,
        entries,
        true,
        this.queue?.isRuntimeBlocked(r.name) ?? false,
      ));
    }

    // Show queued/executing work even before rat has written a runtime/kernel to
    // state.yaml. This makes newly submitted cells visible immediately.
    for (const [runtimeName, entries] of queueByRuntime) {
      if (runtimeNodeNames.has(runtimeName)) continue;
      const first = entries[0];
      const executing = entries.some((entry) => entry.state !== "queued");
      nodes.push(new RuntimeNode(
        runtimeName,
        first.lang,
        first.cwd,
        "",
        executing,
        0,
        entries,
        false,
        this.queue?.isRuntimeBlocked(runtimeName) ?? false,
      ));
    }

    return dedupeRuntimeNodes(nodes).sort((a, b) => {
      if (a.running !== b.running) return a.running ? -1 : 1;
      if (a.queueItems.length !== b.queueItems.length) {
        return b.queueItems.length - a.queueItems.length;
      }
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
    public readonly queueItems: QueueSnapshotItem[] = [],
    public readonly saved: boolean = !running,
    public readonly blocked: boolean = false,
  ) {
    super(
      runtimeName,
      queueItems.length > 0
        ? vscode.TreeItemCollapsibleState.Expanded
        : vscode.TreeItemCollapsibleState.None,
    );

    const icon = running ? "circle-filled" : "circle-outline";
    const color = running
      ? new vscode.ThemeColor("testing.iconPassed")
      : undefined;
    this.iconPath = new vscode.ThemeIcon(icon, color);

    const parts = [lang];
    if (cwd) parts.push(shortPath(cwd));
    if (venv) parts.push(".venv");
    if (runtimeName.endsWith("-global")) parts.push("global");
    const queueDescription = describeQueue(queueItems);
    if (blocked) parts.push("queue paused");
    if (queueDescription) parts.push(queueDescription);
    if (!running && saved) parts.push("saved");
    else if (!running && !saved && queueItems.length > 0) parts.push("pending");
    this.description = parts.join(" · ");

    this.tooltip = [
      `Name: ${runtimeName}`,
      `Language: ${lang}`,
      `CWD: ${cwd}`,
      venv ? `Venv: ${venv}` : null,
      running ? (port ? `Port: ${port}` : "Status: executing") : "Status: stopped",
      blocked ? "Queue: paused after a stale interrupt. Resume, clear, or restart this runtime." : null,
      queueItems.length > 0 ? `Queue: ${describeQueue(queueItems)}` : null,
    ]
      .filter(Boolean)
      .join("\n");

    this.contextValue = blocked ? "blockedRuntime" : running ? "runningKernel" : saved ? "savedRuntime" : "queuedRuntime";
  }

  withQueueItems(queueItems: QueueSnapshotItem[]): RuntimeNode {
    return new RuntimeNode(
      this.runtimeName,
      this.lang,
      this.cwd,
      this.venv,
      this.running,
      this.port,
      queueItems,
      this.saved,
      this.blocked,
    );
  }
}

export class RuntimeQueueNode extends vscode.TreeItem {
  public readonly queueId: number;
  public readonly runtimeName: string;

  constructor(
    public readonly entry: QueueSnapshotItem,
    queuedOrdinal: number,
  ) {
    super(entry.title, vscode.TreeItemCollapsibleState.None);

    this.queueId = entry.id;
    this.runtimeName = entry.runtimeName;

    const active = entry.state !== "queued";
    const cancelling = entry.state === "cancelling";
    const stale = entry.state === "stale";
    this.iconPath = new vscode.ThemeIcon(
      active ? (cancelling ? "debug-pause" : stale ? "warning" : "loading") : "clock",
      active ? new vscode.ThemeColor(stale ? "problemsWarningIcon.foreground" : "progressBar.background") : undefined,
    );

    this.description = queueNodeDescription(entry, queuedOrdinal);
    this.tooltip = [
      queueStateLabel(entry.state),
      `Runtime: ${entry.runtimeName}`,
      `Language: ${entry.lang}`,
      entry.line !== undefined ? `Line: ${entry.line + 1}` : null,
      `Document: ${entry.documentUri}`,
      "",
      entry.codePreview,
    ]
      .filter((line) => line !== null)
      .join("\n");
    this.contextValue = entry.state === "queued"
      ? "queueQueued"
      : cancelling || stale
        ? "queueCancelling"
        : "queueRunning";
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

export function interruptRuntimeCmd(
  queue: ExecutionController,
  node?: RuntimeNode,
): void {
  if (!node) return;
  if (!queue.cancelRuntime(node.runtimeName)) {
    void ratCancel(node.runtimeName).catch((err: unknown) => {
      vscode.window.showErrorMessage(`Rat: ${msg(err)}`);
    });
  }
}

export function clearRuntimeQueueCmd(
  queue: ExecutionController,
  node?: RuntimeNode,
): void {
  if (!node) return;
  queue.clearQueuedRuntime(node.runtimeName);
}

export function resumeRuntimeQueueCmd(
  queue: ExecutionController,
  node?: RuntimeNode,
): void {
  if (!node) return;
  queue.resumeRuntime(node.runtimeName);
}

export function interruptQueueItemCmd(
  queue: ExecutionController,
  node?: RuntimeQueueNode,
): void {
  if (!node) return;
  if (node.entry.state === "cancelling" || node.entry.state === "stale") {
    queue.forceClearRunning(node.queueId) || queue.forceClearRuntime(node.runtimeName);
    return;
  }
  queue.cancelRunning(node.queueId) || queue.cancelRuntime(node.runtimeName);
}

export function forceClearQueueItemCmd(
  queue: ExecutionController,
  node?: RuntimeQueueNode,
): void {
  if (!node) return;
  queue.forceClearRunning(node.queueId) || queue.forceClearRuntime(node.runtimeName);
}

export function removeQueuedItemCmd(
  queue: ExecutionController,
  node?: RuntimeQueueNode,
): void {
  if (!node) return;
  if (node.entry.state !== "queued") {
    queue.cancelRunning(node.queueId) || queue.cancelRuntime(node.runtimeName);
    return;
  }
  queue.removeQueued(node.queueId);
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
    const mergedQueue = uniqueQueueItems(group.flatMap((node) => node.queueItems));
    if (group.length === 1) {
      deduped.push(group[0].withQueueItems(mergedQueue));
      continue;
    }

    const preferredName = projectRuntimeName(group[0].lang, group[0].cwd);
    const preferred = group.find((node) => node.runtimeName === preferredName)
      ?? group.find((node) => node.running)
      ?? group[0];
    deduped.push(preferred.withQueueItems(mergedQueue));
  }

  return deduped;
}

function aliasKey(name: string): string {
  return name.toLowerCase().replace(/[-_]+/g, "_");
}

function stateRank(state: QueueSnapshotItem["state"]): number {
  switch (state) {
    case "running":
      return 0;
    case "cancelling":
      return 1;
    case "stale":
      return 2;
    case "queued":
    default:
      return 3;
  }
}

function groupQueueItems(items: QueueSnapshotItem[]): Map<string, QueueSnapshotItem[]> {
  const grouped = new Map<string, QueueSnapshotItem[]>();
  for (const item of items) {
    const group = grouped.get(item.runtimeName);
    if (group) group.push(item);
    else grouped.set(item.runtimeName, [item]);
  }
  for (const group of grouped.values()) {
    group.sort((a, b) => {
      if (a.state !== b.state) return stateRank(a.state) - stateRank(b.state);
      return (a.queuedIndex ?? 0) - (b.queuedIndex ?? 0);
    });
  }
  return grouped;
}

function uniqueQueueItems(items: QueueSnapshotItem[]): QueueSnapshotItem[] {
  const byId = new Map<number, QueueSnapshotItem>();
  for (const item of items) byId.set(item.id, item);
  return [...byId.values()].sort((a, b) => {
    if (a.state !== b.state) return stateRank(a.state) - stateRank(b.state);
    return (a.queuedIndex ?? 0) - (b.queuedIndex ?? 0);
  });
}

function queueNodeDescription(entry: QueueSnapshotItem, queuedOrdinal: number): string {
  switch (entry.state) {
    case "running":
      return `executing · ${entry.detail}`;
    case "cancelling":
      return `cancelling… · ${entry.detail}`;
    case "stale":
      return `stale · ${entry.detail}`;
    case "queued":
    default:
      return `queued #${queuedOrdinal} · ${entry.detail}`;
  }
}

function queueStateLabel(state: QueueSnapshotItem["state"]): string {
  switch (state) {
    case "running":
      return "Executing";
    case "cancelling":
      return "Cancelling";
    case "stale":
      return "Stale local execution state";
    case "queued":
    default:
      return "Queued";
  }
}

function describeQueue(items: QueueSnapshotItem[]): string {
  const running = items.filter((item) => item.state === "running").length;
  const cancelling = items.filter((item) => item.state === "cancelling").length;
  const stale = items.filter((item) => item.state === "stale").length;
  const queued = items.filter((item) => item.state === "queued").length;
  const parts: string[] = [];
  if (running) parts.push(`${running} executing`);
  if (cancelling) parts.push(`${cancelling} cancelling`);
  if (stale) parts.push(`${stale} stale`);
  if (queued) parts.push(`${queued} queued`);
  return parts.join(" · ");
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
