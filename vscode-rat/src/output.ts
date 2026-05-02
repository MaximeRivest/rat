/**
 * output.ts — Manage ```output blocks in the document.
 *
 * After running a cell the extension inserts (or replaces) an output
 * block right below the cell's closing fence.  The status line
 * (e.g. "✓ 1.5s | 1 var") is placed on the output fence itself:
 *
 *   ```python
 *   dspy.LM('gpt-4.1')
 *   ```
 *
 *   ```output | ✓ 1.5s | 1 var
 *   <dspy.clients.lm.LM object at 0x7ff6fa8574d0>
 *   ```
 *
 * When there is no output (only a status line), no output block is
 * created — instead the code cell's opening fence is annotated:
 *
 *   ```py ✓
 *   import dspy
 *   ```
 *
 * On re-run the existing output block + trailing images are replaced.
 */

import * as vscode from "vscode";
import { findOutputBlock } from "./cells";
import { findModelCellByFenceLines, parseRatNotebookDocument } from "./documentModel";

// ── Status line detection ──────────────────────────────────

/**
 * Matches the rat status trailer, e.g.:
 *   ✓ 150ms | 2 vars
 *   ✗ error | 0 vars
 *   ✓ 1.5s | 1 var
 */
const STATUS_RE = /^[✓✗] .+$/;

/**
 * Split output text into body (actual output) and status line.
 * The status line is always the last non-empty line if it matches
 * the pattern.
 */
export function separateStatus(text: string): { body: string; status: string } {
  const trimmed = text.trimEnd();
  if (!trimmed) return { body: "", status: "" };

  const lines = trimmed.split("\n");

  // Walk backwards to find the status line (skip trailing blanks)
  let statusIdx = -1;
  for (let i = lines.length - 1; i >= 0; i--) {
    if (lines[i].trim() === "") continue;
    if (STATUS_RE.test(lines[i].trim())) {
      statusIdx = i;
    }
    break;
  }

  if (statusIdx === -1) {
    return { body: trimmed, status: "" };
  }

  const status = lines[statusIdx].trim();
  const bodyLines = lines.slice(0, statusIdx);
  // Trim trailing blank lines from the body
  while (bodyLines.length > 0 && bodyLines[bodyLines.length - 1].trim() === "") {
    bodyLines.pop();
  }
  return { body: bodyLines.join("\n"), status };
}

// ── Public API ─────────────────────────────────────────────

/**
 * Insert or replace the output region after a code cell.
 * Status is placed on the fence line; if there's no body content,
 * no output block is created.
 *
 * @param editor    Active editor
 * @param openLine  Line number of the code cell's opening fence
 * @param closeLine Line number of the code cell's closing fence
 * @param text      Output body text
 * @param images    Markdown image links to append (may be empty)
 * @param maxLines  Max output lines (0 = unlimited)
 * @param statusOverride Structured rat status trailer, if supplied by the protocol
 */
export async function upsertOutput(
  editor: vscode.TextEditor,
  openLine: number,
  closeLine: number,
  text: string,
  images: string[],
  maxLines: number,
  statusOverride?: string,
): Promise<void> {
  const doc = editor.document;
  const model = parseRatNotebookDocument(doc);
  const existing = findModelCellByFenceLines(model, openLine, closeLine)?.output
    ?? findOutputBlock(doc, closeLine);
  const { body, status } = statusOverride !== undefined
    ? { body: text.trimEnd(), status: statusOverride }
    : separateStatus(text);
  const isError = status.startsWith("✗");
  const hasBody = body.trim().length > 0 || images.length > 0;
  // Collapse to a fence annotation only for successful runs with no output.
  // Errors always get an output block so the message / traceback is visible.
  const collapseToAnnotation = !hasBody && !isError && !!status;
  // When an error has no traceback body, show the status as the body
  // so the user sees *something* in the output block.
  const effectiveBody = (!hasBody && isError) ? status : body;
  const needsBlock = hasBody || (isError && !!status);

  if (!needsBlock && !collapseToAnnotation && !existing) {
    // Nothing to show, nothing to clean up
    return;
  }

  await editor.edit(
    (eb) => {
      // ── Annotate the opening fence with ✓ ────────────────
      const openLineText = doc.lineAt(openLine).text;
      // Strip any previous annotation (e.g. " ✓" or " ✗")
      const cleanOpen = openLineText.replace(/\s+[✓✗].*$/, "");
      if (collapseToAnnotation) {
        // Success, no output → put ✓ on the opening fence
        const newOpen = cleanOpen + " ✓";
        if (newOpen !== openLineText) {
          eb.replace(
            new vscode.Range(openLine, 0, openLine, openLineText.length),
            newOpen,
          );
        }
      } else if (cleanOpen !== openLineText) {
        // Has output block — remove any stale annotation from the opening fence
        eb.replace(
          new vscode.Range(openLine, 0, openLine, openLineText.length),
          cleanOpen,
        );
      }

      // ── Output block ─────────────────────────────────────
      if (needsBlock) {
        const block = buildBlock(effectiveBody, status, images, maxLines);

        if (existing) {
          let start = existing.startLine;
          if (start > 0 && doc.lineAt(start - 1).isEmptyOrWhitespace) start--;

          let endLine = Math.max(existing.endLine, existing.imageEndLine);
          while (
            endLine + 1 < doc.lineCount &&
            doc.lineAt(endLine + 1).isEmptyOrWhitespace
          ) {
            endLine++;
          }

          const range = new vscode.Range(
            start,
            0,
            endLine,
            doc.lineAt(endLine).text.length,
          );
          const hasContentAfter = endLine + 1 < doc.lineCount &&
            !doc.lineAt(endLine + 1).isEmptyOrWhitespace;
          const suffix = hasContentAfter ? "\n" : "";
          eb.replace(range, block.trimStart().trimEnd() + suffix);
        } else {
          const pos = new vscode.Position(
            closeLine,
            doc.lineAt(closeLine).text.length,
          );
          const hasContentAfter = closeLine + 1 < doc.lineCount &&
            !doc.lineAt(closeLine + 1).isEmptyOrWhitespace;
          const suffix = hasContentAfter ? "\n" : "";
          eb.insert(pos, block.trimEnd() + suffix);
        }
      } else if (existing) {
        // No body content — remove the existing output block
        let start = existing.startLine;
        if (start > 0 && doc.lineAt(start - 1).isEmptyOrWhitespace) start--;

        let endLine = Math.max(existing.endLine, existing.imageEndLine);
        while (
          endLine + 1 < doc.lineCount &&
          doc.lineAt(endLine + 1).isEmptyOrWhitespace
        ) {
          endLine++;
        }

        // Delete from start of region through end (inclusive)
        const delEnd = endLine + 1 < doc.lineCount ? endLine + 1 : endLine;
        if (delEnd > endLine) {
          eb.delete(new vscode.Range(start, 0, delEnd, 0));
        } else {
          // Last lines in file — delete including preceding newline
          const delStart = start > 0 ? start - 1 : start;
          eb.delete(
            new vscode.Range(
              delStart,
              start > 0 ? doc.lineAt(delStart).text.length : 0,
              endLine,
              doc.lineAt(endLine).text.length,
            ),
          );
        }
      }
    },
    { undoStopBefore: true, undoStopAfter: true },
  );
}

/** Delete every ```output block (and trailing images) and fence annotations. */
export async function clearAllOutputs(
  editor: vscode.TextEditor,
): Promise<void> {
  const doc = editor.document;
  const model = parseRatNotebookDocument(doc);

  // Collect output block regions bottom-up so line numbers stay valid.
  const regions: { start: number; end: number }[] = model.outputs.map((output) => {
    let start = output.startLine;
    if (start > 0 && doc.lineAt(start - 1).isEmptyOrWhitespace) start--;

    let end = Math.max(output.endLine, output.imageEndLine) + 1;
    while (end < doc.lineCount && doc.lineAt(end).isEmptyOrWhitespace) end++;

    return { start, end };
  });

  // Collect opening fence annotations to clean.
  const annotations: { line: number; clean: string }[] = [];
  for (const cell of model.cells) {
    const lineText = doc.lineAt(cell.openLine).text;
    if (/^`{3,}\w.*\s+[✓✗]/.test(lineText)) {
      const clean = lineText.replace(/\s+[✓✗].*$/, "");
      if (clean !== lineText) annotations.push({ line: cell.openLine, clean });
    }
  }

  if (regions.length === 0 && annotations.length === 0) return;

  await editor.edit((eb) => {
    // Delete output blocks (bottom-up)
    for (let r = regions.length - 1; r >= 0; r--) {
      const { start, end } = regions[r];
      eb.delete(new vscode.Range(start, 0, end, 0));
    }
    // Clean fence annotations
    for (const { line, clean } of annotations) {
      const lineText = doc.lineAt(line).text;
      eb.replace(
        new vscode.Range(line, 0, line, lineText.length),
        clean,
      );
    }
  });
}

// ── internal ───────────────────────────────────────────────

function buildBlock(
  body: string,
  status: string,
  images: string[],
  maxLines: number,
): string {
  const raw = body.trimEnd();
  let content: string;

  if (maxLines > 0) {
    const lines = raw.split("\n");
    if (lines.length > maxLines) {
      const kept = lines.slice(0, maxLines);
      kept.push(`… ${lines.length - maxLines} more lines`);
      content = kept.join("\n");
    } else {
      content = raw;
    }
  } else {
    content = raw;
  }

  // Fence line includes status when available. Use enough backticks to
  // safely contain Markdown/code output that itself includes ``` fences.
  const ticks = outputFenceTicks(content);
  const fence = status ? `${ticks}output | ${status}` : `${ticks}output`;

  let result = "\n\n" + fence + "\n" + content + "\n" + ticks;

  for (const img of images) {
    result += "\n\n" + img;
  }

  return result;
}

function outputFenceTicks(content: string): string {
  let maxRun = 0;
  for (const match of content.matchAll(/`+/g)) {
    maxRun = Math.max(maxRun, match[0].length);
  }
  return "`".repeat(Math.max(3, maxRun + 1));
}
