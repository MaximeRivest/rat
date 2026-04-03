/**
 * intellisense.ts — Completions + Hover inside code cells.
 *
 * Completions call `look(code=…, cursor=…)` on the running kernel
 * so they see the live namespace (DataFrames, imported modules, etc.).
 *
 * Hover calls `look(at=…)` and shows the rich inspection output
 * that rat already formats (type, shape, head, docstring, methods).
 *
 * Both providers only activate when the cursor is inside a code
 * cell — outside cells VS Code's markdown / Quarto extensions keep
 * working normally.
 */

import * as vscode from "vscode";
import {
  parseCells,
  cellAtLine,
  codeContext,
  wordAtPosition,
} from "./cells";
import { existingClient } from "./rat";
import { resolveRuntime } from "./resolve";

// ── Completion ─────────────────────────────────────────────

export class RatCompletionProvider
  implements vscode.CompletionItemProvider
{
  async provideCompletionItems(
    document: vscode.TextDocument,
    position: vscode.Position,
    token: vscode.CancellationToken,
  ): Promise<vscode.CompletionItem[] | null> {
    const cell = getExecutableCell(document, position);
    if (!cell) return null;

    const { name } = resolveRuntime(cell.ratLang, document);
    const client = existingClient(name);
    if (!client) return null; // kernel not running yet

    const { code, cursor } = codeContext(cell, position);

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
    const cell = getExecutableCell(document, position);
    if (!cell) return null;

    const word = wordAtPosition(cell, position);
    if (!word) return null;

    const { name } = resolveRuntime(cell.ratLang, document);
    const client = existingClient(name);
    if (!client) return null;

    try {
      const info = await client.look(word);
      if (token.isCancellationRequested || !info) return null;

      // Pick a language id for syntax-highlighting the output
      const langId =
        cell.ratLang === "py"
          ? "python"
          : cell.ratLang === "r"
            ? "r"
            : cell.lang;

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

import type { CodeCell } from "./cells";

function getExecutableCell(
  document: vscode.TextDocument,
  position: vscode.Position,
): CodeCell | null {
  const cells = parseCells(document);
  const cell = cellAtLine(cells, position.line);
  if (!cell?.executable) return null;
  // Only inside the code body — not on the fence lines
  if (position.line <= cell.openLine || position.line >= cell.closeLine) {
    return null;
  }
  return cell;
}
