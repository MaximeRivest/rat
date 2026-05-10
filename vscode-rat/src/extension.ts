/**
 * extension.ts — Entry point.
 *
 * Wires cells, queue, CodeLens, completions, hover, and commands
 * into VS Code.  Supports two modes:
 *   - "notebook" — markdown/quarto with fenced code cells
 *   - "source"   — .py/.r/.jl/.js/.sh files sent to REPL
 */

import * as vscode from "vscode";

import {
  parseCells,
  cellAtLine,
  cellsUpTo,
  ratLangForFence,
} from "./cells";
import { RatCodeLensProvider } from "./codeLens";
import { RatInspectorViewProvider } from "./inspectorView";
import {
  RatCompletionProvider,
  RatHoverProvider,
  RatSignatureHelpProvider,
  registerFallbackDocumentProvider,
} from "./intellisense";
import {
  RatDefinitionProvider,
  RatDocumentHighlightProvider,
  RatReferenceProvider,
} from "./navigation";
import { clearAllOutputs } from "./output";
import { debugPythonCellsAtLine } from "./pythonDebug";
import { ExecutionController, type QueueState } from "./queue";
import {
  initRuntimeState,
  resolveRuntime,
  scopeLabel,
  setScopeOverride,
} from "./resolve";
import { disposeAll, getClient, ratStop, ratRestart, readState } from "./rat";
import {
  RuntimeTreeProvider,
  startRuntimeCmd,
  stopRuntimeCmd,
  restartRuntimeCmd,
  removeRuntimeCmd,
  interruptRuntimeCmd,
  clearRuntimeQueueCmd,
  resumeRuntimeQueueCmd,
  interruptQueueItemCmd,
  forceClearQueueItemCmd,
  removeQueuedItemCmd,
} from "./runtimeView";
import { showRuntimePicker } from "./runtimePicker";
import { getRuntimeOverride } from "./resolve";
import { applyDecorations, disposeDecorations } from "./decorations";
import { RatVariablesViewProvider } from "./variablesView";
import { initRatInstaller, installRatCliCommand, ratTerminalCommand } from "./installRat";
import { RatSnippetProvider, CELL_SNIPPETS } from "./snippets";
import { detectFileLang, isRatFile } from "./langDetect";
import { blockAtCursor, nextBlock, allBlocks } from "./blocks";
import {
  clearResults,
  renderResults,
  disposeInlineOutput,
  rebaseResultsForDocumentChange,
} from "./inlineOutput";
import {
  configureRatMarkdownAppearance,
  openRenderedMarkdownEditor,
  restoreDefaultMarkdownEditor,
  RatMrmdEditorProvider,
  setMarkdownPreviewShortcutReplacement,
  syncRatMarkdownAppearance,
  syncRatMarkdownThemes,
  useRatMarkdownAsDefaultEditor,
} from "./mrmdEditor";
import { PiSessionMrmdEditor } from "./piSessionEditor";

// ── Globals ────────────────────────────────────────────────

let statusBar: vscode.StatusBarItem;
let runtimeBar: vscode.StatusBarItem;
let queue: ExecutionController;
let treeProvider: RuntimeTreeProvider;
let inspectorProvider: RatInspectorViewProvider;
let variablesProvider: RatVariablesViewProvider;
let extensionContext: vscode.ExtensionContext;
let lastVariablesTargetKey = "";

// ── Activate ───────────────────────────────────────────────

export function activate(ctx: vscode.ExtensionContext): void {
  extensionContext = ctx;
  initRuntimeState(ctx);
  initRatInstaller(ctx);
  registerFallbackDocumentProvider(ctx);

  ctx.subscriptions.push(
    vscode.window.onDidChangeActiveColorTheme(() => syncRatMarkdownThemes()),
    vscode.workspace.onDidChangeConfiguration((event) => {
      if (
        event.affectsConfiguration("rat.markdownPageMode") ||
        event.affectsConfiguration("rat.markdownThemeAssociations") ||
        event.affectsConfiguration("rat.markdownPageCanvasBackground") ||
        event.affectsConfiguration("rat.markdownFontScale")
      ) {
        syncRatMarkdownAppearance();
      }
    }),
  );

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

  // Execution queue
  queue = new ExecutionController((state, pending) => {
    setStatus(state, pending);
    vscode.commands.executeCommand(
      "setContext",
      "rat.executing",
      state === "running",
    );
    treeProvider?.refresh();
  });

  // Tree view
  treeProvider = new RuntimeTreeProvider(queue);
  const treeView = vscode.window.createTreeView("ratRuntimes", {
    treeDataProvider: treeProvider,
  });
  treeProvider.startAutoRefresh(4000);
  ctx.subscriptions.push(treeView);

  inspectorProvider = new RatInspectorViewProvider();
  variablesProvider = new RatVariablesViewProvider();
  const piSessionProvider = new PiSessionMrmdEditor(ctx, queue);
  ctx.subscriptions.push(
    inspectorProvider,
    variablesProvider,
    piSessionProvider,
    vscode.window.registerWebviewViewProvider("ratInspector", inspectorProvider),
    vscode.window.registerWebviewViewProvider("ratVariables", variablesProvider),
    vscode.window.registerWebviewViewProvider(PiSessionMrmdEditor.viewType, piSessionProvider, { webviewOptions: { retainContextWhenHidden: true } }),
  );

  ctx.subscriptions.push(RatMrmdEditorProvider.register(ctx, queue, {
    onActiveDocument: (document) => {
      vscode.commands.executeCommand("setContext", "rat.activeFile", isRatFile(document));
      if (runtimeBar) updateRuntimeBarForDocument(document);
      updateVariablesForDocument(document, ratLangForContext(document), true);
    },
    onSelection: (document, language, expression) => {
      vscode.commands.executeCommand("setContext", "rat.activeFile", isRatFile(document));
      const ratLang = language ? ratLangForFence(language) : null;
      if (runtimeBar) updateRuntimeBarForDocument(document, language ?? undefined);
      updateVariablesForDocument(document, ratLang);
      inspectorProvider?.updateRatMarkdownSelection(document, language, expression);
    },
  }));

  // ── Providers — notebook files (markdown/quarto) ─────────

  const notebookSel: vscode.DocumentSelector = [
    { language: "markdown" },
    { language: "quarto" },
    { pattern: "**/*.qmd" },
    { pattern: "**/*.rmd" },
    { pattern: "**/*.Rmd" },
  ];

  const codeLens = new RatCodeLensProvider();

  const notebookNavigation = new RatDefinitionProvider();

  ctx.subscriptions.push(
    vscode.languages.registerCodeLensProvider(notebookSel, codeLens),
    vscode.languages.registerCompletionItemProvider(
      notebookSel,
      new RatCompletionProvider(),
      ".",
      "(",
      "=",
      "[",
    ),
    vscode.languages.registerHoverProvider(notebookSel, new RatHoverProvider()),
    vscode.languages.registerSignatureHelpProvider(
      notebookSel,
      new RatSignatureHelpProvider(),
      "(",
      ",",
    ),
    vscode.languages.registerDefinitionProvider(notebookSel, notebookNavigation),
    vscode.languages.registerDeclarationProvider(notebookSel, notebookNavigation),
    vscode.languages.registerReferenceProvider(notebookSel, new RatReferenceProvider()),
    vscode.languages.registerDocumentHighlightProvider(
      notebookSel,
      new RatDocumentHighlightProvider(),
    ),
    vscode.languages.registerCompletionItemProvider(
      notebookSel,
      new RatSnippetProvider(),
    ),
  );

  // ── Providers — source files (.py, .r, .jl, .js, .sh) ───

  const sourceSel: vscode.DocumentSelector = [
    { language: "python" },
    { language: "r" },
    { language: "julia" },
    { language: "javascript" },
    { language: "shellscript" },
  ];

  ctx.subscriptions.push(
    vscode.languages.registerCompletionItemProvider(
      sourceSel,
      new RatCompletionProvider(),
      ".",
      "(",
      "=",
      "[",
    ),
    vscode.languages.registerHoverProvider(sourceSel, new RatHoverProvider()),
    vscode.languages.registerSignatureHelpProvider(
      sourceSel,
      new RatSignatureHelpProvider(),
      "(",
      ",",
    ),
  );

  // ── Auto-trigger snippets and member completions ───────────
  const snippetPrefixes = new Set(CELL_SNIPPETS.map((s) => s.prefix));
  ctx.subscriptions.push(
    vscode.workspace.onDidChangeTextDocument((e) => {
      const ed = vscode.window.activeTextEditor;
      if (!ed || ed.document !== e.document || !isRatFile(e.document)) return;
      const fl = detectFileLang(e.document);

      for (const ch of e.contentChanges) {
        if (ch.text.length !== 1) continue;
        const position = new vscode.Position(
          ch.range.start.line,
          ch.range.start.character + ch.text.length,
        );

        if (fl.mode === "notebook") {
          const line = e.document.lineAt(ch.range.start.line);
          const typed = line.text.trim();
          if (typed.length > 0 && snippetPrefixes.has(typed)) {
            vscode.commands.executeCommand("editor.action.triggerSuggest");
            return;
          }
        }

        if (shouldTriggerMemberSuggest(e.document, position, ch.text)) {
          vscode.commands.executeCommand("editor.action.triggerSuggest");
          return;
        }
      }
    }),
  );

  // Context key + runtime bar update + decorations on editor switch
  const updateCtx = () => {
    const ed = vscode.window.activeTextEditor;
    const active = !!ed && isRatFile(ed.document);
    vscode.commands.executeCommand("setContext", "rat.activeFile", active);

    updateRuntimeBar(ed);
    inspectorProvider.update(ed);
    updateVariablesForEditor(ed, true);
    if (ed && isRatFile(ed.document)) {
      const fl = detectFileLang(ed.document);
      if (fl.mode === "notebook") {
        applyDecorations(ed);
      } else {
        renderResults(ed);
      }
    }
  };
  ctx.subscriptions.push(
    vscode.window.onDidChangeActiveTextEditor(updateCtx),
    vscode.window.onDidChangeTextEditorSelection((e) => {
      inspectorProvider.update(e.textEditor);
      updateRuntimeBar(e.textEditor);
      updateVariablesForEditor(e.textEditor);
    }),
    vscode.workspace.onDidChangeTextDocument((e) => {
      rebaseResultsForDocumentChange(e);
      updateRuntimeBar(vscode.window.activeTextEditor);
      updateVariablesForEditor(vscode.window.activeTextEditor);
      const ed = vscode.window.activeTextEditor;
      if (ed && ed.document === e.document && isRatFile(ed.document)) {
        const fl = detectFileLang(ed.document);
        if (fl.mode === "notebook") {
          applyDecorations(ed);
        } else if (fl.mode === "source") {
          renderResults(ed);
        }
      }
    }),
  );
  updateCtx();

  // ── Commands ─────────────────────────────────────────────

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const reg = (id: string, fn: (...a: any[]) => unknown) =>
    ctx.subscriptions.push(vscode.commands.registerCommand(id, fn));

  reg("rat.runCell", () => runCmd(false));
  reg("rat.runCellAndAdvance", () => runCmd(true));
  reg("rat.runAbove", runAboveCmd);
  reg("rat.runAll", runAllCmd);
  reg("rat.runCellAt", (line: number) => runCellAtCmd(line));
  reg("rat.runAboveAt", (line: number) => runAboveAtCmd(line));
  reg("rat.debugPythonCell", () => debugPythonCellCmd());
  reg("rat.debugPythonAbove", () => debugPythonAboveCmd());
  reg("rat.debugPythonCellAt", (line: number) => debugPythonCellAtCmd(line));
  reg("rat.debugPythonAboveAt", (line: number) => debugPythonAboveAtCmd(line));

  reg("rat.cancelExecution", () => queue.cancelAll());
  reg("rat.clearQueue", () => queue.clear());
  reg("rat.togglePause", () => queue.togglePause());

  reg("rat.insertCell", insertCellCmd);
  reg("rat.clearOutputs", clearOutputsCmd);
  reg("rat.installCli", installRatCliCommand);
  reg("rat.openRenderedMarkdown", openRenderedMarkdownEditor);
  reg("rat.openMarkdownPreview", openRenderedMarkdownEditor);
  reg("rat.enableMarkdownPreviewReplacement", () => setMarkdownPreviewShortcutReplacement(true));
  reg("rat.disableMarkdownPreviewReplacement", () => setMarkdownPreviewShortcutReplacement(false));
  reg("rat.configureMarkdownAppearance", configureRatMarkdownAppearance);
  reg("rat.useMrmdAsDefaultMarkdownEditor", useRatMarkdownAsDefaultEditor);
  reg("rat.restoreDefaultMarkdownEditor", restoreDefaultMarkdownEditor);
  reg("rat.openPiSessionMrmd", (uri?: vscode.Uri) => piSessionProvider.show(uri));
  reg("rat.showVariables", showVariablesCmd);
  reg("rat.stopKernel", stopKernelCmd);
  reg("rat.restartKernel", restartKernelCmd);

  // Runtime management
  reg("rat.selectRuntime", async () => {
    await showRuntimePicker();
    updateRuntimeBar(vscode.window.activeTextEditor);
    treeProvider.refresh();
  });
  reg("rat.openRepl", openReplCmd);
  reg("rat.useNotebookScope", () => setCurrentScope("notebook"));
  reg("rat.useProjectScope", () => setCurrentScope("project"));
  reg("rat.useGlobalScope", () => setCurrentScope("global"));
  reg("rat.refreshRuntimes", () => treeProvider.refresh());
  reg("rat.startRuntime", (n: any) => startRuntimeCmd(n).then(() => treeProvider.refresh()));
  reg("rat.stopRuntime", (n: any) => {
    if (typeof n?.runtimeName === "string") queue.clearRuntime(n.runtimeName);
    return stopRuntimeCmd(n).then(() => { disposeAll(); treeProvider.refresh(); });
  });
  reg("rat.restartRuntime", (n: any) => {
    if (typeof n?.runtimeName === "string") queue.clearRuntime(n.runtimeName);
    return restartRuntimeCmd(n).then(() => { disposeAll(); treeProvider.refresh(); });
  });
  reg("rat.removeRuntime", (n: any) => {
    if (typeof n?.runtimeName === "string") queue.clearRuntime(n.runtimeName);
    return removeRuntimeCmd(n).then(() => { disposeAll(); treeProvider.refresh(); });
  });
  reg("rat.interruptRuntime", (n: any) => { interruptRuntimeCmd(queue, n); treeProvider.refresh(); });
  reg("rat.clearRuntimeQueue", (n: any) => { clearRuntimeQueueCmd(queue, n); treeProvider.refresh(); });
  reg("rat.resumeRuntimeQueue", (n: any) => { resumeRuntimeQueueCmd(queue, n); treeProvider.refresh(); });
  reg("rat.interruptQueueItem", (n: any) => { interruptQueueItemCmd(queue, n); treeProvider.refresh(); });
  reg("rat.forceClearQueueItem", (n: any) => { forceClearQueueItemCmd(queue, n); treeProvider.refresh(); });
  reg("rat.removeQueuedItem", (n: any) => { removeQueuedItemCmd(queue, n); treeProvider.refresh(); });
  reg("rat.addRuntime", async () => { await showRuntimePicker(); treeProvider.refresh(); });
}

// ── Deactivate ─────────────────────────────────────────────

export function deactivate(): void {
  treeProvider?.stopAutoRefresh();
  disposeDecorations();
  disposeInlineOutput();
  disposeAll();
}

// ── Helpers ────────────────────────────────────────────────

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

function shouldTriggerMemberSuggest(
  document: vscode.TextDocument,
  position: vscode.Position,
  typed: string,
): boolean {
  if (typed !== "." && !/^[A-Za-z0-9_]$/.test(typed)) return false;

  const fl = detectFileLang(document);
  if (fl.mode === "notebook") {
    const cell = cellAtLine(parseCells(document), position.line);
    if (!cell?.executable) return false;
    if (position.line <= cell.openLine || position.line >= cell.closeLine) return false;
  }

  const line = document.lineAt(position.line).text;
  const beforeCursor = line.slice(0, Math.min(position.character, line.length));
  return /(?:^|[^A-Za-z0-9_])[$A-Za-z_][\w$]*(?:\.[$A-Za-z_][\w$]*)*\.?[$A-Za-z_][\w$]*$/.test(beforeCursor) &&
    beforeCursor.includes(".");
}

function updateRuntimeBar(editor: vscode.TextEditor | undefined): void {
  if (!editor) {
    runtimeBar.hide();
    return;
  }
  updateRuntimeBarForDocument(editor.document, undefined, editor.selection.active.line);
}

function updateRuntimeBarForDocument(
  document: vscode.TextDocument,
  preferredFenceLang?: string,
  preferredLine?: number,
): void {
  if (!isRatFile(document)) {
    runtimeBar.hide();
    return;
  }

  const lang = ratLangForContext(document, preferredLine, preferredFenceLang);
  if (!lang) {
    runtimeBar.hide();
    return;
  }

  const docUri = document.uri.toString();
  const override = getRuntimeOverride(docUri, lang);
  const { name, scope } = resolveRuntime(lang, document);

  const state = readState();
  const kernel = state.kernels.find((k) => k.name === name);
  const icon = kernel ? "$(circle-filled)" : "$(circle-outline)";

  runtimeBar.text = `${icon} ${name} · ${scopeLabel(scope)}`;
  runtimeBar.tooltip = kernel
    ? `Runtime: ${name} (${scopeLabel(scope)} scope, running on :${kernel.port})\nClick to switch or open a REPL`
    : `Runtime: ${name} (${scopeLabel(scope)} scope, not running)\nClick to switch or open a REPL`;
  if (override) {
    runtimeBar.text += " (override)";
  }
  runtimeBar.show();
}

function ratLangForContext(
  document: vscode.TextDocument,
  line?: number,
  preferredFenceLang?: string,
): string | null {
  const preferredRatLang = preferredFenceLang ? ratLangForFence(preferredFenceLang) : null;
  if (preferredRatLang) return preferredRatLang;

  const fl = detectFileLang(document);
  if (fl.mode === "source") return fl.ratLang;
  if (fl.mode !== "notebook") return null;

  const cells = parseCells(document);
  if (cells.length === 0) return null;
  if (line === undefined) return cells[0].ratLang;

  const containing = cellAtLine(cells, line);
  if (containing) return containing.ratLang;

  const preceding = [...cells].reverse().find((cell) => cell.openLine <= line);
  return (preceding ?? cells[0]).ratLang;
}

function currentRatLang(editor: vscode.TextEditor): string | null {
  return ratLangForContext(editor.document, editor.selection.active.line);
}

function updateVariablesForEditor(
  editor: vscode.TextEditor | undefined,
  force = false,
): void {
  if (!editor) {
    updateVariablesForDocument(undefined, null, force);
    return;
  }
  updateVariablesForDocument(
    editor.document,
    ratLangForContext(editor.document, editor.selection.active.line),
    force,
  );
}

function updateVariablesForDocument(
  document: vscode.TextDocument | undefined,
  ratLang: string | null | undefined,
  force = false,
): void {
  const key = document && ratLang ? `${document.uri.toString()}::${ratLang}` : "";
  if (!force && key === lastVariablesTargetKey) return;
  lastVariablesTargetKey = key;
  variablesProvider?.updateDocument(document, ratLang);
}

// ── Notebook-mode helpers ──────────────────────────────────

function enqueueNotebook(
  editor: vscode.TextEditor,
  cell: ReturnType<typeof parseCells>[number],
): void {
  const { name, cwd, lang } = resolveRuntime(cell.ratLang, editor.document);
  queue.enqueue({ kind: "notebook", cell, editor, runtimeName: name, cwd, lang });
}

// ── Source-mode helpers ────────────────────────────────────

function enqueueSource(
  editor: vscode.TextEditor,
  block: import("./blocks").SourceBlock,
  ratLang: string,
): void {
  const { name, cwd, lang } = resolveRuntime(ratLang, editor.document);
  queue.enqueue({
    kind: "source",
    block,
    code: block.code,
    editor,
    runtimeName: name,
    cwd,
    lang,
  });
}

/** Strip common leading whitespace (like Python's textwrap.dedent). */
function dedent(text: string): string {
  const lines = text.split("\n");
  // Find minimum indentation of non-empty lines
  let minIndent = Infinity;
  for (const line of lines) {
    if (line.trim().length === 0) continue;
    const indent = line.match(/^(\s*)/)![1].length;
    if (indent < minIndent) minIndent = indent;
  }
  if (minIndent === 0 || minIndent === Infinity) return text;
  return lines.map((l) => l.slice(minIndent)).join("\n");
}

/**
 * Block-opening pattern: lines that expect an indented body after them.
 * Matches `for ...:`, `if ...:`, `while ...:`, `def ...:`, `class ...:`,
 * `with ...:`, `try:`, `except ...:`, `elif ...:`, `else:`, `finally:`.
 */
const BLOCK_OPENER_RE = /^\s*(?:for|if|while|def|class|with|try|except|elif|else|finally|async)\b.*:\s*(?:#.*)?$/;
const DECORATOR_RE = /^\s*@/;

/**
 * Track bracket nesting, triple-quoted strings, and backslash continuations
 * across a single logical line.  Used to decide whether the *next* line is
 * still part of the current statement.
 */
interface LexState {
  depth: number;           // () [] {} nesting depth
  triple: string | null;   // active triple-quote delimiter, or null
  continued: boolean;      // previous line ended with a backslash
}

function updateLexState(state: LexState, line: string): LexState {
  let { depth, triple, continued } = state;
  continued = false;
  let i = 0;
  while (i < line.length) {
    const c = line[i];

    if (triple) {
      if (line.startsWith(triple, i)) {
        triple = null;
        i += 3;
        continue;
      }
      if (c === "\\" && i + 1 < line.length) {
        i += 2;
        continue;
      }
      i++;
      continue;
    }

    // Comment — rest of the line is irrelevant
    if (c === "#") break;

    // Triple-quoted string start
    if ((c === '"' || c === "'") && line.startsWith(c.repeat(3), i)) {
      triple = c.repeat(3);
      i += 3;
      continue;
    }

    // Regular string — scan to its closing quote
    if (c === '"' || c === "'") {
      const quote = c;
      i++;
      while (i < line.length) {
        const cc = line[i];
        if (cc === "\\" && i + 1 < line.length) { i += 2; continue; }
        if (cc === quote) { i++; break; }
        i++;
      }
      continue;
    }

    if (c === "(" || c === "[" || c === "{") depth++;
    else if (c === ")" || c === "]" || c === "}") depth = Math.max(0, depth - 1);

    i++;
  }

  // Line-continuation backslash (outside a string)
  if (!triple && /\\\s*$/.test(line)) continued = true;

  return { depth, triple, continued };
}

function stmtIsOpen(state: LexState): boolean {
  return state.depth > 0 || state.triple !== null || state.continued;
}

/**
 * Split dedented code into executable chunks.
 *
 * A statement boundary only happens when we're back to depth 0 / no open
 * string / no backslash continuation AND the next non-empty line starts
 * at column 0.  Indented lines belonging to a block opener stay grouped
 * with the opener; orphan indented lines get dedented.
 */
function splitTopLevel(code: string): string[] {
  const lines = code.split("\n");
  const chunks: string[] = [];
  let current: string[] = [];
  let inBlock = false;
  let lex: LexState = { depth: 0, triple: null, continued: false };

  const flush = () => {
    const text = current.join("\n").trimEnd();
    if (text.trim().length > 0) chunks.push(text);
    current = [];
    inBlock = false;
  };

  for (const line of lines) {
    // Still inside an open statement (unbalanced brackets, triple-string,
    // or backslash continuation) → always append to the current chunk
    if (stmtIsOpen(lex)) {
      current.push(line);
      lex = updateLexState(lex, line);
      continue;
    }

    if (line.trim().length === 0) {
      current.push(line);
      continue;
    }

    const indent = line.match(/^(\s*)/)![1].length;

    if (indent === 0) {
      // New top-level line
      if (current.some((l) => l.trim().length > 0)) flush();
      current.push(line);
      inBlock = BLOCK_OPENER_RE.test(line) || DECORATOR_RE.test(line);
      lex = updateLexState({ depth: 0, triple: null, continued: false }, line);
    } else if (inBlock) {
      // Indented line belonging to a block opener — keep grouped
      current.push(line);
      lex = updateLexState(lex, line);
    } else {
      // Orphan indented line (no block opener above) — dedent and
      // emit as its own chunk
      if (current.some((l) => l.trim().length > 0)) flush();
      const dedented = line.trimStart();
      current.push(dedented);
      lex = updateLexState({ depth: 0, triple: null, continued: false }, dedented);
    }
  }
  flush();
  return chunks;
}

function enqueueSelection(
  editor: vscode.TextEditor,
  ratLang: string,
): void {
  const sel = editor.selection;
  const raw = editor.document.getText(sel);
  if (!raw.trim()) return;
  const code = dedent(raw);
  const { name, cwd, lang } = resolveRuntime(ratLang, editor.document);
  const range = new vscode.Range(sel.start, sel.end);

  // Split into independent top-level statements so e.g.
  // selecting `i = 234` + `    print(i)` inside a loop works.
  const chunks = splitTopLevel(code);
  for (const chunk of chunks) {
    queue.enqueue({
      kind: "source",
      block: { code: chunk, range },
      code: chunk,
      editor,
      runtimeName: name,
      cwd,
      lang,
    });
  }
}

// ── Unified run command ────────────────────────────────────

async function runCmd(advance: boolean): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor || !isRatFile(editor.document)) return;

  const fl = detectFileLang(editor.document);

  // ── Selection run (works in both modes) ─────────────────
  if (!editor.selection.isEmpty) {
    if (fl.mode === "notebook") {
      // Determine language from the cell the selection is in
      const cells = parseCells(editor.document);
      const cell = cellAtLine(cells, editor.selection.active.line);
      if (cell?.executable) {
        enqueueSelection(editor, cell.ratLang);
        return;
      }
    } else {
      enqueueSelection(editor, fl.ratLang!);
      return;
    }
  }

  if (fl.mode === "notebook") {
    // Notebook mode — run fenced code cell
    const cells = parseCells(editor.document);
    const cell = cellAtLine(cells, editor.selection.active.line);
    if (!cell?.executable) {
      vscode.window.showInformationMessage("No executable code cell at cursor");
      return;
    }
    enqueueNotebook(editor, cell);

    if (advance) {
      const idx = cells.indexOf(cell);
      if (idx < cells.length - 1) {
        const next = cells[idx + 1];
        const pos = new vscode.Position(next.openLine + 1, 0);
        editor.selection = new vscode.Selection(pos, pos);
        editor.revealRange(new vscode.Range(pos, pos));
      }
    }
    return;
  }

  // Source mode
  const ratLang = fl.ratLang!;

  // Find the logical block at cursor
  const line = editor.selection.active.line;
  const block = await blockAtCursor(editor.document, line, ratLang);
  if (!block) {
    vscode.window.showInformationMessage("No executable block at cursor");
    return;
  }

  enqueueSource(editor, block, ratLang);

  if (advance) {
    // Move cursor to the next block
    const nb = await nextBlock(editor.document, block.range.end.line, ratLang);
    if (nb) {
      const pos = new vscode.Position(nb.range.start.line, 0);
      editor.selection = new vscode.Selection(pos, pos);
      editor.revealRange(new vscode.Range(pos, pos));
    } else {
      // No next block — move past end of current block
      const endLine = Math.min(block.range.end.line + 1, editor.document.lineCount - 1);
      const pos = new vscode.Position(endLine, 0);
      editor.selection = new vscode.Selection(pos, pos);
    }
  }
}

function runCellAtCmd(line: number): void {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;
  const fl = detectFileLang(editor.document);
  if (fl.mode === "notebook") {
    const cell = parseCells(editor.document).find((c) => c.openLine === line);
    if (cell?.executable) enqueueNotebook(editor, cell);
  }
  // CodeLens not shown for source files currently
}

function runAboveCmd(): void {
  const editor = vscode.window.activeTextEditor;
  if (!editor || !isRatFile(editor.document)) return;

  const fl = detectFileLang(editor.document);
  if (fl.mode === "notebook") {
    for (const c of cellsUpTo(
      parseCells(editor.document),
      editor.selection.active.line,
    )) {
      if (c.executable) enqueueNotebook(editor, c);
    }
    return;
  }

  // Source mode — run all blocks up to and including cursor
  const ratLang = fl.ratLang!;
  const cursorLine = editor.selection.active.line;
  allBlocks(editor.document, ratLang).then((blocks) => {
    for (const b of blocks) {
      if (b.range.start.line <= cursorLine) {
        enqueueSource(editor, b, ratLang);
      }
    }
  });
}

function runAboveAtCmd(line: number): void {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;
  const fl = detectFileLang(editor.document);
  if (fl.mode === "notebook") {
    for (const c of cellsUpTo(parseCells(editor.document), line)) {
      if (c.executable) enqueueNotebook(editor, c);
    }
  }
}

async function debugPythonCellCmd(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor || !isRatFile(editor.document)) return;
  await debugPythonCellAtCmd(editor.selection.active.line);
}

async function debugPythonAboveCmd(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor || !isRatFile(editor.document)) return;
  await debugPythonAboveAtCmd(editor.selection.active.line);
}

async function debugPythonCellAtCmd(line: number): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;
  const fl = detectFileLang(editor.document);
  if (fl.mode !== "notebook") return;
  await debugPythonCellsAtLine(extensionContext, editor, line, "cell");
}

async function debugPythonAboveAtCmd(line: number): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;
  const fl = detectFileLang(editor.document);
  if (fl.mode !== "notebook") return;
  await debugPythonCellsAtLine(extensionContext, editor, line, "above");
}

async function runAllCmd(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor || !isRatFile(editor.document)) return;

  const fl = detectFileLang(editor.document);
  if (fl.mode === "notebook") {
    for (const c of parseCells(editor.document)) {
      if (c.executable) enqueueNotebook(editor, c);
    }
    return;
  }

  const ratLang = fl.ratLang!;
  const blocks = await allBlocks(editor.document, ratLang);
  for (const b of blocks) {
    enqueueSource(editor, b, ratLang);
  }
}

// ── Reverse map: ratLang → canonical fence name ───────────

async function insertCellCmd(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;
  const fl = detectFileLang(editor.document);
  if (fl.mode !== "notebook") return; // Only for notebook files

  const cells = parseCells(editor.document);
  const fenceLang = dominantFenceLang(cells);

  const cursorLine = editor.selection.active.line;
  const lineText = editor.document.lineAt(cursorLine).text;
  const insertLine = cursorLine;
  const insertAt = new vscode.Position(insertLine, lineText.length);

  const openFence = "```" + fenceLang;
  const prefix = lineText.length > 0 || cursorLine > 0 ? "\n\n" : "";
  const snippet = prefix + openFence + "\n" + "\n" + "```";

  await editor.edit((eb) => {
    eb.insert(insertAt, snippet);
  });

  const openFenceLine = insertLine + (prefix.length > 0 ? 2 : 0);
  const bodyLine = openFenceLine + 1;
  const pos = new vscode.Position(bodyLine, 0);
  editor.selection = new vscode.Selection(pos, pos);
  editor.revealRange(new vscode.Range(pos, pos));
}

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
  if (!editor) return;
  const fl = detectFileLang(editor.document);
  if (fl.mode === "notebook") {
    await clearAllOutputs(editor);
  } else if (fl.mode === "source") {
    clearResults(editor);
  }
}

async function showVariablesCmd(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;

  const lang = currentRatLang(editor);
  if (!lang) return;

  const { name, cwd } = resolveRuntime(lang, editor.document);
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

  const lang = currentRatLang(editor);
  if (!lang) return;

  const { name } = resolveRuntime(lang, editor.document);
  try {
    queue.clearRuntime(name);
    await ratStop(name);
    disposeAll();
    treeProvider.refresh();
    vscode.window.showInformationMessage(`Rat: ${name} stopped`);
  } catch {
    /* swallow */
  }
}

async function openReplCmd(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor || !isRatFile(editor.document)) return;

  const lang = currentRatLang(editor);
  if (!lang) return;

  const { name, cwd } = resolveRuntime(lang, editor.document);
  try {
    await getClient(name, cwd, lang);
    const terminal = vscode.window.createTerminal({
      name: `rat ${name}`,
      cwd,
    });
    terminal.show(true);
    terminal.sendText(ratTerminalCommand([name]));
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    vscode.window.showErrorMessage(`Rat: ${msg}`);
  }
}

async function setCurrentScope(scope: "notebook" | "project" | "global"): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor || !isRatFile(editor.document)) return;

  const lang = currentRatLang(editor);
  if (!lang) return;

  setScopeOverride(editor.document.uri.toString(), lang, scope);
  updateRuntimeBar(editor);
  treeProvider.refresh();
  const info = resolveRuntime(lang, editor.document);
  vscode.window.showInformationMessage(`Rat: using ${scopeLabel(scope)} scope (${info.name})`);
}

async function restartKernelCmd(): Promise<void> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;

  const lang = currentRatLang(editor);
  if (!lang) return;

  const { name, cwd } = resolveRuntime(lang, editor.document);
  try {
    queue.clearRuntime(name);
    disposeAll();
    await ratRestart(name, cwd);
    treeProvider.refresh();
    vscode.window.showInformationMessage(`Rat: ${name} restarted`);
  } catch (err: unknown) {
    const msg = err instanceof Error ? err.message : String(err);
    vscode.window.showErrorMessage(`Rat: ${msg}`);
  }
}
