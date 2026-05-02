/**
 * inspectorView.ts — Rat sidebar inspector for the symbol under the cursor.
 *
 * Shows the full `look(at=...)` output in the Rat activity bar, which is much
 * roomier than a hover tooltip and keeps docstrings visible while editing.
 */

import * as vscode from "vscode";
import { cellAtLine, parseCells, ratLangForFence } from "./cells";
import { detectFileLang, isRatFile } from "./langDetect";
import { existingOrRunningClient } from "./rat";
import { resolveRuntime } from "./resolve";

interface InspectTarget {
  expr: string;
  ratLang: string;
  document: vscode.TextDocument;
}

interface SourceLocation {
  path: string;
  line: number;
}

export class RatInspectorViewProvider implements vscode.WebviewViewProvider, vscode.Disposable {
  private view?: vscode.WebviewView;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private seq = 0;
  private readonly disposables: vscode.Disposable[] = [];

  resolveWebviewView(view: vscode.WebviewView): void {
    this.view = view;
    view.webview.options = { enableScripts: true };
    this.disposables.push(
      view.webview.onDidReceiveMessage((message) => {
        if (message?.command === "open" && typeof message.path === "string") {
          const line = typeof message.line === "number" ? message.line : 0;
          const pos = new vscode.Position(Math.max(0, line), 0);
          vscode.window.showTextDocument(vscode.Uri.file(message.path), {
            preview: false,
            selection: new vscode.Range(pos, pos),
          });
        }
      }),
    );
    this.renderEmpty("Place the cursor on a symbol in a rat file.");
    this.update(vscode.window.activeTextEditor);
  }

  update(editor: vscode.TextEditor | undefined): void {
    this.updateTarget(editor ? inspectTarget(editor) : null);
  }

  updateRatMarkdownSelection(
    document: vscode.TextDocument,
    language: string | null,
    expression: string | null,
  ): void {
    const ratLang = language ? ratLangForFence(language) : null;
    this.updateTarget(ratLang && expression ? { expr: expression, ratLang, document } : null);
  }

  updateTarget(target: InspectTarget | null): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => {
      void this.updateNow(target);
    }, 150);
  }

  dispose(): void {
    if (this.timer) clearTimeout(this.timer);
    for (const disposable of this.disposables) disposable.dispose();
  }

  private async updateNow(target: InspectTarget | null): Promise<void> {
    if (!this.view) return;
    const seq = ++this.seq;

    if (!target) {
      this.renderEmpty("Place the cursor on a symbol in a rat file.");
      return;
    }

    this.renderLoading(target.expr);

    const { name } = resolveRuntime(target.ratLang, target.document);
    const client = await existingOrRunningClient(name);
    if (seq !== this.seq) return;

    if (!client) {
      this.renderEmpty(
        `No running runtime for ${escapeHtml(target.expr)}. Run a cell first to inspect live objects.`,
      );
      return;
    }

    try {
      const info = await client.lookFull(target.expr);
      if (seq !== this.seq) return;
      if (!info || info.endsWith(": not found")) {
        this.renderEmpty(`${escapeHtml(target.expr)} was not found in the running runtime.`);
        return;
      }
      this.renderInfo(target.expr, info);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      this.renderEmpty(`Inspection failed: ${escapeHtml(msg)}`);
    }
  }

  private renderLoading(expr: string): void {
    this.setHtml(`
      ${style()}
      <main>
        <h2>${escapeHtml(expr)}</h2>
        <p class="muted">Inspecting…</p>
      </main>
    `);
  }

  private renderInfo(expr: string, info: string): void {
    const source = sourceLocationFromInspection(info);
    const sourceHtml = source
      ? `<button id="open-def">Open definition</button><p class="source">${escapeHtml(source.path)}:${source.line + 1}</p>`
      : "";
    const script = source ? `
      <script>
        const vscode = acquireVsCodeApi();
        document.getElementById('open-def')?.addEventListener('click', () => {
          vscode.postMessage({ command: 'open', path: ${JSON.stringify(source.path)}, line: ${source.line} });
        });
      </script>
    ` : "";

    this.setHtml(`
      ${style()}
      <main>
        <h2>${escapeHtml(expr)}</h2>
        ${sourceHtml}
        <pre>${escapeHtml(info)}</pre>
      </main>
      ${script}
    `);
  }

  private renderEmpty(message: string): void {
    this.setHtml(`
      ${style()}
      <main>
        <h2>Inspector</h2>
        <p class="muted">${message}</p>
      </main>
    `);
  }

  private setHtml(body: string): void {
    if (!this.view) return;
    this.view.webview.html = `<!doctype html><html><head><meta charset="UTF-8"></head><body>${body}</body></html>`;
  }
}

function inspectTarget(editor: vscode.TextEditor): InspectTarget | null {
  const document = editor.document;
  if (!isRatFile(document)) return null;

  const fl = detectFileLang(document);
  const position = editor.selection.active;
  let ratLang: string | null = null;

  if (fl.mode === "source") {
    ratLang = fl.ratLang;
  } else {
    const cell = cellAtLine(parseCells(document), position.line);
    if (!cell?.executable) return null;
    if (position.line <= cell.openLine || position.line >= cell.closeLine) return null;
    ratLang = cell.ratLang;
  }
  if (!ratLang) return null;

  const selected = selectedExpression(editor, ratLang);
  const expr = selected ?? expressionAtPosition(document.lineAt(position.line).text, position.character, ratLang);
  if (!expr) return null;

  return { expr, ratLang, document };
}

function selectedExpression(editor: vscode.TextEditor, ratLang: string): string | null {
  const selection = editor.selection;
  if (selection.isEmpty || !selection.isSingleLine) return null;
  const text = editor.document.getText(selection).trim();
  if (!text || text.length > 200) return null;
  return isExpression(text, ratLang) ? text : null;
}

function expressionAtPosition(line: string, character: number, ratLang: string): string | null {
  const col = Math.min(character, line.length);
  let start = col;
  if (start > 0 && !isExprChar(line[start], ratLang) && isExprChar(line[start - 1], ratLang)) start--;
  while (start > 0 && isExprChar(line[start - 1], ratLang)) start--;

  let end = Math.max(col, start);
  while (end < line.length && isExprChar(line[end], ratLang)) end++;

  const expr = line.slice(start, end).replace(/^\.+|\.+$/g, "");
  if (!isExpression(expr, ratLang)) return null;
  return expr;
}

function isExprChar(ch: string | undefined, ratLang: string): boolean {
  if (!ch) return false;
  if (ratLang === "js") return /[A-Za-z0-9_.$]/.test(ch);
  return /[A-Za-z0-9_.]/.test(ch);
}

function isExpression(value: string, ratLang: string): boolean {
  if (!value || /^\d/.test(value) || value.includes("..")) return false;
  if (ratLang === "js") return /^[$A-Za-z_][\w$]*(?:\.[$A-Za-z_][\w$]*)*$/.test(value);
  return /^[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*$/.test(value);
}

function sourceLocationFromInspection(info: string): SourceLocation | null {
  for (const line of info.split("\n")) {
    const match = line.match(/\bDefined in:\s+(.+?)\s*$/);
    if (!match) continue;
    const raw = match[1].trim();
    const pathLine = raw.match(/^(.*):(\d+)$/);
    if (pathLine) {
      return { path: pathLine[1], line: Math.max(0, Number(pathLine[2]) - 1) };
    }
    return { path: raw, line: 0 };
  }
  return null;
}

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#039;");
}

function style(): string {
  return `
    <style>
      body {
        padding: 0;
        color: var(--vscode-foreground);
        background: var(--vscode-sideBar-background);
        font-family: var(--vscode-font-family);
      }
      main { padding: 10px 12px; }
      h2 {
        font-size: 13px;
        font-weight: 600;
        margin: 0 0 10px;
        word-break: break-word;
      }
      pre {
        margin: 10px 0 0;
        padding: 10px;
        border-radius: 4px;
        white-space: pre-wrap;
        word-break: break-word;
        background: var(--vscode-editor-background);
        color: var(--vscode-editor-foreground);
        font-family: var(--vscode-editor-font-family), monospace;
        font-size: var(--vscode-editor-font-size);
        line-height: 1.35;
      }
      button {
        color: var(--vscode-button-foreground);
        background: var(--vscode-button-background);
        border: 0;
        border-radius: 2px;
        padding: 4px 8px;
        cursor: pointer;
      }
      button:hover { background: var(--vscode-button-hoverBackground); }
      .muted { color: var(--vscode-descriptionForeground); line-height: 1.4; }
      .source { color: var(--vscode-descriptionForeground); font-size: 11px; word-break: break-all; }
    </style>
  `;
}
