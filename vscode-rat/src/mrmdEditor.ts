/**
 * mrmdEditor.ts — VS Code custom text editor backed by mrmd-editor.
 *
 * This is not the Markdown preview. It is a real editable text editor in a
 * webview: mrmd renders Markdown on blur/focus and delegates executable code
 * cells back to rat kernels in the extension host.
 */

import * as fs from "fs";
import * as path from "path";
import { fileURLToPath } from "url";
import * as vscode from "vscode";

import { ratLangForFence } from "./cells";
import type { ExecutionController } from "./queue";
import { existingOrRunningClient } from "./rat";
import { resolveRuntime } from "./resolve";

interface WebviewMessage {
  type: string;
  id?: number;
  text?: string;
  code?: string;
  language?: string;
  expression?: string | null;
  at?: string | null;
  cursor?: number;
  url?: string;
  mimeType?: string;
  assetType?: string;
  version?: number;
}

export interface RatMrmdEditorCallbacks {
  onActiveDocument?: (document: vscode.TextDocument) => void;
  onSelection?: (
    document: vscode.TextDocument,
    language: string | null,
    expression: string | null,
  ) => void;
}

const livePanels = new Set<vscode.WebviewPanel>();

export function syncRatMarkdownThemes(): void {
  for (const panel of livePanels) {
    void panel.webview.postMessage({ type: "themeChanged" });
  }
}

interface RatMarkdownAppearance {
  pageMode: string;
  themeAssociations: Record<string, string>;
  canvasBackground: string;
  fontScale: number;
}

export function syncRatMarkdownPageMode(): void {
  const appearance = ratMarkdownAppearance();
  for (const panel of livePanels) {
    void panel.webview.postMessage({ type: "pageModeChanged", pageMode: appearance.pageMode });
  }
}

export function syncRatMarkdownAppearance(): void {
  const appearance = ratMarkdownAppearance();
  for (const panel of livePanels) {
    void panel.webview.postMessage({ type: "appearanceChanged", ...appearance });
  }
}

export class RatMrmdEditorProvider implements vscode.CustomTextEditorProvider {
  static readonly viewType = "rat.mrmdEditor";

  constructor(
    private readonly ctx: vscode.ExtensionContext,
    private readonly execution: ExecutionController,
    private readonly callbacks: RatMrmdEditorCallbacks = {},
  ) {}

  static register(
    ctx: vscode.ExtensionContext,
    execution: ExecutionController,
    callbacks: RatMrmdEditorCallbacks = {},
  ): vscode.Disposable {
    return vscode.window.registerCustomEditorProvider(
      RatMrmdEditorProvider.viewType,
      new RatMrmdEditorProvider(ctx, execution, callbacks),
      {
        supportsMultipleEditorsPerDocument: false,
        webviewOptions: {
          retainContextWhenHidden: true,
        },
      },
    );
  }

  async resolveCustomTextEditor(
    document: vscode.TextDocument,
    webviewPanel: vscode.WebviewPanel,
    _token: vscode.CancellationToken,
  ): Promise<void> {
    webviewPanel.webview.options = {
      enableScripts: true,
      localResourceRoots: this.localResourceRoots(document),
    };
    webviewPanel.webview.html = this.html(webviewPanel.webview, document);
    livePanels.add(webviewPanel);

    const disposables: vscode.Disposable[] = [];
    let ignoredDocumentText: string | undefined;
    let lastWebviewText = document.getText();

    const postDocumentContent = () => {
      const text = document.getText();
      lastWebviewText = text;
      void webviewPanel.webview.postMessage({
        type: "setContent",
        text,
        version: document.version,
      });
    };

    disposables.push(
      vscode.workspace.onDidChangeTextDocument((e) => {
        if (e.document.uri.toString() !== document.uri.toString()) return;

        const next = document.getText();
        if (ignoredDocumentText === next) {
          ignoredDocumentText = undefined;
          lastWebviewText = next;
          return;
        }

        postDocumentContent();
      }),
    );

    disposables.push(
      webviewPanel.webview.onDidReceiveMessage(async (message: WebviewMessage) => {
        try {
          switch (message.type) {
            case "ready":
              this.noteActiveDocument(document);
              await this.initializeWebview(webviewPanel, document);
              break;

            case "selection":
              this.handleSelection(document, message);
              break;

            case "edit":
              if (typeof message.text === "string" && message.text !== document.getText()) {
                const baseVersion = typeof message.version === "number" ? message.version : undefined;
                const currentText = document.getText();

                if (
                  baseVersion !== undefined &&
                  baseVersion !== document.version &&
                  currentText !== lastWebviewText
                ) {
                  await webviewPanel.webview.postMessage({
                    type: "editConflict",
                    text: currentText,
                    version: document.version,
                  });
                  break;
                }

                ignoredDocumentText = message.text;
                const applied = await this.updateTextDocument(document, message.text);
                if (applied) {
                  lastWebviewText = message.text;
                  await webviewPanel.webview.postMessage({
                    type: "editApplied",
                    text: message.text,
                    version: document.version,
                  });
                }
              }
              break;

            case "save":
              await document.save();
              break;

            case "openText":
              await vscode.window.showTextDocument(document, {
                preview: false,
                viewColumn: webviewPanel.viewColumn,
              });
              break;

            case "ratRun":
              await this.handleRatRun(webviewPanel, document, message);
              break;

            case "ratCancel":
              this.handleRatCancel(webviewPanel, message);
              break;

            case "ratAsset":
              await this.handleRatAsset(webviewPanel, document, message);
              break;

            case "ratComplete":
              await this.handleRatComplete(webviewPanel, document, message);
              break;

            case "ratInspect":
              await this.handleRatInspect(webviewPanel, document, message);
              break;
          }
        } catch (err: unknown) {
          const msg = err instanceof Error ? err.message : String(err);
          if (typeof message.id === "number") {
            await this.postRpcError(webviewPanel, message.id, msg);
          } else {
            await webviewPanel.webview.postMessage({ type: "showError", message: msg });
          }
        }
      }),
    );

    disposables.push(
      webviewPanel.onDidChangeViewState((event) => {
        if (event.webviewPanel.active || event.webviewPanel.visible) {
          this.noteActiveDocument(document);
        }
      }),
    );

    webviewPanel.onDidDispose(() => {
      livePanels.delete(webviewPanel);
      for (const d of disposables) d.dispose();
    });
  }

  private async initializeWebview(
    webviewPanel: vscode.WebviewPanel,
    document: vscode.TextDocument,
  ): Promise<void> {
    const folder = vscode.workspace.getWorkspaceFolder(document.uri);
    const docDir = document.uri.scheme === "file"
      ? vscode.Uri.file(path.dirname(document.uri.fsPath))
      : document.uri;

    await webviewPanel.webview.postMessage({
      type: "init",
      text: document.getText(),
      version: document.version,
      theme: currentMrmdTheme(),
      docDirWebviewUri: webviewPanel.webview.asWebviewUri(docDir).toString(),
      ...ratMarkdownAppearance(),
      projectRoot: folder?.uri.fsPath ?? path.dirname(document.uri.fsPath),
      documentPath: document.uri.scheme === "file" ? document.uri.fsPath : document.uri.toString(),
    });
  }

  private async updateTextDocument(
    document: vscode.TextDocument,
    text: string,
  ): Promise<boolean> {
    const edit = new vscode.WorkspaceEdit();
    edit.replace(document.uri, fullDocumentRange(document), text);
    return vscode.workspace.applyEdit(edit);
  }

  private noteActiveDocument(document: vscode.TextDocument): void {
    this.callbacks.onActiveDocument?.(document);
  }

  private handleSelection(document: vscode.TextDocument, message: WebviewMessage): void {
    this.noteActiveDocument(document);
    const language = typeof message.language === "string" && message.language.length > 0
      ? message.language
      : null;
    const expression = typeof message.expression === "string" && message.expression.length > 0
      ? message.expression
      : null;
    this.callbacks.onSelection?.(document, language, expression);
  }

  private async handleRatRun(
    webviewPanel: vscode.WebviewPanel,
    document: vscode.TextDocument,
    message: WebviewMessage,
  ): Promise<void> {
    const id = message.id;
    if (typeof id !== "number") return;

    const runtime = this.resolveRuntimeForMessage(document, message.language);
    if (!runtime) {
      await webviewPanel.webview.postMessage({
        type: "ratDone",
        id,
        success: false,
        stdout: "",
        stderr: `No rat runtime for language: ${message.language ?? ""}`,
        error: { message: `No rat runtime for language: ${message.language ?? ""}` },
      });
      return;
    }

    this.execution.enqueue({
      kind: "webview",
      id,
      code: message.code ?? "",
      document,
      webviewPanel,
      runtimeName: runtime.name,
      cwd: runtime.cwd,
      lang: runtime.lang,
      onDidFinish: () => this.noteActiveDocument(document),
    });
  }

  private handleRatCancel(
    webviewPanel: vscode.WebviewPanel,
    message: WebviewMessage,
  ): void {
    if (typeof message.id !== "number") return;
    this.execution.cancelWebviewRun(webviewPanel, message.id);
  }

  private async handleRatAsset(
    webviewPanel: vscode.WebviewPanel,
    document: vscode.TextDocument,
    message: WebviewMessage,
  ): Promise<void> {
    if (typeof message.id !== "number") return;

    try {
      const src = localPathFromAssetUrl(message.url ?? "");
      if (!src || !fs.existsSync(src)) {
        throw new Error(`Asset not found: ${message.url ?? ""}`);
      }

      const assetsRel: string = vscode.workspace
        .getConfiguration("rat")
        .get("assetsDir", "_assets");
      const folder = vscode.workspace.getWorkspaceFolder(document.uri);
      const projectRoot = folder?.uri.fsPath ?? path.dirname(document.uri.fsPath);
      const assetsAbs = path.join(projectRoot, assetsRel);
      fs.mkdirSync(assetsAbs, { recursive: true });

      const dest = uniquePath(path.join(assetsAbs, path.basename(src)));
      fs.copyFileSync(src, dest);

      const rel = path
        .relative(path.dirname(document.uri.fsPath), dest)
        .split(path.sep)
        .join("/");

      await this.postRpcResult(webviewPanel, message.id, {
        assetPath: dest,
        relativePath: rel,
      });
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      await this.postRpcError(webviewPanel, message.id, msg);
    }
  }

  private async handleRatComplete(
    webviewPanel: vscode.WebviewPanel,
    document: vscode.TextDocument,
    message: WebviewMessage,
  ): Promise<void> {
    if (typeof message.id !== "number") return;

    const runtime = this.resolveRuntimeForMessage(document, message.language);
    if (!runtime) {
      await this.postRpcResult(webviewPanel, message.id, { items: [] });
      return;
    }

    const client = await existingOrRunningClient(runtime.name);
    if (!client) {
      await this.postRpcResult(webviewPanel, message.id, { items: [] });
      return;
    }

    try {
      const items = await client.complete(message.code ?? "", message.cursor ?? 0);
      await this.postRpcResult(webviewPanel, message.id, { items });
    } catch {
      await this.postRpcResult(webviewPanel, message.id, { items: [] });
    }
  }

  private async handleRatInspect(
    webviewPanel: vscode.WebviewPanel,
    document: vscode.TextDocument,
    message: WebviewMessage,
  ): Promise<void> {
    if (typeof message.id !== "number") return;

    const runtime = this.resolveRuntimeForMessage(document, message.language);
    if (!runtime) {
      await this.postRpcResult(webviewPanel, message.id, { text: "" });
      return;
    }

    const client = await existingOrRunningClient(runtime.name);
    if (!client) {
      await this.postRpcResult(webviewPanel, message.id, { text: "" });
      return;
    }

    try {
      const text = message.at ? await client.look(message.at) : await client.look();
      await this.postRpcResult(webviewPanel, message.id, { text });
    } catch {
      await this.postRpcResult(webviewPanel, message.id, { text: "" });
    }
  }

  private resolveRuntimeForMessage(
    document: vscode.TextDocument,
    language: string | undefined,
  ): { name: string; cwd: string; lang: string } | null {
    if (!language) return null;
    const ratLang = ratLangForFence(language);
    if (!ratLang) return null;
    const resolved = resolveRuntime(ratLang, document);
    return { name: resolved.name, cwd: resolved.cwd, lang: resolved.lang };
  }

  private async postRpcResult(
    webviewPanel: vscode.WebviewPanel,
    id: number,
    result: unknown,
  ): Promise<void> {
    await webviewPanel.webview.postMessage({ type: "rpcResult", id, result });
  }

  private async postRpcError(
    webviewPanel: vscode.WebviewPanel,
    id: number,
    error: string,
  ): Promise<void> {
    await webviewPanel.webview.postMessage({ type: "rpcError", id, error });
  }

  private localResourceRoots(document: vscode.TextDocument): vscode.Uri[] {
    const roots = [vscode.Uri.joinPath(this.ctx.extensionUri, "media")];
    const folders = vscode.workspace.workspaceFolders ?? [];
    for (const folder of folders) roots.push(folder.uri);

    if (document.uri.scheme === "file") {
      roots.push(vscode.Uri.file(path.dirname(document.uri.fsPath)));
    }

    return roots;
  }

  private html(webview: vscode.Webview, document: vscode.TextDocument): string {
    const nonce = getNonce();
    const mrmdUri = webview.asWebviewUri(
      vscode.Uri.joinPath(this.ctx.extensionUri, "media", "mrmd.iife.min.js"),
    );
    const scriptUri = webview.asWebviewUri(
      vscode.Uri.joinPath(this.ctx.extensionUri, "media", "mrmdEditor.js"),
    );

    const title = escapeHtml(path.basename(document.uri.fsPath || document.uri.path));

    return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src ${webview.cspSource} https: http: data: blob:; font-src ${webview.cspSource} data:; style-src ${webview.cspSource} 'unsafe-inline'; script-src ${webview.cspSource} 'nonce-${nonce}' 'unsafe-eval';">
  <title>${title}</title>
  <style nonce="${nonce}">
    html, body {
      width: 100%;
      height: 100%;
      margin: 0;
      padding: 0;
      overflow: hidden;
      background: var(--vscode-editor-background);
      color: var(--vscode-editor-foreground);
      font-family: var(--vscode-font-family);
    }

    #shell {
      display: flex;
      flex-direction: column;
      width: 100%;
      height: 100%;
    }

    #toolbar {
      display: flex;
      align-items: center;
      gap: 0.5rem;
      min-height: 30px;
      padding: 0 10px;
      border-bottom: 1px solid var(--vscode-editorWidget-border, transparent);
      background: var(--vscode-sideBar-background, var(--vscode-editor-background));
      color: var(--vscode-sideBar-foreground, var(--vscode-editor-foreground));
      font-size: 12px;
      user-select: none;
    }

    #toolbar .spacer { flex: 1; }

    #toolbar button {
      border: 1px solid var(--vscode-button-border, transparent);
      border-radius: 3px;
      padding: 2px 8px;
      color: var(--vscode-button-secondaryForeground, var(--vscode-button-foreground));
      background: var(--vscode-button-secondaryBackground, var(--vscode-button-background));
      font: inherit;
      cursor: pointer;
    }

    #toolbar button:hover {
      background: var(--vscode-button-secondaryHoverBackground, var(--vscode-button-hoverBackground));
    }

    #status {
      opacity: 0.75;
    }

    #editor {
      flex: 1;
      min-height: 0;
      overflow: auto;
    }

    .mrmd-root {
      height: 100%;
    }
  </style>
</head>
<body>
  <div id="shell">
    <div id="toolbar">
      <strong>Rat Markdown</strong>
      <span id="status">Loading…</span>
      <span class="spacer"></span>
      <button id="openText" title="Open the same document in VS Code's text editor">Text editor</button>
      <button id="save" title="Save document">Save</button>
    </div>
    <div id="editor"></div>
  </div>
  <script nonce="${nonce}" src="${mrmdUri}"></script>
  <script nonce="${nonce}" src="${scriptUri}"></script>
</body>
</html>`;
  }
}

export async function openRenderedMarkdownEditor(uri?: vscode.Uri): Promise<void> {
  const targetUri = uri ?? vscode.window.activeTextEditor?.document.uri;
  if (!targetUri) {
    vscode.window.showInformationMessage("Open a Markdown document first.");
    return;
  }

  await vscode.commands.executeCommand(
    "vscode.openWith",
    targetUri,
    RatMrmdEditorProvider.viewType,
    { viewColumn: vscode.ViewColumn.Active, preview: false },
  );
}

export async function setMarkdownPreviewShortcutReplacement(enabled: boolean): Promise<void> {
  const target = configurationTarget();
  await vscode.workspace
    .getConfiguration("rat")
    .update("replaceMarkdownPreviewShortcut", enabled, target);

  vscode.window.showInformationMessage(
    enabled
      ? "Rat Markdown will now open for Ctrl+Shift+V in Markdown files."
      : "Ctrl+Shift+V restored to VS Code's Markdown preview.",
  );
}

export async function useRatMarkdownAsDefaultEditor(): Promise<void> {
  await updateEditorAssociations(true);
}

export async function restoreDefaultMarkdownEditor(): Promise<void> {
  await updateEditorAssociations(false);
}

const MARKDOWN_EDITOR_PATTERNS = ["*.md", "*.qmd", "*.rmd", "*.Rmd"];

async function updateEditorAssociations(enable: boolean): Promise<void> {
  const target = configurationTarget();
  const cfg = vscode.workspace.getConfiguration("workbench");
  const current = cfg.get<Record<string, string>>("editorAssociations", {});
  const next: Record<string, string> = { ...current };

  for (const pattern of MARKDOWN_EDITOR_PATTERNS) {
    if (enable) {
      next[pattern] = RatMrmdEditorProvider.viewType;
    } else if (next[pattern] === RatMrmdEditorProvider.viewType) {
      delete next[pattern];
    }
  }

  await cfg.update("editorAssociations", next, target);
  vscode.window.showInformationMessage(
    enable
      ? "Rat Markdown is now the default editor for Markdown-like files in this scope."
      : "Rat Markdown default editor associations were removed in this scope.",
  );
}

function configurationTarget(): vscode.ConfigurationTarget {
  return vscode.workspace.workspaceFolders?.length
    ? vscode.ConfigurationTarget.Workspace
    : vscode.ConfigurationTarget.Global;
}

function ratMarkdownAppearance(): RatMarkdownAppearance {
  const cfg = vscode.workspace.getConfiguration("rat");
  return {
    pageMode: cfg.get<string>("markdownPageMode", "never"),
    themeAssociations: cfg.get<Record<string, string>>("markdownThemeAssociations", {
      light: "vscode-host-light",
      dark: "vscode-host-dark",
      highContrast: "vscode-host-dark",
      highContrastLight: "vscode-host-light",
    }),
    canvasBackground: cfg.get<string>("markdownPageCanvasBackground", "auto"),
    fontScale: cfg.get<number>("markdownFontScale", 1),
  };
}

export async function configureRatMarkdownAppearance(): Promise<void> {
  await vscode.commands.executeCommand("workbench.action.openSettings", "rat markdown");
}

function currentMrmdTheme(): string {
  const kind = vscode.window.activeColorTheme.kind;
  return kind === vscode.ColorThemeKind.Dark || kind === vscode.ColorThemeKind.HighContrast
    ? "plain-dark"
    : "plain-light";
}

function fullDocumentRange(document: vscode.TextDocument): vscode.Range {
  const lastLine = document.lineAt(document.lineCount - 1);
  return new vscode.Range(0, 0, document.lineCount - 1, lastLine.text.length);
}

function localPathFromAssetUrl(url: string): string {
  if (!url) return "";
  if (/^file:/i.test(url)) return fileURLToPath(url);
  return url;
}

function uniquePath(candidate: string): string {
  if (!fs.existsSync(candidate)) return candidate;

  const dir = path.dirname(candidate);
  const ext = path.extname(candidate);
  const base = path.basename(candidate, ext);

  for (let i = 1; i < 10_000; i++) {
    const next = path.join(dir, `${base}-${i}${ext}`);
    if (!fs.existsSync(next)) return next;
  }

  return path.join(dir, `${base}-${Date.now()}${ext}`);
}

function getNonce(): string {
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  let nonce = "";
  for (let i = 0; i < 32; i++) {
    nonce += chars.charAt(Math.floor(Math.random() * chars.length));
  }
  return nonce;
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}
