/**
 * decorations.ts — Zen styling for code cells.
 *
 * - Code cell body gets a subtle background to stand out from prose
 * - Fence lines (``` markers) get smaller, muted font
 * - Output blocks get their own lighter styling
 */

import * as vscode from "vscode";
import { parseCells, findOutputBlock } from "./cells";

// ── Decoration types ───────────────────────────────────────

/** Subtle transparent background for code cell body */
const codeCellBody = vscode.window.createTextEditorDecorationType({
  isWholeLine: true,
  backgroundColor: new vscode.ThemeColor("rat.cellBackground"),
});

/** Left accent bar on code lines */
const codeCellLeftBar = vscode.window.createTextEditorDecorationType({
  isWholeLine: true,
  borderWidth: "0 0 0 2px",
  borderStyle: "solid",
  borderColor: new vscode.ThemeColor("rat.cellAccent"),
});

/** Fence markers: smaller, muted */
const fenceLine = vscode.window.createTextEditorDecorationType({
  isWholeLine: true,
  color: new vscode.ThemeColor("rat.fenceColor"),
  textDecoration: "none; font-size: 0.8em",
});

/** Output block body */
const outputBody = vscode.window.createTextEditorDecorationType({
  isWholeLine: true,
  backgroundColor: new vscode.ThemeColor("rat.outputBackground"),
  color: new vscode.ThemeColor("editor.foreground"),
});

/** Output fence markers */
const outputFence = vscode.window.createTextEditorDecorationType({
  isWholeLine: true,
  color: new vscode.ThemeColor("rat.fenceColor"),
  textDecoration: "none; font-size: 0.8em",
});

// ── Apply decorations ──────────────────────────────────────

export function applyDecorations(editor: vscode.TextEditor): void {
  const doc = editor.document;

  const bodyRanges: vscode.DecorationOptions[] = [];
  const barRanges: vscode.DecorationOptions[] = [];
  const fenceRanges: vscode.DecorationOptions[] = [];
  const outBodyRanges: vscode.DecorationOptions[] = [];
  const outFenceRanges: vscode.DecorationOptions[] = [];

  const cells = parseCells(doc);

  for (const cell of cells) {
    // Fence lines — small and subtle
    fenceRanges.push({
      range: new vscode.Range(cell.openLine, 0, cell.openLine, doc.lineAt(cell.openLine).text.length),
    });
    fenceRanges.push({
      range: new vscode.Range(cell.closeLine, 0, cell.closeLine, doc.lineAt(cell.closeLine).text.length),
    });

    // Code body lines — background + left accent bar
    for (let line = cell.openLine + 1; line < cell.closeLine; line++) {
      const r = new vscode.Range(line, 0, line, doc.lineAt(line).text.length);
      bodyRanges.push({ range: r });
      barRanges.push({ range: r });
    }

    // Output block (if present)
    const output = findOutputBlock(doc, cell.closeLine);
    if (output) {
      outFenceRanges.push({
        range: new vscode.Range(output.startLine, 0, output.startLine, doc.lineAt(output.startLine).text.length),
      });
      outFenceRanges.push({
        range: new vscode.Range(output.endLine, 0, output.endLine, doc.lineAt(output.endLine).text.length),
      });
      for (let line = output.startLine + 1; line < output.endLine; line++) {
        outBodyRanges.push({
          range: new vscode.Range(line, 0, line, doc.lineAt(line).text.length),
        });
      }
    }
  }

  editor.setDecorations(codeCellBody, bodyRanges);
  editor.setDecorations(codeCellLeftBar, barRanges);
  editor.setDecorations(fenceLine, fenceRanges);
  editor.setDecorations(outputBody, outBodyRanges);
  editor.setDecorations(outputFence, outFenceRanges);
}

/** Clean up all decoration types (call on deactivate). */
export function disposeDecorations(): void {
  codeCellBody.dispose();
  codeCellLeftBar.dispose();
  fenceLine.dispose();
  outputBody.dispose();
  outputFence.dispose();
}
