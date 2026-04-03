/**
 * extension.ts — Entry point.
 *
 * Wires cells, queue, CodeLens, completions, hover, and commands
 * into VS Code.  The extension is intentionally thin — rat handles
 * kernel lifecycle, venv discovery, and execution.
 */

import * as vscode from "vscode";

import {
  parseCells,
  cellAtLine,
  cellsUpTo,
} from "./cells";
import { RatCodeLensProvider } from "./codeLens";
import { RatCompletionProvider, RatHoverProvider } from "./intellisense";
import { clearAllOutputs } from "./output";
import { ExecutionQueue, type QueueState } from "./queue";
import { resolveRuntime } from "./resolve";
import { disposeAll, getClient, ratStop, ratRestart, readState } from "./rat";
import {
  RuntimeTreeProvider,
  startRuntimeCmd,
  stopRuntimeCmd,
  restartRuntimeCmd,
  removeRuntimeCmd,
} from "./runtimeView";
import { showRuntimePicker } from "./runtimePicker";
import { getRuntimeOverride } from "./resolve";
import { applyDecorations, disposeDecorations } from "./decorations";
import { RatSnippetProvider } from "./snippets";

// ── Globals ────────────────────────────────────────────────

let statusBar: vscode.StatusBarItem;
let runtimeBar: vscode.StatusBarItem;
let queue: ExecutionQueue;
let treeProvider: RuntimeTreeProvider;

// ── Activate ───────────────────────────────────────────────

export function activate(ctx: vscode.ExtensionContext): void {
  // Status bar — queue state
  statusBar = vscode.window.createStatusBarItem(
    vscode.StatusBarAlignment.Left,
    51,
  );
  statusBar.command = "rat.togglePause";
  setStatus("idle", 0);
  statusBar.show();
  ctx.subscriptions.push(statusBar);

  // Status bar — current runtime (clickable → picker)
  runtimeBar = vscode.window.createStatusBarItem(
    vscode.StatusBarAlignment.Left,
    50,
  );
  runtimeBar.command = "rat.selectRuntime";
  runtimeBar.tooltip = "Click to select rat runtime";
  ctx.subscriptions.push(runtimeBar);

  // Tree view
  treeProvider = new RuntimeTreeProvider();
  const treeView = vscode.window.createTreeView("ratRuntimes", {
    treeDataProvider: treeProvider,
  });
  treeProvider.startAutoRefresh(4000);
  ctx.subscriptions.push(treeView);

  // Execution queue
  queue = new ExecutionQueue((state, pending) => {
    setStatus(state, pending);
    vscode.commands.executeCommand(
      "setContext",
      "rat.executing",
      state === "running",
    );
  });

  // Providers — registered for markdown + quarto + rmarkdown files
  const sel: vscode.DocumentSelector = [
    { language: "markdown" },
    { language: "quarto" },
    { pattern: "**/*.qmd" },
    { pattern: "**/*.rmd" },
    { pattern: "**/*.Rmd" },
  ];

  const codeLens = new RatCodeLensProvider();

  ctx.subscriptions.push(
    vscode.languages.registerCodeLensProvider(sel, codeLens),
    vscode.languages.registerCompletionItemProvider(
      sel,
      new RatCompletionProvider(),
      ".",
      "(",
      "=",
      "[",
    ),
    vscode.languages.registerHoverProvider(sel, new RatHoverProvider()),
    vscode.languages.registerCompletionItemProvider(
      sel,
      new RatSnippetProvider(),
    ),
  );

  // Context key + runtime bar update + decorations on editor switch
  const updateCtx = () => {
    const ed = vscode.window.activeTextEditor;
    const active = !!ed && isSupported(ed.document);
    vscode.commands.executeCommand("setContext", "rat.activeFile", active);
    updateRuntimeBar(ed);
    if (ed && isSupported(ed.document)) {
      applyDecorations(ed);
    }
  };
  ctx.subscriptions.push(
    vscode.window.onDidChangeActiveTextEditor(updateCtx),
    vscode.workspace.onDidChangeTextDocument((e) => {
      updateRuntimeBar(vscode.window.activeTextEditor);
      // Refresh decorations when the document changes
      const ed = vscode.window.activeTextEditor;
      if (ed && ed.document === e.document && isSupported(ed.document)) {
        applyDecorations(ed);
      }
    }),
  );
  updateCtx();

  // ── Commands ─────────────────────────────────────────────

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const reg = (id: string, fn: (...a: any[]) => unknown) =>
    ctx.subscriptions.push(vscode.commands.registerCommand(id, fn));

  reg("rat.runCell", () => runCellCmd(false));
  reg("rat.runCellAndAdvance", () => runCellCmd(true));
  reg("rat.runAbove", runAboveCmd);
  reg("rat.runAll", runAllCmd);
  reg("rat.runCellAt", (line: number) => runCellAtCmd(line));
  reg("rat.runAboveAt", (line: number) => runAboveAtCmd(line));

  reg("rat.cancelExecution", () => queue.cancelAll());
  reg("rat.clearQueue", () => queue.clear());
  reg("rat.togglePause", () => queue.togglePause());

  reg("rat.insertCell", insertCellCmd);
  reg("rat.clearOutputs", clearOutputsCmd);
  reg("rat.showVariables", showVariablesCmd);
  reg("rat.stopKernel", stopKernelCmd);
  reg("rat.restartKernel", restartKernelCmd);

  // Runtime management
  reg("rat.selectRuntime", async () => {
    await showRuntimePicker();
    updateRuntimeBar(vscode.window.activeTextEditor);
    treeProvider.refresh();
  });
  reg("rat.refreshRuntimes", () => treeProvider.refresh());
  reg("rat.startRuntime", (n: any) => startRuntimeCmd(n).then(() => treeProvider.refresh()));
  reg("rat.stopRuntime", (n: any) => stopRuntimeCmd(n).then(() => { disposeAll(); treeProvider.refresh(); }));
  reg("rat.restartRuntime", (n: any) => restartRuntimeCmd(n).then(() => { disposeAll(); treeProvider.refresh(); }));
  reg("rat.removeRuntime", (n: any) => removeRuntimeCmd(n).then(() => treeProvider.refresh()));
  reg("rat.addRuntime", async () => { await showRuntimePicker(); treeProvider.refresh(); });
}

// ── Deactivate ─────────────────────────────────────────────

export function deactivate(): void {
  treeProvider?.stopAutoRefresh();
  disposeDecorations();
  disposeAll();
}

// ── Helpers ────────────────────────────────────────────────

function isSupported(doc: vscode.TextDocument): boolean {
  if (["markdown", "quarto", "rmarkdown"].includes(doc.languageId)) {
    return true;
  }
  const ext = doc.fileName.split(".").pop()?.toLowerCase();
  return ["qmd", "rmd", "md"].includes(ext ?? "");
}

function setStatus(state: QueueState, pending: number): void {
  switch (state) {
    case "idle":
      statusBar.text = "$(circle-outline) Rat";
      statusBar.tooltip = "Rat: idle";
      break;
    case "running":
      statusBar.text =
        `$(loading~spin) Rat` + (pending ? ` [${pending}]` : "");
      statusBar.tooltip =
        "Rat: running" + (pending ? ` — ${pending} queued` : "");
      break;
    case "paused":
      statusBar.text = `$(debug-pause) Rat [${pending}]`;
      statusBar.tooltip = `Rat: paused — ${pending} queued (click to resume)`;
      break;
  }
}

function updateRuntimeBar(editor: vscode.TextEditor | undefined): void {
  if (!editor || !isSupported(editor.document)) {
    runtimeBar.hide();
    return;
  }
  const cells = parseCells(editor.document);
  if (cells.length === 0) {
    runtimeBar.hide();
    return;
  }

  // Show the runtime for the first cell's language
  const lang = cells[0].ratLang;
  const docUri = editor.document.uri.toString();
  const override = getRuntimeOverride(docUri, lang);
  const { name } = resolveRuntime(lang, editor.document);

  // Check if kernel is running
  const state = readState();
  const kernel = state.kernels.find((k) => k.name === name);
  const icon = kernel ? "$(circle-filled)" : "$(circle-outline)";

  runtimeBar.text = `${icon} ${name}`;
  runtimeBar.tooltip = kernel
    ? `Runtime: ${name} (running on :${kernel.port})\nClick to switch`
    : `Runtime: ${name} (not running)\nClick to switch`;
  if (override) {
    runtimeBar.text += " (override)";
  }
  runtimeBar.show();
}

function enqueue(
  editor: vscode.TextEditor,
  cell: ReturnType<typeof parseCells>[number],
): void {
  const { name, cwd, lang } = resolveRuntime(cell.ratLang, editor.document);
  queue.enqueue({ cell, editor, runtimeName: name, cwd, lang });
}

// ── Command implementations ───────────────────────────────

function runCellCmd(advance: boolean): void {
  const editor = vscode.window.activeTextEditor;
  if (!editor || !isSupported(editor.document)) return;

  const cells = parseCells(editor.document);
  const cell = cellAtLine(cells, editor.selection.active.line);
  if (!cell?.executable) {
    vscode.window.showInformationMessage("No executable code cell at cursor");
    return;
  }

  enqueue(editor, cell);

  if (advance) {
    const idx = cells.indexOf(cell);
    if (idx < cells.length - 1) {
      const next = cells[idx + 1];
      const pos = new vscode.Position(next.openLine + 1, 0);
      editor.selection = new vscode.Selection(pos, pos);
      editor.revealRange(new vscode.Range(pos, pos));
    }
  }
}

function runCellAtCmd(line: number): void {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;
  const cell = parseCells(editor.document).find((c) => c.openLine === line);
  if (cell?.executable) enqueue(editor, cell);
}

function runAboveCmd(): void {
  const editor = vscode.window.activeTextEditor;
  if (!editor || !isSupported(editor.document)) return;
  for (const c of cellsUpTo(
    parseCells(editor.document),
    editor.selection.active.line,
  )) {
    if (c.executable) enqueue(editor, c);
  }
}

function runAboveAtCmd(line: number): void {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;
  for (const c of cellsUpTo(parseCells(editor.document), line)) {
    if (c.executable) enqueue(editor, c);
  }
}

function runAllCmd(): void {
  const editor = vscode.window.activeTextEditor;
  if (!editor || !isSupported(editor.document)) return;
  for (const c of parseCells(editor.document)) {
    if (c.executable) enqueue(editor, c);
  }
}

// ── Reverse map: ratLang → canonical fence name ───────────

const RAT_TO_FENCE: Record<string, string> = {
  py: "python",
  r: "r",
  sh: "bash",
  jl: "julia",
  js: "javascript",
};

async function insertCellCmd(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor || !isSupported(editor.document)) return;

  // Find the most common fence language in the document
  const cells = parseCells(editor.document);
  const fenceLang = dominantFenceLang(cells);

  const cursorLine = editor.selection.active.line;
  const lineText = editor.document.lineAt(cursorLine).text;

  // Insert after the current line (or at start if on an empty first line)
  const insertLine = cursorLine;
  const insertAt = new vscode.Position(insertLine, lineText.length);

  const openFence = "```" + fenceLang;
  // Build: newline, opening fence, newline, empty code line, newline, closing fence
  const prefix = lineText.length > 0 || cursorLine > 0 ? "\n\n" : "";
  const snippet = prefix + openFence + "\n" + "\n" + "```";

  await editor.edit((eb) => {
    eb.insert(insertAt, snippet);
  });

  // Place cursor on the empty line inside the cell (2 lines below opening fence)
  const openFenceLine = insertLine + (prefix.length > 0 ? 2 : 0);
  const bodyLine = openFenceLine + 1;
  const pos = new vscode.Position(bodyLine, 0);
  editor.selection = new vscode.Selection(pos, pos);
  editor.revealRange(new vscode.Range(pos, pos));
}

/** Return the fence language used most often, or "python" as fallback. */
function dominantFenceLang(cells: ReturnType<typeof parseCells>): string {
  if (cells.length === 0) return "python";

  const counts = new Map<string, number>();
  for (const c of cells) {
    counts.set(c.lang, (counts.get(c.lang) ?? 0) + 1);
  }

  let best = cells[0].lang;
  let bestCount = 0;
  for (const [lang, count] of counts) {
    if (count > bestCount) {
      best = lang;
      bestCount = count;
    }
  }
  return best;
}

async function clearOutputsCmd(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (editor) await clearAllOutputs(editor);
}

async function showVariablesCmd(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;
  const cells = parseCells(editor.document);
  if (!cells.length) return;

  const { name, cwd, lang } = resolveRuntime(cells[0].ratLang, editor.document);
  try {
    const client = await getClient(name, cwd, lang);
    const text = await client.look();
    const doc = await vscode.workspace.openTextDocument({
      content: text,
      language: "plaintext",
    });
    await vscode.window.showTextDocument(doc, {
      viewColumn: vscode.ViewColumn.Beside,
      preview: true,
    });
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    vscode.window.showErrorMessage(`Rat: ${msg}`);
  }
}

async function stopKernelCmd(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;
  const cells = parseCells(editor.document);
  if (!cells.length) return;

  const { name } = resolveRuntime(cells[0].ratLang, editor.document);
  try {
    await ratStop(name);
    disposeAll();
    vscode.window.showInformationMessage(`Rat: ${name} stopped`);
  } catch {
    /* swallow */
  }
}

async function restartKernelCmd(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;
  const cells = parseCells(editor.document);
  if (!cells.length) return;

  const { name, cwd, lang: _lang } = resolveRuntime(cells[0].ratLang, editor.document);
  try {
    disposeAll();
    await ratRestart(name, cwd);
    vscode.window.showInformationMessage(`Rat: ${name} restarted`);
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    vscode.window.showErrorMessage(`Rat: ${msg}`);
  }
}
