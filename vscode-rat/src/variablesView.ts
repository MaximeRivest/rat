/**
 * variablesView.ts — Rat sidebar variables view.
 *
 * Shows the current runtime namespace for the active rat file. This is a
 * persistent, side-panel version of `rat look`: variables at a glance, with a
 * click-through detail inspector powered by `lookFull(at=...)`.
 */

import * as vscode from "vscode";
import { parseCells } from "./cells";
import { detectFileLang, isRatFile } from "./langDetect";
import { existingOrRunningClient, type McpClient } from "./rat";
import { resolveRuntime } from "./resolve";

interface VariableRow {
  name: string;
  kind: string;
  preview: string;
}

interface RuntimeTarget {
  ratLang: string;
  document: vscode.TextDocument;
}

interface SourceLocation {
  path: string;
  line: number;
}

export class RatVariablesViewProvider implements vscode.WebviewViewProvider, vscode.Disposable {
  private view?: vscode.WebviewView;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private seq = 0;
  private target: RuntimeTarget | null = null;
  private overviewText = "";
  private rows: VariableRow[] = [];
  private selectedName: string | null = null;
  private selectedInfo: string | null = null;
  private readonly disposables: vscode.Disposable[] = [];

  resolveWebviewView(view: vscode.WebviewView): void {
    this.view = view;
    view.webview.options = { enableScripts: true };
    this.disposables.push(
      view.webview.onDidReceiveMessage((message) => {
        void this.handleMessage(message);
      }),
    );
    this.renderEmpty("Open a rat file and run a cell to see variables.");
    this.update(vscode.window.activeTextEditor);
  }

  update(editor: vscode.TextEditor | undefined): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => {
      void this.updateNow(editor);
    }, 200);
  }

  dispose(): void {
    if (this.timer) clearTimeout(this.timer);
    for (const disposable of this.disposables) disposable.dispose();
  }

  private async updateNow(editor: vscode.TextEditor | undefined): Promise<void> {
    if (!this.view) return;
    const seq = ++this.seq;
    this.target = editor ? runtimeTarget(editor.document) : null;
    this.selectedName = null;
    this.selectedInfo = null;

    if (!this.target) {
      this.renderEmpty("Open a rat file and run a cell to see variables.");
      return;
    }

    this.renderLoading("Loading variables…");

    const client = await this.currentClient();
    if (seq !== this.seq) return;

    if (!client) {
      this.renderEmpty("No running runtime for this file. Run a cell first.");
      return;
    }

    try {
      this.overviewText = await client.look();
      if (seq !== this.seq) return;
      this.rows = parseOverview(this.overviewText);
      this.render();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      this.renderEmpty(`Could not read variables: ${escapeHtml(msg)}`);
    }
  }

  private async handleMessage(message: unknown): Promise<void> {
    if (!message || typeof message !== "object") return;
    const msg = message as { command?: string; name?: string; path?: string; line?: number };

    if (msg.command === "refresh") {
      this.update(vscode.window.activeTextEditor);
      return;
    }

    if (msg.command === "inspect" && typeof msg.name === "string") {
      await this.inspectVariable(msg.name);
      return;
    }

    if (msg.command === "open" && typeof msg.path === "string") {
      const line = typeof msg.line === "number" ? msg.line : 0;
      const pos = new vscode.Position(Math.max(0, line), 0);
      await vscode.window.showTextDocument(vscode.Uri.file(msg.path), {
        preview: false,
        selection: new vscode.Range(pos, pos),
      });
    }
  }

  private async inspectVariable(name: string): Promise<void> {
    const client = await this.currentClient();
    if (!client) return;

    this.selectedName = name;
    this.selectedInfo = null;
    this.render();

    try {
      this.selectedInfo = await client.lookFull(name);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      this.selectedInfo = `Inspection failed: ${msg}`;
    }
    this.render();
  }

  private async currentClient(): Promise<McpClient | undefined> {
    if (!this.target) return undefined;
    const { name } = resolveRuntime(this.target.ratLang, this.target.document);
    return existingOrRunningClient(name);
  }

  private render(): void {
    if (!this.view) return;
    const rowsHtml = this.rows.length > 0
      ? this.rows.map((row) => this.renderRow(row)).join("\n")
      : `<p class="muted">No variables yet.</p><pre>${escapeHtml(this.overviewText)}</pre>`;

    const detailHtml = this.selectedName
      ? this.renderDetail(this.selectedName, this.selectedInfo)
      : `<p class="muted">Select a variable to inspect it.</p>`;

    this.setHtml(`
      ${style()}
      <main>
        <header>
          <h2>Variables</h2>
          <button id="refresh" title="Refresh variables">Refresh</button>
        </header>
        <section class="vars" aria-label="Variables">
          ${rowsHtml}
        </section>
        <section class="detail" aria-label="Selected variable">
          ${detailHtml}
        </section>
      </main>
      <script>
        const vscode = acquireVsCodeApi();
        document.getElementById('refresh')?.addEventListener('click', () => {
          vscode.postMessage({ command: 'refresh' });
        });
        document.querySelectorAll('[data-var]').forEach((row) => {
          row.addEventListener('click', () => {
            vscode.postMessage({ command: 'inspect', name: row.getAttribute('data-var') });
          });
        });
        document.getElementById('open-def')?.addEventListener('click', () => {
          vscode.postMessage({
            command: 'open',
            path: document.getElementById('open-def')?.getAttribute('data-path'),
            line: Number(document.getElementById('open-def')?.getAttribute('data-line') ?? 0),
          });
        });
      </script>
    `);
  }

  private renderRow(row: VariableRow): string {
    const selected = row.name === this.selectedName ? " selected" : "";
    return `
      <button class="var-row${selected}" data-var="${escapeAttr(row.name)}">
        <span class="name">${escapeHtml(row.name)}</span>
        <span class="kind">${escapeHtml(row.kind)}</span>
        <span class="preview">${escapeHtml(row.preview)}</span>
      </button>
    `;
  }

  private renderDetail(name: string, info: string | null): string {
    if (!info) {
      return `
        <h3>${escapeHtml(name)}</h3>
        <p class="muted">Inspecting…</p>
      `;
    }

    const source = sourceLocationFromInspection(info);
    const sourceHtml = source
      ? `<button id="open-def" data-path="${escapeAttr(source.path)}" data-line="${source.line}">Open definition</button><p class="source">${escapeHtml(source.path)}:${source.line + 1}</p>`
      : "";

    return `
      <h3>${escapeHtml(name)}</h3>
      ${sourceHtml}
      <pre>${escapeHtml(info)}</pre>
    `;
  }

  private renderLoading(message: string): void {
    this.setHtml(`
      ${style()}
      <main>
        <header><h2>Variables</h2></header>
        <p class="muted">${escapeHtml(message)}</p>
      </main>
    `);
  }

  private renderEmpty(message: string): void {
    this.setHtml(`
      ${style()}
      <main>
        <header>
          <h2>Variables</h2>
          <button id="refresh">Refresh</button>
        </header>
        <p class="muted">${message}</p>
      </main>
      <script>
        const vscode = acquireVsCodeApi();
        document.getElementById('refresh')?.addEventListener('click', () => vscode.postMessage({ command: 'refresh' }));
      </script>
    `);
  }

  private setHtml(body: string): void {
    if (!this.view) return;
    this.view.webview.html = `<!doctype html><html><head><meta charset="UTF-8"></head><body>${body}</body></html>`;
  }
}

function runtimeTarget(document: vscode.TextDocument): RuntimeTarget | null {
  if (!isRatFile(document)) return null;

  const fl = detectFileLang(document);
  if (fl.mode === "source") {
    return fl.ratLang ? { ratLang: fl.ratLang, document } : null;
  }

  const cells = parseCells(document);
  const first = cells.find((cell) => cell.executable);
  return first ? { ratLang: first.ratLang, document } : null;
}

function parseOverview(text: string): VariableRow[] {
  const rows: VariableRow[] = [];
  const lines = text.split("\n");
  for (const raw of lines.slice(1)) {
    const line = raw.trimEnd();
    if (!line.trim()) continue;
    if (/^[-=]+$/.test(line.trim())) continue;
    if (/^\S+\s+(?:idle|running|waiting)/.test(line)) continue;

    const match = line.match(/^\s*(\S+)\s{2,}(\S+)\s{2,}(.*)$/);
    if (match) {
      rows.push({ name: match[1], kind: match[2], preview: match[3] });
      continue;
    }

    const parts = line.trim().split(/\s+/);
    if (parts.length >= 2 && /^[A-Za-z_]\w*$/.test(parts[0])) {
      rows.push({ name: parts[0], kind: parts[1], preview: parts.slice(2).join(" ") });
    }
  }
  return rows;
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

function escapeAttr(value: string): string {
  return escapeHtml(value).replace(/`/g, "&#096;");
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
      main { padding: 10px 10px 14px; }
      header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
        margin-bottom: 8px;
      }
      h2, h3 {
        margin: 0;
        font-size: 13px;
        font-weight: 600;
      }
      h3 { margin-bottom: 8px; word-break: break-word; }
      button {
        font: inherit;
      }
      header button, #open-def {
        color: var(--vscode-button-foreground);
        background: var(--vscode-button-background);
        border: 0;
        border-radius: 2px;
        padding: 3px 7px;
        cursor: pointer;
      }
      header button:hover, #open-def:hover { background: var(--vscode-button-hoverBackground); }
      .vars {
        border: 1px solid var(--vscode-sideBarSectionHeader-border, transparent);
        border-radius: 4px;
        overflow: hidden;
        background: var(--vscode-editor-background);
      }
      .var-row {
        display: grid;
        grid-template-columns: minmax(55px, 0.8fr) minmax(45px, 0.6fr) minmax(90px, 1.6fr);
        width: 100%;
        gap: 6px;
        padding: 5px 7px;
        border: 0;
        border-bottom: 1px solid var(--vscode-sideBarSectionHeader-border, transparent);
        color: var(--vscode-foreground);
        background: transparent;
        text-align: left;
        cursor: pointer;
      }
      .var-row:hover { background: var(--vscode-list-hoverBackground); }
      .var-row.selected { background: var(--vscode-list-activeSelectionBackground); color: var(--vscode-list-activeSelectionForeground); }
      .var-row span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
      .name { font-weight: 600; color: var(--vscode-symbolIcon-variableForeground, var(--vscode-foreground)); }
      .kind { color: var(--vscode-descriptionForeground); }
      .preview { color: var(--vscode-descriptionForeground); }
      .detail { margin-top: 10px; }
      pre {
        margin: 8px 0 0;
        padding: 9px;
        border-radius: 4px;
        white-space: pre-wrap;
        word-break: break-word;
        background: var(--vscode-editor-background);
        color: var(--vscode-editor-foreground);
        font-family: var(--vscode-editor-font-family), monospace;
        font-size: var(--vscode-editor-font-size);
        line-height: 1.35;
      }
      .muted, .source {
        color: var(--vscode-descriptionForeground);
        line-height: 1.4;
      }
      .source { font-size: 11px; word-break: break-all; }
    </style>
  `;
}
