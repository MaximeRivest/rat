/**
 * queue.ts — Execution queue with pause / cancel / remove.
 *
 * Cells are enqueued and processed one-at-a-time so the shared
 * kernel namespace stays consistent.  While a cell is running the
 * queue polls `ctl(status)` and pops an input box when the kernel
 * reports "waiting_for_input".
 */

import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";

import { refindCell, type CodeCell } from "./cells";
import { upsertOutput } from "./output";
import { getClient, type McpClient } from "./rat";

// ── Types ──────────────────────────────────────────────────

export interface QueueItem {
  cell: CodeCell;
  editor: vscode.TextEditor;
  runtimeName: string;
  cwd: string;
  lang: string;
}

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
    if (this.paused) return "paused";
    return this.busy ? "running" : "idle";
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
      await this.run(item);
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

  private async run(item: QueueItem): Promise<void> {
    const { cell, editor, runtimeName, cwd, lang } = item;
    if (!cell.executable) return;

    // ── Re-find the cell in the live document ─────────────────
    // Line numbers go stale when earlier outputs are inserted /
    // resized.  We match by (code + language + proximity) and
    // use the FRESH lines for every output write.
    const freshCell = (): { openLine: number; closeLine: number } | null => {
      const found = refindCell(
        editor.document,
        cell.code,
        cell.ratLang,
        cell.openLine,     // hint — where it was when queued
      );
      return found ? { openLine: found.openLine, closeLine: found.closeLine } : null;
    };

    const fc = freshCell();
    if (fc === null) return;   // cell was deleted or edited away

    const client = await getClient(runtimeName, cwd, lang);

    // cancellation flag
    let cancelled = false;
    this.cancelFn = () => {
      cancelled = true;
      client.abortCurrentRequest();
      client.cancel();
    };

    const maxLines: number = vscode.workspace
      .getConfiguration("rat")
      .get("maxOutputLines", 100);

    // Poll for partial output + input prompts while the cell executes.
    // During streaming the code cell doesn't move (we only modify the
    // output block below it), so one freshCloseLine() at the start is
    // enough — we re-derive only for the final write.
    let lastPartialLen = 0;
    let inputPromptOpen = false;
    let tickRunning = false;            // reentrance guard
    const poll = setInterval(async () => {
      // setInterval + async: ticks don't wait for each other.
      // Without this guard, tick N+1 fires while tick N is still
      // awaiting showInputBox → second input box dismisses the
      // first with undefined → triggers cancel → KeyboardInterrupt.
      if (cancelled || tickRunning) return;
      tickRunning = true;
      try {
        // ── stream partial output into the document ──
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
        // ── check for input() prompts ──
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

      // Final write — re-find in case streaming shifted things.
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

  /**
   * Check if the kernel is waiting for input().  If so, show a VS Code
   * input box, send the reply, and return `true`.  Returns `false` if
   * the kernel was not waiting.
   */
  private async pollInput(client: McpClient): Promise<boolean> {
    try {
      const st = await client.status();
      if (st !== "waiting_for_input") return false;

      const input = await vscode.window.showInputBox({
        prompt: "Program is waiting for input",
        placeHolder: "Type here and press Enter…",
      });

      if (input !== undefined) {
        await client.sendInput(input);
      } else {
        // user pressed Escape → cancel execution
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

/**
 * Strip ANSI escape codes and process `\r` (carriage return) so that
 * tqdm / progress bars show only the final frame and terminal colours
 * don’t leak into markdown output blocks.
 */
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
