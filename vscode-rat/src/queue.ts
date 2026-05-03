/**
 * queue.ts — Execution controller with per-runtime scheduling.
 *
 * Work is serialized per rat runtime so a shared kernel namespace remains
 * consistent, but independent runtimes can execute concurrently. While code is
 * running the controller streams rat/output notifications and polls ctl(status)
 * only to detect stdin prompts.
 *
 * Supports three targets:
 *   - "notebook" — fenced code cells in markdown; output injected as ```output blocks
 *   - "source"   — logical blocks in source files; output shown as inline decorations
 *   - "webview"  — Rat Markdown custom editor runs; output posted to the webview
 */

import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";

import { refindCell, type CodeCell } from "./cells";
import { upsertOutput } from "./output";
import { getClient, ratCancel, toolResultDisplayText, type McpClient } from "./rat";
import {
  showRunning,
  clearRunning,
  showResult,
  appendToOutputChannel,
} from "./inlineOutput";
import type { SourceBlock } from "./blocks";

// ── Types ──────────────────────────────────────────────────

export interface NotebookItem {
  kind: "notebook";
  cell: CodeCell;
  editor: vscode.TextEditor;
  runtimeName: string;
  cwd: string;
  lang: string;
}

export interface SourceItem {
  kind: "source";
  block: SourceBlock;
  code: string;
  editor: vscode.TextEditor;
  runtimeName: string;
  cwd: string;
  lang: string;
}

export interface WebviewItem {
  kind: "webview";
  id: number;
  code: string;
  document: vscode.TextDocument;
  webviewPanel: vscode.WebviewPanel;
  runtimeName: string;
  cwd: string;
  lang: string;
  onDidFinish?: () => void;
}

export type QueueItem = NotebookItem | SourceItem | WebviewItem;

export type QueueState = "idle" | "running" | "paused";
export type QueueItemState = "running" | "cancelling" | "stale" | "queued";

export interface QueueSnapshotItem {
  id: number;
  state: QueueItemState;
  /** Index in the global queued list. Undefined for running items. */
  queuedIndex?: number;
  runtimeName: string;
  cwd: string;
  lang: string;
  kind: QueueItem["kind"];
  title: string;
  detail: string;
  codePreview: string;
  documentUri: string;
  /** Zero-based source/document line when known. */
  line?: number;
}

export type StateCallback = (state: QueueState, pending: number) => void;

class RunCancellation {
  cancelled = false;
  locallyFinalized = false;
  private cancelImpl: (() => void) | null = null;
  private cleanupFns: Array<() => void> = [];
  private cleanupRan = false;

  setCancel(fn: () => void): void {
    this.cancelImpl = fn;
    if (this.cancelled) this.safeCall(fn);
  }

  onCancel(fn: () => void): void {
    this.cleanupFns.push(fn);
    if (this.cancelled) this.safeCall(fn);
  }

  cancel(): void {
    const first = !this.cancelled;
    this.cancelled = true;
    this.cancelImpl && this.safeCall(this.cancelImpl);
    if (first) this.runCleanup();
  }

  markLocallyFinalized(): void {
    this.locallyFinalized = true;
    this.cancel();
  }

  private runCleanup(): void {
    if (this.cleanupRan) return;
    this.cleanupRan = true;
    for (const fn of this.cleanupFns) this.safeCall(fn);
  }

  private safeCall(fn: () => void): void {
    try {
      fn();
    } catch {
      // Cancellation should be best-effort and never keep VS Code stuck.
    }
  }
}

interface RunningExecution {
  item: QueueItem;
  cancellation: RunCancellation;
  state: Exclude<QueueItemState, "queued">;
  cancelRequestedAt?: number;
  forceTimer?: ReturnType<typeof setTimeout>;
}

const DEFAULT_CANCEL_FORCE_MS = 3_000;
const STOP_CANCEL_FORCE_MS = 1_000;

// ── Controller ─────────────────────────────────────────────

export class ExecutionController {
  private items: QueueItem[] = [];
  private running = new Map<string, RunningExecution>();
  private itemIds = new WeakMap<QueueItem, number>();
  private nextItemId = 1;
  private paused = false;
  private blockedRuntimes = new Set<string>();
  private onChange: StateCallback;

  constructor(onChange: StateCallback) {
    this.onChange = onChange;
  }

  get state(): QueueState {
    // A pause only gates future items; already-running items remain visible as
    // running so cancel keybindings/status indicators stay correct.
    if (this.running.size > 0) return "running";
    return this.paused ? "paused" : "idle";
  }

  get pending(): number {
    return this.items.length;
  }

  snapshot(): QueueSnapshotItem[] {
    const running = Array.from(this.running.values()).map((run) =>
      this.snapshotItem(run.item, run.state),
    );
    const queued = this.items.map((item, index) =>
      this.snapshotItem(item, "queued", index),
    );
    return [...running, ...queued];
  }

  enqueue(item: QueueItem): void {
    this.itemId(item);
    this.items.push(item);
    this.pump();
  }

  isRuntimeBlocked(runtimeName: string): boolean {
    return this.blockedRuntimes.has(runtimeName);
  }

  resumeRuntime(runtimeName: string): boolean {
    const changed = this.blockedRuntimes.delete(runtimeName);
    if (changed) this.pump();
    return changed;
  }

  cancelCurrent(forceAfterMs = DEFAULT_CANCEL_FORCE_MS): void {
    let changed = false;
    for (const [key, run] of this.running) {
      this.requestCancel(key, run, forceAfterMs);
      changed = true;
    }
    if (changed) this.emit();
  }

  cancelRuntime(runtimeName: string, forceAfterMs = DEFAULT_CANCEL_FORCE_MS): boolean {
    const run = this.running.get(runtimeName);
    if (!run) return false;
    this.requestCancel(runtimeName, run, forceAfterMs);
    this.emit();
    return true;
  }

  clearRuntime(runtimeName: string): boolean {
    let changed = false;
    const run = this.running.get(runtimeName);
    if (run) {
      this.requestCancel(runtimeName, run, STOP_CANCEL_FORCE_MS);
      changed = true;
    }

    changed = this.blockedRuntimes.delete(runtimeName) || changed;
    changed = this.clearQueuedRuntime(runtimeName, false) || changed;
    if (changed) this.emit();
    return changed;
  }

  clearQueuedRuntime(runtimeName: string, emit = true): boolean {
    let changed = this.blockedRuntimes.delete(runtimeName);
    const kept: QueueItem[] = [];
    for (const item of this.items) {
      if (this.runtimeKey(item) === runtimeName) {
        if (item.kind === "webview") {
          void this.postWebviewDone(item, false, "", "Cancelled", { message: "Cancelled" });
        }
        changed = true;
      } else {
        kept.push(item);
      }
    }
    this.items = kept;
    if (changed && emit) this.emit();
    return changed;
  }

  cancelRunning(id: number, forceAfterMs = DEFAULT_CANCEL_FORCE_MS): boolean {
    for (const [key, run] of this.running) {
      if (this.itemId(run.item) !== id) continue;
      this.requestCancel(key, run, forceAfterMs);
      this.emit();
      return true;
    }
    return false;
  }

  forceClearRunning(id: number): boolean {
    for (const [key, run] of this.running) {
      if (this.itemId(run.item) !== id) continue;
      this.requestCancel(key, run, 0, false);
      this.forceFinishRun(key, run, "Cancelled", false);
      return true;
    }
    return false;
  }

  forceClearRuntime(runtimeName: string): boolean {
    const run = this.running.get(runtimeName);
    if (!run) return false;
    this.requestCancel(runtimeName, run, 0, false);
    this.forceFinishRun(runtimeName, run, "Cancelled", false);
    return true;
  }

  cancelAll(): void {
    for (const [key, run] of this.running) {
      this.requestCancel(key, run, STOP_CANCEL_FORCE_MS);
    }
    this.cancelQueuedWebviews();
    this.items = [];
    this.blockedRuntimes.clear();
    this.emit();
  }

  clear(): void {
    this.cancelQueuedWebviews();
    this.items = [];
    this.blockedRuntimes.clear();
    this.emit();
  }

  togglePause(): void {
    this.paused = !this.paused;
    if (!this.paused) this.pump();
    else this.emit();
  }

  remove(index: number): void {
    if (!this.removeQueuedAt(index)) return;
    this.emit();
  }

  removeQueued(id: number): boolean {
    const index = this.items.findIndex((item) => this.itemId(item) === id);
    if (!this.removeQueuedAt(index)) return false;
    this.emit();
    return true;
  }

  cancelWebviewRun(webviewPanel: vscode.WebviewPanel, id: number): void {
    const queuedIdx = this.items.findIndex(
      (item) => item.kind === "webview" && item.webviewPanel === webviewPanel && item.id === id,
    );
    if (queuedIdx >= 0) {
      this.removeQueuedAt(queuedIdx);
      this.emit();
      return;
    }

    for (const [key, run] of this.running) {
      if (
        run.item.kind === "webview" &&
        run.item.webviewPanel === webviewPanel &&
        run.item.id === id
      ) {
        this.requestCancel(key, run, DEFAULT_CANCEL_FORCE_MS);
        this.emit();
        return;
      }
    }
  }

  // ── internal scheduling ──────────────────────────────────

  private emit(): void {
    this.onChange(this.state, this.items.length);
  }

  private runtimeKey(item: QueueItem): string {
    return item.runtimeName;
  }

  private itemId(item: QueueItem): number {
    let id = this.itemIds.get(item);
    if (id === undefined) {
      id = this.nextItemId++;
      this.itemIds.set(item, id);
    }
    return id;
  }

  private removeQueuedAt(index: number): QueueItem | null {
    if (index < 0 || index >= this.items.length) return null;
    const [item] = this.items.splice(index, 1);
    if (item.kind === "webview") {
      void this.postWebviewDone(item, false, "", "Cancelled", { message: "Cancelled" });
    }
    return item;
  }

  private requestCancel(
    key: string,
    run: RunningExecution,
    forceAfterMs: number,
    notifyOnForce = true,
  ): void {
    run.state = "cancelling";
    run.cancelRequestedAt ??= Date.now();
    run.cancellation.cancel();

    this.clearForceTimer(run);
    if (forceAfterMs <= 0) return;

    run.forceTimer = setTimeout(() => {
      const current = this.running.get(key);
      if (current !== run) return;
      this.forceFinishRun(key, run, "Cancelled", notifyOnForce);
    }, forceAfterMs);
  }

  private forceFinishRun(
    key: string,
    run: RunningExecution,
    reason: string,
    notify: boolean,
  ): void {
    const current = this.running.get(key);
    if (current !== run) return;

    this.clearForceTimer(run);
    run.state = "stale";
    run.cancellation.markLocallyFinalized();
    this.finishLocalUi(run.item, reason);
    this.running.delete(key);

    if (notify) {
      this.blockedRuntimes.add(key);
      vscode.window.showWarningMessage(
        `Rat: ${run.item.runtimeName} did not finish after interrupt; VS Code execution state was reset. Queued work for this runtime is paused until resumed, cleared, or restarted.`,
      );
    }

    this.emit();
    if (!notify) this.pump();
  }

  private finishLocalUi(item: QueueItem, reason: string): void {
    if (item.kind === "source") {
      clearRunning(item.editor);
    } else if (item.kind === "webview") {
      void this.postWebviewDone(item, false, "", reason, { message: reason });
      item.onDidFinish?.();
    }
  }

  private clearForceTimer(run: RunningExecution): void {
    if (!run.forceTimer) return;
    clearTimeout(run.forceTimer);
    run.forceTimer = undefined;
  }

  private snapshotItem(
    item: QueueItem,
    state: QueueItemState,
    queuedIndex?: number,
  ): QueueSnapshotItem {
    const code = queueItemCode(item);
    const location = queueItemLocation(item);
    const codePreview = previewCode(code);
    return {
      id: this.itemId(item),
      state,
      queuedIndex,
      runtimeName: item.runtimeName,
      cwd: item.cwd,
      lang: item.lang,
      kind: item.kind,
      title: queueItemTitle(item, location),
      detail: codePreview,
      codePreview,
      documentUri: location.document.uri.toString(),
      line: location.line,
    };
  }

  private pump(): void {
    if (this.paused) {
      this.emit();
      return;
    }

    let started = false;
    for (let i = 0; i < this.items.length;) {
      const item = this.items[i];
      const key = this.runtimeKey(item);
      if (this.running.has(key) || this.blockedRuntimes.has(key)) {
        i++;
        continue;
      }

      this.items.splice(i, 1);
      this.startItem(item, key);
      started = true;
    }

    if (!started) this.emit();
  }

  private startItem(item: QueueItem, key: string): void {
    const cancellation = new RunCancellation();
    this.running.set(key, { item, cancellation, state: "running" });
    this.emit();

    void this.runItem(item, cancellation)
      .catch((err: unknown) => {
        if (cancellation.cancelled) return;
        const msg = err instanceof Error ? err.message : String(err);
        vscode.window.showErrorMessage(`Rat: ${msg}`);
      })
      .finally(() => {
        const current = this.running.get(key);
        if (current?.item === item) {
          this.clearForceTimer(current);
          this.running.delete(key);
        }
        this.emit();
        this.pump();
      });
  }

  private async runItem(
    item: QueueItem,
    cancellation: RunCancellation,
  ): Promise<void> {
    if (item.kind === "notebook") {
      await this.runNotebook(item, cancellation);
    } else if (item.kind === "source") {
      await this.runSource(item, cancellation);
    } else {
      await this.runWebview(item, cancellation);
    }
  }

  private cancelQueuedWebviews(): void {
    for (const item of this.items) {
      if (item.kind === "webview") {
        void this.postWebviewDone(item, false, "", "Cancelled", { message: "Cancelled" });
      }
    }
  }

  // ── Notebook mode (markdown/quarto) ──────────────────────

  private async runNotebook(
    item: NotebookItem,
    cancellation: RunCancellation,
  ): Promise<void> {
    const { cell, editor, runtimeName, cwd } = item;
    if (!cell.executable || cancellation.cancelled) return;

    const freshCell = (): { openLine: number; closeLine: number } | null => {
      const found = refindCell(
        editor.document,
        cell.code,
        cell.ratLang,
        cell.openLine,
      );
      return found ? { openLine: found.openLine, closeLine: found.closeLine } : null;
    };

    const fc = freshCell();
    if (fc === null) return;

    const client = await getClient(runtimeName, cwd, item.lang);
    cancellation.setCancel(() => {
      client.abortCurrentRequest();
      void client.cancel();
      void ratCancel(runtimeName).catch(() => {});
    });
    if (cancellation.cancelled) return;

    const maxLines: number = vscode.workspace
      .getConfiguration("rat")
      .get("maxOutputLines", 100);

    let streamedOutput = "";
    let waitingForInput = false;
    let promptActive = false;
    let partialTimer: ReturnType<typeof setTimeout> | null = null;
    let partialRenderPromise: Promise<void> | null = null;

    const schedulePartialRender = () => {
      if (partialTimer || cancellation.cancelled) return;
      partialTimer = setTimeout(() => {
        partialTimer = null;
        if (cancellation.cancelled || !streamedOutput.trim()) return;
        const suffix = waitingForInput
          ? "\n\n✋ waiting for input…"
          : "\n\n⏳ running…";
        const fc2 = freshCell();
        if (fc2 !== null) {
          partialRenderPromise = upsertOutput(
            editor,
            fc2.openLine,
            fc2.closeLine,
            cleanOutput(streamedOutput) + suffix,
            [],
            maxLines,
          ).finally(() => {
            partialRenderPromise = null;
          });
        }
      }, 200);
    };

    let poll: ReturnType<typeof setInterval> | null = setInterval(() => {
      if (cancellation.cancelled || promptActive) return;
      promptActive = true;
      void this.pollInput(client, () => {
        waitingForInput = true;
        schedulePartialRender();
      }).finally(() => {
        waitingForInput = false;
        promptActive = false;
      });
    }, 300);
    cancellation.onCancel(() => {
      if (partialTimer) {
        clearTimeout(partialTimer);
        partialTimer = null;
      }
      if (poll) {
        clearInterval(poll);
        poll = null;
      }
    });

    try {
      const result = await client.run(cell.code, (chunk) => {
        streamedOutput += chunk;
        schedulePartialRender();
      });
      if (cancellation.cancelled) return;

      const finalFc = freshCell();
      if (finalFc === null) return;

      if (partialTimer) {
        clearTimeout(partialTimer);
        partialTimer = null;
      }
      if (partialRenderPromise) await partialRenderPromise;

      const { text, images } = extractPlots(
        cleanOutput(result.text),
        editor,
        cwd,
      );
      await upsertOutput(
        editor,
        finalFc.openLine,
        finalFc.closeLine,
        text,
        images,
        maxLines,
        result.status,
      );
    } finally {
      if (partialTimer) clearTimeout(partialTimer);
      if (poll) clearInterval(poll);
    }
  }

  // ── Source mode (.py, .r, .jl, etc.) ─────────────────────

  private async runSource(
    item: SourceItem,
    cancellation: RunCancellation,
  ): Promise<void> {
    const { block, code, editor, runtimeName, cwd } = item;
    const lastLine = block.range.end.line;

    showRunning(editor, lastLine);
    cancellation.onCancel(() => clearRunning(editor));

    let client: McpClient;
    try {
      client = await getClient(runtimeName, cwd, item.lang);
    } catch (err) {
      clearRunning(editor);
      throw err;
    }

    cancellation.setCancel(() => {
      client.abortCurrentRequest();
      void client.cancel();
      void ratCancel(runtimeName).catch(() => {});
    });
    if (cancellation.cancelled) {
      clearRunning(editor);
      return;
    }

    // Stream stdout live from run SSE notifications and poll only for
    // stdin prompts. The old ctl(output) polling path is still available for
    // older kernels, but current rat servers push rat/output notifications.
    let streamedOutput = "";
    let promptActive = false;
    let poll: ReturnType<typeof setInterval> | null = setInterval(() => {
      if (cancellation.cancelled || promptActive) return;
      promptActive = true;
      void this.pollInput(client, () => {
        showRunning(editor, lastLine, undefined, true);
      }).finally(() => {
        promptActive = false;
      });
    }, 300);
    cancellation.onCancel(() => {
      if (poll) {
        clearInterval(poll);
        poll = null;
      }
      clearRunning(editor);
    });

    try {
      const result = await client.run(code, (chunk) => {
        streamedOutput += chunk;
        showRunning(editor, lastLine, cleanOutput(streamedOutput));
      });
      clearRunning(editor);
      if (cancellation.cancelled) return;

      const cleaned = cleanOutput(result.text);
      const isError = result.isError;

      // Always log to output channel
      const label = `${item.lang} [line ${block.range.start.line + 1}]`;
      if (cleaned.trim()) {
        appendToOutputChannel(label, cleaned);
      }

      // Inline decoration
      if (cleaned.trim()) {
        showResult(editor, lastLine, cleaned, isError);
      }
    } finally {
      if (poll) clearInterval(poll);
      clearRunning(editor);
    }
  }

  // ── Webview mode (Rat Markdown custom editor) ────────────

  private async runWebview(
    item: WebviewItem,
    cancellation: RunCancellation,
  ): Promise<void> {
    let client: McpClient;
    try {
      client = await getClient(item.runtimeName, item.cwd, item.lang);
    } catch (err: unknown) {
      if (cancellation.locallyFinalized) return;
      const msg = err instanceof Error ? err.message : String(err);
      await this.postWebviewDone(item, false, "", msg, { message: msg });
      item.onDidFinish?.();
      return;
    }

    cancellation.setCancel(() => {
      client.abortCurrentRequest();
      void client.cancel();
      void ratCancel(item.runtimeName).catch(() => {});
    });
    if (cancellation.cancelled) {
      if (!cancellation.locallyFinalized) {
        await this.postWebviewDone(item, false, "", "Cancelled", { message: "Cancelled" });
      }
      item.onDidFinish?.();
      return;
    }

    let promptActive = false;
    let poll: ReturnType<typeof setInterval> | null = setInterval(() => {
      if (cancellation.cancelled || promptActive) return;
      promptActive = true;
      void this.pollInput(client).finally(() => {
        promptActive = false;
      });
    }, 300);
    cancellation.onCancel(() => {
      if (poll) {
        clearInterval(poll);
        poll = null;
      }
    });

    try {
      const result = await client.run(item.code, (chunk) => {
        void item.webviewPanel.webview.postMessage({
          type: "ratOutput",
          id: item.id,
          chunk,
        });
      });
      if (cancellation.cancelled) {
        if (!cancellation.locallyFinalized) {
          await this.postWebviewDone(item, false, "", "Cancelled", { message: "Cancelled" });
        }
        return;
      }

      await this.postWebviewDone(item, !result.isError, toolResultDisplayText(result), "");
    } catch (err: unknown) {
      if (!cancellation.locallyFinalized) {
        const msg = cancellation.cancelled ? "Cancelled" : err instanceof Error ? err.message : String(err);
        await this.postWebviewDone(item, false, "", msg, { message: msg });
      }
    } finally {
      if (poll) clearInterval(poll);
      item.onDidFinish?.();
    }
  }

  // ── Shared helpers ───────────────────────────────────────

  private async postWebviewDone(
    item: WebviewItem,
    success: boolean,
    stdout: string,
    stderr: string,
    error?: { message: string },
  ): Promise<void> {
    await item.webviewPanel.webview.postMessage({
      type: "ratDone",
      id: item.id,
      success,
      stdout,
      stderr,
      ...(error ? { error } : {}),
    });
  }

  private async pollInput(
    client: McpClient,
    onWaiting?: () => void,
  ): Promise<boolean> {
    try {
      const st = await client.status();
      // Status is multi-line ("waiting_for_input\nruntime_version:
      // ...\nmemory_mb: ..."). The state is the first line.
      const state = st.split("\n", 1)[0].trim();
      if (state !== "waiting_for_input") return false;

      onWaiting?.();
      const input = await vscode.window.showInputBox({
        prompt: "Program is waiting for input",
        placeHolder: "Type here and press Enter…",
      });

      if (input !== undefined) {
        await client.sendInput(input);
      } else {
        await client.cancel();
      }
      return true;
    } catch {
      return false;
    }
  }
}

interface QueueItemLocation {
  document: vscode.TextDocument;
  line?: number;
}

function queueItemCode(item: QueueItem): string {
  switch (item.kind) {
    case "notebook":
      return item.cell.code;
    case "source":
      return item.code;
    case "webview":
      return item.code;
  }
}

function queueItemLocation(item: QueueItem): QueueItemLocation {
  switch (item.kind) {
    case "notebook":
      return { document: item.editor.document, line: item.cell.openLine };
    case "source":
      return { document: item.editor.document, line: item.block.range.start.line };
    case "webview":
      return { document: item.document };
  }
}

function queueItemTitle(item: QueueItem, location: QueueItemLocation): string {
  const lang = displayLang(item);
  const doc = documentLabel(location.document);
  const where = location.line !== undefined ? `${doc}:${location.line + 1}` : doc;

  switch (item.kind) {
    case "notebook":
      return `${lang} cell · ${where}`;
    case "source":
      return `${lang} block · ${where}`;
    case "webview":
      return `${lang} cell · ${where}`;
  }
}

function displayLang(item: QueueItem): string {
  return item.kind === "notebook" ? item.cell.lang : item.lang;
}

function documentLabel(document: vscode.TextDocument): string {
  const fileName = document.uri.fsPath || document.fileName || document.uri.toString();
  return path.basename(fileName) || document.uri.toString();
}

function previewCode(code: string): string {
  const first = code
    .split(/\r?\n/)
    .map((line) => line.trim())
    .find((line) => line.length > 0);
  if (!first) return "(empty)";
  return first.length > 90 ? `${first.slice(0, 87)}…` : first;
}

export { ExecutionController as ExecutionQueue };

// ── Plot extraction ────────────────────────────────────────

const PLOT_RE = /^__RAT_PLOT__:(.+)$/;

function extractPlots(
  output: string,
  editor: vscode.TextEditor,
  cwd: string,
): { text: string; images: string[] } {
  const assetsRel: string = vscode.workspace
    .getConfiguration("rat")
    .get("assetsDir", "_assets");

  const assetsAbs = path.join(cwd, assetsRel);
  const fileDir = path.dirname(editor.document.uri.fsPath);

  const textLines: string[] = [];
  const images: string[] = [];

  for (const line of output.split("\n")) {
    const m = line.match(PLOT_RE);
    if (m) {
      const src = m[1];
      try {
        fs.mkdirSync(assetsAbs, { recursive: true });
        const fname = path.basename(src);
        const dest = path.join(assetsAbs, fname);
        fs.copyFileSync(src, dest);
        const rel = path.relative(fileDir, dest);
        images.push(`![plot](${rel})`);
      } catch (e: unknown) {
        const msg = e instanceof Error ? e.message : String(e);
        textLines.push(`[plot error: ${msg}]`);
      }
    } else {
      textLines.push(line);
    }
  }

  return { text: textLines.join("\n"), images };
}

// ── Terminal clean-up ──────────────────────────────────────

// eslint-disable-next-line no-control-regex
const ANSI_RE =
  /\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[()][A-Z0-9]|[0-9A-Za-z=<>])/g;

function cleanOutput(text: string): string {
  let s = text.replace(ANSI_RE, "");
  if (!s.includes("\r")) return s;
  return s
    .split("\n")
    .map((line) => {
      if (!line.includes("\r")) return line;
      const parts = line.split("\r");
      for (let i = parts.length - 1; i >= 0; i--) {
        if (parts[i]) return parts[i];
      }
      return "";
    })
    .join("\n");
}
