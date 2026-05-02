/**
 * documentModel.ts — Central parsed model for rat Markdown-like documents.
 *
 * The model is the shared source of truth for CodeLens, decorations, and
 * output updates: parse fenced code cells once, pair them with adjacent
 * ```output blocks, and expose stable-ish cell ids for queued work.
 */

import * as vscode from "vscode";
import {
  findOutputBlock,
  parseCells,
  type CodeCell,
  type OutputBlock,
} from "./cells";

export interface RatNotebookDocument {
  document: vscode.TextDocument;
  uri: vscode.Uri;
  version: number;
  cells: RatNotebookCell[];
  outputs: RatNotebookOutputBlock[];
}

export interface RatNotebookCell extends CodeCell {
  /** Stable for a document version; useful for queue targets/logging. */
  id: string;
  openFenceRange: vscode.Range;
  codeRange: vscode.Range;
  closeFenceRange: vscode.Range;
  output: RatNotebookOutputBlock | null;
}

export interface RatNotebookOutputBlock extends OutputBlock {
  range: vscode.Range;
  openFenceRange: vscode.Range;
  bodyRange: vscode.Range;
  closeFenceRange: vscode.Range;
  imageRange: vscode.Range | null;
  pairedCellOpenLine?: number;
}

const OUTPUT_OPEN_RE = /^```output(?::\S+)?(\s*$|\s*\|)/;
const OUTPUT_CLOSE_RE = /^```\s*$/;
const IMAGE_LINK_RE = /^!\[.*\]\(.*\)\s*$/;

export function parseRatNotebookDocument(
  document: vscode.TextDocument,
): RatNotebookDocument {
  const rawOutputs = parseOutputBlocks(document);
  const outputsByStartLine = new Map<number, RatNotebookOutputBlock>();
  for (const output of rawOutputs) outputsByStartLine.set(output.startLine, output);

  const cells: RatNotebookCell[] = parseCells(document).map((cell, ordinal) => {
    const pairedRaw = findOutputBlock(document, cell.closeLine);
    const output = pairedRaw ? outputsByStartLine.get(pairedRaw.startLine) ?? null : null;
    if (output) output.pairedCellOpenLine = cell.openLine;

    return {
      ...cell,
      id: cellId(document, cell, ordinal),
      openFenceRange: lineRange(document, cell.openLine),
      codeRange: codeBodyRange(document, cell),
      closeFenceRange: lineRange(document, cell.closeLine),
      output,
    };
  });

  return {
    document,
    uri: document.uri,
    version: typeof document.version === "number" ? document.version : 0,
    cells,
    outputs: rawOutputs,
  };
}

export function parseOutputBlocks(
  document: vscode.TextDocument,
): RatNotebookOutputBlock[] {
  const outputs: RatNotebookOutputBlock[] = [];
  const lineCount = document.lineCount;
  let i = 0;

  while (i < lineCount) {
    if (!OUTPUT_OPEN_RE.test(document.lineAt(i).text)) {
      i++;
      continue;
    }

    const startLine = i;
    i++;

    while (i < lineCount && !OUTPUT_CLOSE_RE.test(document.lineAt(i).text)) i++;
    if (i >= lineCount) break;

    const endLine = i;
    i++;

    let imageEndLine = endLine;
    while (i < lineCount) {
      const text = document.lineAt(i).text;
      if (IMAGE_LINK_RE.test(text)) {
        imageEndLine = i;
        i++;
      } else if (text.trim() === "") {
        i++;
      } else {
        break;
      }
    }

    outputs.push(outputBlockFromLines(document, { startLine, endLine, imageEndLine }));
  }

  return outputs;
}

export function cellAtModelLine(
  model: RatNotebookDocument,
  line: number,
): RatNotebookCell | undefined {
  return model.cells.find((cell) => line >= cell.openLine && line <= cell.closeLine);
}

export function cellsUpToModelLine(
  model: RatNotebookDocument,
  line: number,
): RatNotebookCell[] {
  return model.cells.filter((cell) => cell.openLine <= line);
}

export function findModelCellByFenceLines(
  model: RatNotebookDocument,
  openLine: number,
  closeLine: number,
): RatNotebookCell | undefined {
  return model.cells.find((cell) => cell.openLine === openLine && cell.closeLine === closeLine);
}

function outputBlockFromLines(
  document: vscode.TextDocument,
  output: OutputBlock,
): RatNotebookOutputBlock {
  const range = new vscode.Range(
    output.startLine,
    0,
    output.imageEndLine,
    document.lineAt(output.imageEndLine).text.length,
  );
  const openFenceRange = lineRange(document, output.startLine);
  const closeFenceRange = lineRange(document, output.endLine);
  const bodyRange = new vscode.Range(
    output.startLine + 1,
    0,
    output.endLine,
    0,
  );
  const imageRange = output.imageEndLine > output.endLine
    ? new vscode.Range(
      output.endLine + 1,
      0,
      output.imageEndLine,
      document.lineAt(output.imageEndLine).text.length,
    )
    : null;

  return {
    ...output,
    range,
    openFenceRange,
    bodyRange,
    closeFenceRange,
    imageRange,
  };
}

function lineRange(document: vscode.TextDocument, line: number): vscode.Range {
  const safeLine = Math.max(0, Math.min(line, document.lineCount - 1));
  return new vscode.Range(
    safeLine,
    0,
    safeLine,
    document.lineAt(safeLine).text.length,
  );
}

function codeBodyRange(document: vscode.TextDocument, cell: CodeCell): vscode.Range {
  const startLine = Math.min(cell.openLine + 1, cell.closeLine);
  const endLine = Math.max(startLine, cell.closeLine);
  return new vscode.Range(
    startLine,
    0,
    endLine,
    document.lineAt(Math.min(endLine, document.lineCount - 1)).text.length,
  );
}

function cellId(
  document: vscode.TextDocument,
  cell: CodeCell,
  ordinal: number,
): string {
  return `${document.uri.toString()}#${ordinal}:${cell.openLine}:${shortHash(cell.lang + "\n" + cell.code)}`;
}

function shortHash(value: string): string {
  let h = 2166136261;
  for (let i = 0; i < value.length; i++) {
    h ^= value.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return (h >>> 0).toString(36).slice(0, 8);
}
