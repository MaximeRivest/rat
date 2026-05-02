/**
 * queue.ts — Execution queue with pause / cancel / remove.
 *
 * Cells are enqueued and processed one-at-a-time so the shared
 * kernel namespace stays consistent.  While a cell is running the
 * queue polls `ctl(status)` and pops an input box when the kernel
 * reports "waiting_for_input".
 *
 * Supports two modes:
 *   - "notebook" — fenced code cells in markdown; output injected as ```output blocks
 *   - "source"   — logical blocks in .py/.r/.jl files; output shown as inline decorations
 */

import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";

import { refindCell, type CodeCell } from "./cells";
import { upsertOutput } from "./output";
import { getClient, type McpClient } from "./rat";
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

export type QueueItem = NotebookItem | SourceItem;

export type QueueState = "idle" | "running" | "paused";

export type StateCallback = (state: QueueState, pending: number) => void;

// ── Queue ──────────────────────────────────────────────────

export class ExecutionQueue {
  private items: QueueItem[] = [];
  private busy = false;
  private paused = false;
  private cancelFn: (() => void) | null = null;
  private onChange: StateCallback;

  constructor(onChange: StateCallback) {
    this.onChange = onChange;
  }

  get state(): QueueState {
    // A pause only gates future items; an already-running item is still
    // executing and must remain cancellable / visible as running.
    if (this.busy) return "running";
    return this.paused ? "paused" : "idle";
  }
  get pending(): number {
    return this.items.length;
  }

  enqueue(item: QueueItem): void {
    this.items.push(item);
    this.emit();
    if (!this.busy && !this.paused) this.next();
  }

  cancelCurrent(): void {
    this.cancelFn?.();
    this.cancelFn = null;
  }

  cancelAll(): void {
    this.cancelCurrent();
    this.items = [];
    this.emit();
  }

  clear(): void {
    this.items = [];
    this.emit();
  }

  togglePause(): void {
    this.paused = !this.paused;
    this.emit();
    if (!this.paused && !this.busy && this.items.length) this.next();
  }

  remove(index: number): void {
    if (index >= 0 && index < this.items.length) {
      this.items.splice(index, 1);
      this.emit();
    }
  }

  // ── internal ─────────────────────────────────────────────

  private emit(): void {
    this.onChange(this.state, this.items.length);
  }

  private async next(): Promise<void> {
    if (this.paused || !this.items.length) {
      this.busy = false;
      this.emit();
      return;
    }

    this.busy = true;
    this.emit();

    const item = this.items.shift()!;

    try {
      if (item.kind === "notebook") {
        await this.runNotebook(item);
      } else {
        await this.runSource(item);
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      vscode.window.showErrorMessage(`Rat: ${msg}`);
    }

    this.busy = false;
    if (this.items.length && !this.paused) {
      setTimeout(() => this.next(), 0);
    } else {
      this.emit();
    }
  }

  // ── Notebook mode (markdown/quarto) ──────────────────────

  private async runNotebook(item: NotebookItem): Promise<void> {
    const { cell, editor, runtimeName, cwd } = item;
    if (!cell.executable) return;

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

    let cancelled = false;
    this.cancelFn = () => {
      cancelled = true;
      client.abortCurrentRequest();
      client.cancel();
    };

    const maxLines: number = vscode.workspace
      .getConfiguration("rat")
      .get("maxOutputLines", 100);

    let lastPartialLen = 0;
    let inputPromptOpen = false;
    let tickRunning = false;
    const poll = setInterval(async () => {
      if (cancelled || tickRunning) return;
      tickRunning = true;
      try {
        try {
          const partial = await client.partialOutput();
          if (partial && partial.length > lastPartialLen) {
            lastPartialLen = partial.length;
            const suffix = inputPromptOpen
              ? "\n\n✋ waiting for input…"
              : "\n\n⏳ running…";
            const cleaned = cleanOutput(partial) + suffix;
            const fc2 = freshCell();
            if (fc2 !== null) {
              await upsertOutput(editor, fc2.openLine, fc2.closeLine, cleaned, [], maxLines);
            }
          }
        } catch { /* best-effort */ }
        if (!inputPromptOpen && !cancelled) {
          inputPromptOpen = await this.pollInput(client);
        }
      } finally {
        tickRunning = false;
      }
    }, 300);

    try {
      const result = await client.run(cell.code);
      if (cancelled) return;

      const finalFc = freshCell();
      if (finalFc === null) return;

      const { text, images } = extractPlots(
        cleanOutput(result.text),
        editor,
        cwd,
      );
      await upsertOutput(editor, finalFc.openLine, finalFc.closeLine, text, images, maxLines);
    } finally {
      clearInterval(poll);
      this.cancelFn = null;
    }
  }

  // ── Source mode (.py, .r, .jl, etc.) ─────────────────────

  private async runSource(item: SourceItem): Promise<void> {
    const { block, code, editor, runtimeName, cwd } = item;
    const lastLine = block.range.end.line;

    showRunning(editor, lastLine);

    const client = await getClient(runtimeName, cwd, item.lang);

    let cancelled = false;
    this.cancelFn = () => {
      cancelled = true;
      client.abortCurrentRequest();
      client.cancel();
    };

    // Poll for input prompts AND stream partial stdout live so the
    // gutter indicator shows progress (e.g. tqdm, for-loops with
    // flush=True) instead of just a static ⏳ until completion.
    let inputPromptOpen = false;
    let tickRunning = false;
    let lastPartialLen = 0;
    const poll = setInterval(async () => {
      if (cancelled || tickRunning) return;
      tickRunning = true;
      try {
        try {
          const partial = await client.partialOutput();
          if (partial && partial.length > lastPartialLen) {
            lastPartialLen = partial.length;
            showRunning(editor, lastLine, cleanOutput(partial), inputPromptOpen);
          } else if (inputPromptOpen) {
            showRunning(editor, lastLine, undefined, true);
          }
        } catch {
          /* best-effort */
        }
        if (!inputPromptOpen && !cancelled) {
          inputPromptOpen = await this.pollInput(client);
        }
      } finally {
        tickRunning = false;
      }
    }, 300);

    try {
      const result = await client.run(code);
      clearRunning(editor);
      if (cancelled) return;

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
      clearInterval(poll);
      clearRunning(editor);
      this.cancelFn = null;
    }
  }

  // ── Shared helpers ───────────────────────────────────────

  private async pollInput(client: McpClient): Promise<boolean> {
    try {
      const st = await client.status();
      // Status is multi-line ("waiting_for_input\nruntime_version:
      // ...\nmemory_mb: ..."). The state is the first line.
      const state = st.split("\n", 1)[0].trim();
      if (state !== "waiting_for_input") return false;

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
