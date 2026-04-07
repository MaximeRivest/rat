/**
 * intellisense.ts — Completions + Hover inside code cells and source files.
 *
 * Completions call `look(code=…, cursor=…)` on the running kernel
 * so they see the live namespace (DataFrames, imported modules, etc.).
 *
 * Hover calls `look(at=…)` and shows the rich inspection output
 * that rat already formats (type, shape, head, docstring, methods).
 *
 * Both providers activate:
 *   - Inside fenced code cells (notebook mode)
 *   - Anywhere in source files (.py, .r, .jl, .js, .sh)
 */

import * as vscode from "vscode";
import {
  parseCells,
  cellAtLine,
  codeContext,
  wordAtPosition,
  type CodeCell,
} from "./cells";
import { existingClient } from "./rat";
import { resolveRuntime } from "./resolve";
import { detectFileLang } from "./langDetect";

// ── Language ID → fence language for syntax highlighting ───

const LANG_HIGHLIGHT: Record<string, string> = {
  py: "python",
  r: "r",
  jl: "julia",
  js: "javascript",
  sh: "bash",
};

// ── Resolve context: either a cell in a notebook or whole source file ──

interface CellContext {
  kind: "cell";
  cell: CodeCell;
  ratLang: string;
}

interface SourceContext {
  kind: "source";
  ratLang: string;
}

type CodeCtx = CellContext | SourceContext;

function getContext(
  document: vscode.TextDocument,
  position: vscode.Position,
): CodeCtx | null {
  const fl = detectFileLang(document);

  if (fl.mode === "source" && fl.ratLang) {
    return { kind: "source", ratLang: fl.ratLang };
  }

  // Notebook mode — must be inside a code cell
  const cells = parseCells(document);
  const cell = cellAtLine(cells, position.line);
  if (!cell?.executable) return null;
  if (position.line <= cell.openLine || position.line >= cell.closeLine) {
    return null;
  }
  return { kind: "cell", cell, ratLang: cell.ratLang };
}

// ── Completion ─────────────────────────────────────────────

export class RatCompletionProvider
  implements vscode.CompletionItemProvider
{
  async provideCompletionItems(
    document: vscode.TextDocument,
    position: vscode.Position,
    token: vscode.CancellationToken,
  ): Promise<vscode.CompletionItem[] | null> {
    const ctx = getContext(document, position);
    if (!ctx) return null;

    const { name } = resolveRuntime(ctx.ratLang, document);
    const client = existingClient(name);
    if (!client) return null;

    let code: string;
    let cursor: number;

    if (ctx.kind === "cell") {
      const cc = codeContext(ctx.cell, position);
      code = cc.code;
      cursor = cc.cursor;
    } else {
      // Source file — send text up to cursor position
      code = document.getText(
        new vscode.Range(0, 0, position.line, position.character),
      );
      cursor = code.length;
    }

    try {
      const items = await client.complete(code, cursor);
      if (token.isCancellationRequested) return null;

      return items.map((label) => {
        let kind = vscode.CompletionItemKind.Variable;
        let insertText = label;

        if (label.endsWith("(")) {
          kind = vscode.CompletionItemKind.Function;
          insertText = label.slice(0, -1);
        } else if (label.endsWith("=")) {
          kind = vscode.CompletionItemKind.Property;
        } else if (label === label.toUpperCase() && label.length > 1) {
          kind = vscode.CompletionItemKind.Constant;
        }

        const ci = new vscode.CompletionItem(insertText, kind);
        ci.detail = "rat";
        return ci;
      });
    } catch {
      return null;
    }
  }
}

// ── Hover ──────────────────────────────────────────────────

export class RatHoverProvider implements vscode.HoverProvider {
  async provideHover(
    document: vscode.TextDocument,
    position: vscode.Position,
    token: vscode.CancellationToken,
  ): Promise<vscode.Hover | null> {
    const ctx = getContext(document, position);
    if (!ctx) return null;

    let word: string | null;
    if (ctx.kind === "cell") {
      word = wordAtPosition(ctx.cell, position);
    } else {
      // Source file — extract word at position directly
      word = wordAtPositionInSource(document, position);
    }
    if (!word) return null;

    const { name } = resolveRuntime(ctx.ratLang, document);
    const client = existingClient(name);
    if (!client) return null;

    try {
      const info = await client.look(word);
      if (token.isCancellationRequested || !info) return null;

      const langId = LANG_HIGHLIGHT[ctx.ratLang] ?? ctx.ratLang;
      const md = new vscode.MarkdownString();
      md.appendCodeblock(info, langId);
      md.isTrusted = true;
      return new vscode.Hover(md);
    } catch {
      return null;
    }
  }
}

// ── helper ─────────────────────────────────────────────────

function wordAtPositionInSource(
  document: vscode.TextDocument,
  position: vscode.Position,
): string | null {
  const line = document.lineAt(position.line).text;
  const col = Math.min(position.character, line.length);

  let start = col;
  while (start > 0 && /[\w.]/.test(line[start - 1])) start--;
  let end = col;
  while (end < line.length && /[\w.]/.test(line[end])) end++;

  const word = line.slice(start, end);
  return word || null;
}
