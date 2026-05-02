/**
 * inlineOutput.ts — Inline decorations for source-file execution results.
 *
 * Short results (≤1 line) appear as grayed-out text after the last line.
 * Longer results show a truncated summary inline + full text on hover.
 * Errors show the final error line in red inline + full traceback on hover.
 *
 * A separate "Rat Output" output channel always receives the full result
 * for scrollback.
 */

import * as vscode from "vscode";

// ── Output channel (full scrollback) ───────────────────────

let outputChannel: vscode.OutputChannel | null = null;

function getOutputChannel(): vscode.OutputChannel {
  if (!outputChannel) {
    outputChannel = vscode.window.createOutputChannel("Rat Output");
  }
  return outputChannel;
}

export function appendToOutputChannel(
  label: string,
  text: string,
): void {
  const ch = getOutputChannel();
  ch.appendLine(`── ${label} ${"─".repeat(Math.max(0, 60 - label.length))}`);
  ch.appendLine(text);
  ch.appendLine("");
}

// ── Decoration types ───────────────────────────────────────

const resultDecoration = vscode.window.createTextEditorDecorationType({
  after: {
    color: new vscode.ThemeColor("editorCodeLens.foreground"),
    fontStyle: "italic",
    margin: "0 0 0 2em",
  },
  isWholeLine: true,
});

const errorDecoration = vscode.window.createTextEditorDecorationType({
  after: {
    color: new vscode.ThemeColor("errorForeground"),
    fontStyle: "italic",
    margin: "0 0 0 2em",
  },
  isWholeLine: true,
});

const runningDecoration = vscode.window.createTextEditorDecorationType({
  after: {
    color: new vscode.ThemeColor("editorCodeLens.foreground"),
    fontStyle: "italic",
    margin: "0 0 0 2em",
  },
  isWholeLine: true,
});

// ── Diagnostics collection for errors ──────────────────────

const diagnostics = vscode.languages.createDiagnosticCollection("rat");

// ── State: per-editor decorations ──────────────────────────

interface InlineResult {
  line: number;
  text: string;
  isError: boolean;
}

interface LineChange {
  startLine: number;
  startCharacter: number;
  endLine: number;
  endCharacter: number;
  text: string;
}

// Map<documentUri, InlineResult[]>
const results = new Map<string, InlineResult[]>();

const MAX_INLINE_LENGTH = 80;

// ── Public API ─────────────────────────────────────────────

/**
 * Show a "running…" indicator on the last line of a block.
 * Optionally include a short snippet of the most recent stdout so
 * long-running code feels alive.
 */
export function showRunning(
  editor: vscode.TextEditor,
  lastLine: number,
  latest?: string,
  waitingForInput = false,
): void {
  const range = new vscode.Range(lastLine, 0, lastLine, 0);
  let label: string;
  if (waitingForInput) {
    label = " ✋ waiting for input…";
  } else if (latest && latest.trim()) {
    // Show only the last non-empty line, truncated.
    const lines = latest.split("\n").filter((l) => l.trim().length > 0);
    const tail = lines.length > 0 ? lines[lines.length - 1] : "";
    const trimmed = tail.length > 80 ? tail.slice(0, 79) + "…" : tail;
    label = trimmed ? ` ⏳ ${trimmed}` : " ⏳ running…";
  } else {
    label = " ⏳ running…";
  }
  editor.setDecorations(runningDecoration, [
    {
      range,
      renderOptions: {
        after: { contentText: label },
      },
    },
  ]);
}

/**
 * Clear the "running…" indicator.
 */
export function clearRunning(editor: vscode.TextEditor): void {
  editor.setDecorations(runningDecoration, []);
}

/**
 * Show an inline result after execution completes.
 */
export function showResult(
  editor: vscode.TextEditor,
  lastLine: number,
  text: string,
  isError: boolean,
): void {
  const uri = editor.document.uri.toString();

  // Store result
  let docResults = results.get(uri);
  if (!docResults) {
    docResults = [];
    results.set(uri, docResults);
  }
  // Remove any existing result on this line
  const idx = docResults.findIndex((r) => r.line === lastLine);
  if (idx >= 0) docResults.splice(idx, 1);
  docResults.push({ line: lastLine, text, isError });

  // Update diagnostics for errors
  updateDiagnostics(editor.document);

  // Re-render all results for this document
  renderResults(editor);
}

/**
 * Clear all inline results for a document.
 */
export function clearResults(editor: vscode.TextEditor): void {
  const uri = editor.document.uri.toString();
  results.delete(uri);
  editor.setDecorations(resultDecoration, []);
  editor.setDecorations(errorDecoration, []);
  diagnostics.delete(editor.document.uri);
}

/**
 * Keep stored inline result locations aligned after source edits.
 *
 * Decorations are anchored to line numbers, not semantic blocks. When users
 * insert/delete text above a result we can rebase it; when the edit touches the
 * result line itself we drop that result to avoid stale diagnostics.
 */
export function rebaseResultsForDocumentChange(
  event: vscode.TextDocumentChangeEvent,
): void {
  const uri = event.document.uri.toString();
  const docResults = results.get(uri);
  if (!docResults || docResults.length === 0 || event.contentChanges.length === 0) {
    return;
  }

  const changes = event.contentChanges.map((change) => ({
    startLine: change.range.start.line,
    startCharacter: change.range.start.character,
    endLine: change.range.end.line,
    endCharacter: change.range.end.character,
    text: change.text,
  }));

  const next = rebaseInlineResults(docResults, changes);
  if (next.length === 0) {
    results.delete(uri);
    diagnostics.delete(event.document.uri);
    return;
  }

  results.set(uri, next);
  updateDiagnostics(event.document);
}

export function rebaseInlineResultsForTest(
  input: InlineResult[],
  changes: LineChange[],
): InlineResult[] {
  return rebaseInlineResults(input, changes);
}

/**
 * Re-render all stored results (call after editor switch).
 */
export function renderResults(editor: vscode.TextEditor): void {
  const uri = editor.document.uri.toString();
  const docResults = results.get(uri);
  if (!docResults || docResults.length === 0) {
    editor.setDecorations(resultDecoration, []);
    editor.setDecorations(errorDecoration, []);
    return;
  }

  const okDecos: vscode.DecorationOptions[] = [];
  const errDecos: vscode.DecorationOptions[] = [];

  for (const r of docResults) {
    if (r.line >= editor.document.lineCount) continue;
    const range = new vscode.Range(r.line, 0, r.line, 0);

    if (r.isError) {
      const { summary, hoverText } = formatError(r.text);
      const hover = new vscode.MarkdownString();
      hover.appendCodeblock(hoverText, "text");
      hover.isTrusted = true;
      errDecos.push({
        range,
        renderOptions: {
          after: { contentText: ` ✗ ${summary}` },
        },
        hoverMessage: hover,
      });
    } else {
      const { summary, hoverText } = formatResult(r.text);
      const deco: vscode.DecorationOptions = {
        range,
        renderOptions: {
          after: { contentText: ` → ${summary}` },
        },
      };
      // Add hover only if output was truncated
      if (hoverText) {
        const hover = new vscode.MarkdownString();
        hover.appendCodeblock(hoverText, "text");
        hover.isTrusted = true;
        deco.hoverMessage = hover;
      }
      okDecos.push(deco);
    }
  }

  editor.setDecorations(resultDecoration, okDecos);
  editor.setDecorations(errorDecoration, errDecos);
}

/** Dispose module resources. */
export function disposeInlineOutput(): void {
  resultDecoration.dispose();
  errorDecoration.dispose();
  runningDecoration.dispose();
  diagnostics.dispose();
  outputChannel?.dispose();
  results.clear();
}

// ── Helpers ────────────────────────────────────────────────

function rebaseInlineResults(
  input: InlineResult[],
  changes: LineChange[],
): InlineResult[] {
  let rebased = input.map((result) => ({ ...result }));
  let accumulatedDelta = 0;

  for (const change of changes) {
    const startLine = change.startLine + accumulatedDelta;
    const endLine = change.endLine + accumulatedDelta;
    const oldLineSpan = change.endLine - change.startLine;
    const newLineSpan = lineSpan(change.text);
    const delta = newLineSpan - oldLineSpan;
    const isPureInsertion =
      change.startLine === change.endLine &&
      change.startCharacter === change.endCharacter;

    rebased = rebased.flatMap((result) => {
      if (isPureInsertion) {
        if (result.line >= startLine) {
          return [{ ...result, line: result.line + delta }];
        }
        return [result];
      }

      if (result.line < startLine) return [result];
      if (result.line > endLine) return [{ ...result, line: result.line + delta }];
      return [];
    });

    accumulatedDelta += delta;
  }

  return rebased;
}

function lineSpan(text: string): number {
  if (text.length === 0) return 0;
  return text.split(/\r\n|\r|\n/).length - 1;
}

/**
 * Format a successful result for inline display.
 * Short single-line results are shown in full.
 * Longer results get a truncated summary + full text in hover.
 */
function formatResult(text: string): { summary: string; hoverText: string | null } {
  const lines = text.split("\n").filter((l) => l.trim().length > 0);
  if (lines.length === 0) return { summary: "(no output)", hoverText: null };

  if (lines.length === 1 && lines[0].length <= MAX_INLINE_LENGTH) {
    return { summary: lines[0], hoverText: null };
  }

  // Truncate for inline
  const first = lines[0].length > MAX_INLINE_LENGTH
    ? lines[0].slice(0, MAX_INLINE_LENGTH - 1) + "…"
    : lines[0];

  const suffix = lines.length > 1 ? ` (+${lines.length - 1} lines)` : "";
  return {
    summary: first + suffix,
    hoverText: text.trimEnd(),
  };
}

/**
 * Format an error for inline display.
 * Shows just the final error line (e.g. "ValueError: ...") inline.
 * Full traceback available in hover.
 */
function formatError(text: string): { summary: string; hoverText: string } {
  const lines = text.split("\n").filter((l) => l.trim().length > 0);

  // Find the last line that looks like an actual error message
  // (not a "File ..." or "    ^" line)
  let errorLine = lines[lines.length - 1] ?? "error";
  for (let i = lines.length - 1; i >= 0; i--) {
    const l = lines[i].trim();
    if (l && !l.startsWith("File ") && !l.startsWith("^") && !l.startsWith("~")) {
      errorLine = l;
      break;
    }
  }

  const summary = errorLine.length > MAX_INLINE_LENGTH
    ? errorLine.slice(0, MAX_INLINE_LENGTH - 1) + "…"
    : errorLine;

  return {
    summary,
    hoverText: text.trimEnd(),
  };
}

/**
 * Update VS Code diagnostics for error results.
 */
function updateDiagnostics(doc: vscode.TextDocument): void {
  const uri = doc.uri.toString();
  const docResults = results.get(uri);
  if (!docResults) {
    diagnostics.delete(doc.uri);
    return;
  }

  const diags: vscode.Diagnostic[] = [];
  for (const r of docResults) {
    if (!r.isError) continue;
    if (r.line >= doc.lineCount) continue;

    const { summary } = formatError(r.text);
    const range = new vscode.Range(r.line, 0, r.line, doc.lineAt(r.line).text.length);
    const diag = new vscode.Diagnostic(range, summary, vscode.DiagnosticSeverity.Error);
    diag.source = "rat";
    diags.push(diag);
  }

  diagnostics.set(doc.uri, diags);
}
