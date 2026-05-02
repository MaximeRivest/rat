/**
 * cells.ts — Parse fenced code blocks from markdown / Quarto documents.
 *
 * Handles:
 *   ```python          (standard markdown)
 *   ```{python}        (Quarto / knitr)
 *   ````python         (four-backtick fences)
 *   #| eval: false     (Quarto cell option → marks cell non-executable)
 *
 * Also detects ```output blocks so the rest of the extension can
 * find / replace them.
 */

import * as vscode from "vscode";
import { ratLangForFence } from "./languages";

export { ratLangForFence } from "./languages";

// ── Types ──────────────────────────────────────────────────

export interface CodeCell {
  /** Original language identifier from the fence ("python", "r", …) */
  lang: string;
  /** Canonical rat language name ("py", "sh", "r", "jl", "js") */
  ratLang: string;
  /** Code content between the fences (no fences, no trailing newline) */
  code: string;
  /** Full range covering both fences and content */
  range: vscode.Range;
  /** Line number of the opening fence */
  openLine: number;
  /** Line number of the closing fence */
  closeLine: number;
  /** false when the cell has `#| eval: false` */
  executable: boolean;
}

export interface OutputBlock {
  /** Line of the opening ```output fence */
  startLine: number;
  /** Line of the closing ``` fence */
  endLine: number;
  /** Lines after the block that are image links produced by rat */
  imageEndLine: number;
}

// ── Parsers ────────────────────────────────────────────────

const FENCE_OPEN = /^(\s{0,3})(`{3,})\s*(?:\{(\w+)(?:[,\s][^}]*)?\}|(\w+))(?:\s+[✓✗].*?)?\s*$/;
const FENCE_CLOSE = /^(\s{0,3})`{3,}\s*$/;

/**
 * Parse all executable code cells from a document.
 * Cells whose fence language is not in LANG_MAP are skipped
 * (yaml, json, html, css, … won't get Run buttons).
 */
export function parseCells(document: vscode.TextDocument): CodeCell[] {
  const cells: CodeCell[] = [];
  const lineCount = document.lineCount;
  let i = 0;

  while (i < lineCount) {
    const lineText = document.lineAt(i).text;
    const openMatch = lineText.match(FENCE_OPEN);

    if (openMatch) {
      const backtickLen = openMatch[2].length;
      const lang = openMatch[3] ?? openMatch[4];
      const ratLang = ratLangForFence(lang);
      const openLine = i;
      i++;
      const codeLines: string[] = [];

      // Always advance to the matching close fence, even for unsupported
      // languages. Otherwise cells inside literal Markdown/examples/output
      // blocks can be detected as runnable cells.
      while (i < lineCount) {
        const lt = document.lineAt(i).text;
        const closeMatch = lt.match(FENCE_CLOSE);
        if (closeMatch) {
          // Closing fence must have ≥ same backtick count
          const closeTicks = lt.trim().replace(/[^`]/g, "").length;
          if (closeTicks >= backtickLen) break;
        }
        codeLines.push(lt);
        i++;
      }

      if (i < lineCount && ratLang) {
        const closeLine = i;
        const code = codeLines.join("\n");
        const executable = !/^#\|\s*eval:\s*false/m.test(code);

        cells.push({
          lang,
          ratLang,
          code,
          range: new vscode.Range(openLine, 0, closeLine, document.lineAt(closeLine).text.length),
          openLine,
          closeLine,
          executable,
        });
      }
    }
    i++;
  }
  return cells;
}

/**
 * Find the output block paired to a code cell.
 *
 * **Pairing rule**: an output block belongs to a code cell iff there
 * is _only whitespace_ (blank lines) between the code cell's closing
 * fence and the output block's opening ` ```output ` fence.  Any
 * non-whitespace content breaks the pairing.
 */
export function findOutputBlock(
  document: vscode.TextDocument,
  afterLine: number,
): OutputBlock | null {
  const lineCount = document.lineCount;
  let i = afterLine + 1;

  // Skip blank lines — but if we hit any non-blank, non-```output
  // line, the pairing is broken.
  while (i < lineCount && document.lineAt(i).isEmptyOrWhitespace) {
    i++;
  }

  if (i >= lineCount) return null;
  const openMatch = document.lineAt(i).text.match(/^(\s{0,3})(`{3,})output(?::\S+)?(\s*$|\s*\|)/);
  if (!openMatch) return null;
  const backtickLen = openMatch[2].length;

  const startLine = i;
  i++;

  // Find closing fence. It must use at least as many backticks as the opener
  // so output bodies can safely contain shorter Markdown code fences.
  while (i < lineCount) {
    const closeMatch = document.lineAt(i).text.match(/^(\s{0,3})(`{3,})\s*$/);
    if (closeMatch && closeMatch[2].length >= backtickLen) break;
    i++;
  }
  if (i >= lineCount) return null;

  const endLine = i;
  i++;

  // Consume following image links and interleaved blank lines
  let imageEndLine = endLine;
  while (i < lineCount) {
    const t = document.lineAt(i).text;
    if (/^!\[.*\]\(.*\)\s*$/.test(t)) {
      imageEndLine = i;
      i++;
    } else if (t.trim() === "") {
      i++; // skip blank between images
    } else {
      break;
    }
  }

  return { startLine, endLine, imageEndLine };
}

// ── Cursor helpers ─────────────────────────────────────────

/** Return the cell whose range contains `line`, or undefined. */
export function cellAtLine(cells: CodeCell[], line: number): CodeCell | undefined {
  return cells.find((c) => line >= c.openLine && line <= c.closeLine);
}

/**
 * Re-find a cell in the current document state.
 *
 * Line numbers go stale whenever an earlier output block is
 * inserted / resized.  This function re-parses the document
 * and matches by **(code content + language)**.  When several
 * cells have identical code (rare), the one closest to
 * `hintLine` wins.
 *
 * Returns `null` if the cell was deleted or edited away.
 */
export function refindCell(
  document: vscode.TextDocument,
  code: string,
  ratLang: string,
  hintLine: number,
): CodeCell | null {
  const cells = parseCells(document);
const matches = cells.filter(
    (c) => c.code === code && c.ratLang === ratLang,
  );
  if (matches.length === 0) return null;
  if (matches.length === 1) return matches[0];
  // Disambiguate: closest to where it was when queued.
  matches.sort(
    (a, b) =>
      Math.abs(a.openLine - hintLine) - Math.abs(b.openLine - hintLine),
  );
  return matches[0];
}

/** Return all cells whose opening fence is at or above `line`. */
export function cellsUpTo(cells: CodeCell[], line: number): CodeCell[] {
  return cells.filter((c) => c.openLine <= line);
}

/**
 * Map a document cursor position to a (code, cursorOffset) pair
 * suitable for rat's `look(code=…, cursor=…)` completion API.
 * `code` is the full cell content; `cursorOffset` is a char offset.
 */
export function codeContext(
  cell: CodeCell,
  position: vscode.Position,
): { code: string; cursor: number } {
  const codeLines = cell.code.split("\n");
  const lineInCode = position.line - (cell.openLine + 1);

  if (lineInCode < 0 || lineInCode >= codeLines.length) {
    return { code: cell.code, cursor: cell.code.length };
  }

  let cursor = 0;
  for (let j = 0; j < lineInCode; j++) {
    cursor += codeLines[j].length + 1;
  }
  cursor += Math.min(position.character, codeLines[lineInCode].length);
  return { code: cell.code, cursor };
}

/**
 * Extract the dotted name at `position` inside a cell.
 * e.g. "df.columns" when the cursor is anywhere on that token.
 */
export function wordAtPosition(
  cell: CodeCell,
  position: vscode.Position,
): string | null {
  const codeLines = cell.code.split("\n");
  const lineInCode = position.line - (cell.openLine + 1);
  if (lineInCode < 0 || lineInCode >= codeLines.length) return null;

  const line = codeLines[lineInCode];
  const col = Math.min(position.character, line.length);

  let start = col;
  while (start > 0 && /[\w.]/.test(line[start - 1])) start--;
  let end = col;
  while (end < line.length && /[\w.]/.test(line[end])) end++;

  const word = line.slice(start, end);
  return word || null;
}
