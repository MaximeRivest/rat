/**
 * navigation.ts — lightweight symbol navigation inside notebook code cells.
 *
 * Markdown / Quarto fenced code blocks do not get the normal language-server
 * experience, so rat provides a small document-local navigator:
 *   - Ctrl/Cmd+Click → definition / declaration in this notebook
 *   - Find References / Peek References → all occurrences in same-language cells
 *   - Document highlights → quick at-a-glance usage highlighting
 *
 * This is intentionally conservative and local. It does not try to replace
 * Python / R / Julia language servers; it only covers common definitions in
 * fenced cells where VS Code otherwise sees mostly Markdown.
 */

import * as fs from "fs";
import * as vscode from "vscode";
import {
  cellAtLine,
  parseCells,
  type CodeCell,
} from "./cells";
import { existingOrRunningClient } from "./rat";
import { resolveRuntime } from "./resolve";

interface SymbolAtPosition {
  /** Local identifier used for document-local definitions/references. */
  name: string;
  /** Richer expression to ask the live runtime about, e.g. `dspy.Module`. */
  lookupName: string;
  cell: CodeCell;
}

interface SymbolScan {
  definitions: vscode.Location[];
  references: vscode.Location[];
  highlights: vscode.DocumentHighlight[];
}

const KEYWORDS: Record<string, Set<string>> = {
  py: new Set([
    "False", "None", "True", "and", "as", "assert", "async", "await",
    "break", "class", "continue", "def", "del", "elif", "else", "except",
    "finally", "for", "from", "global", "if", "import", "in", "is", "lambda",
    "nonlocal", "not", "or", "pass", "raise", "return", "try", "while", "with", "yield",
  ]),
  js: new Set([
    "await", "break", "case", "catch", "class", "const", "continue", "debugger",
    "default", "delete", "do", "else", "export", "extends", "finally", "for",
    "function", "if", "import", "in", "instanceof", "let", "new", "return",
    "switch", "this", "throw", "try", "typeof", "var", "void", "while", "with", "yield",
  ]),
  r: new Set([
    "break", "else", "FALSE", "for", "function", "if", "in", "Inf", "NA", "NaN",
    "next", "NULL", "repeat", "TRUE", "while",
  ]),
  jl: new Set([
    "abstract", "baremodule", "begin", "break", "catch", "const", "continue",
    "do", "else", "elseif", "end", "export", "finally", "for", "function", "if",
    "import", "let", "macro", "module", "mutable", "primitive", "quote", "return",
    "struct", "try", "using", "while",
  ]),
  sh: new Set([
    "case", "do", "done", "elif", "else", "esac", "fi", "for", "function", "if",
    "in", "select", "then", "until", "while",
  ]),
};

function symbolAtPosition(
  document: vscode.TextDocument,
  position: vscode.Position,
): SymbolAtPosition | null {
  const cells = parseCells(document);
  const cell = cellAtLine(cells, position.line);
  if (!cell?.executable) return null;
  if (position.line <= cell.openLine || position.line >= cell.closeLine) return null;

  const line = document.lineAt(position.line).text;
  const col = Math.min(position.character, line.length);

  let start = col;
  if (start > 0 && !isSymbolChar(line[start], cell.ratLang) && isSymbolChar(line[start - 1], cell.ratLang)) {
    start--;
  }
  while (start > 0 && isSymbolChar(line[start - 1], cell.ratLang)) start--;

  let end = Math.max(col, start);
  while (end < line.length && isSymbolChar(line[end], cell.ratLang)) end++;

  const name = line.slice(start, end);
  if (!isNavigableSymbol(name, cell.ratLang)) return null;
  return { name, lookupName: dottedLookupAtPosition(line, position.character, cell.ratLang) ?? name, cell };
}

function dottedLookupAtPosition(
  line: string,
  character: number,
  ratLang: string,
): string | null {
  if (ratLang !== "py" && ratLang !== "r" && ratLang !== "jl" && ratLang !== "js") {
    return null;
  }

  const col = Math.min(character, line.length);
  let start = col;
  if (start > 0 && !/[A-Za-z0-9_.$]/.test(line[start] ?? "") && /[A-Za-z0-9_]/.test(line[start - 1])) {
    start--;
  }
  while (start > 0 && /[A-Za-z0-9_.$]/.test(line[start - 1])) start--;

  let end = Math.max(col, start);
  while (end < line.length && /[A-Za-z0-9_.$]/.test(line[end])) end++;

  const expr = line.slice(start, end).replace(/^\.+|\.+$/g, "");
  if (!expr || /^\d/.test(expr) || expr.includes("..")) return null;
  return expr;
}

function isNavigableSymbol(name: string, ratLang: string): boolean {
  if (!name) return false;
  if (KEYWORDS[ratLang]?.has(name)) return false;
  if (/^\d/.test(name)) return false;
  return true;
}

function isSymbolChar(ch: string | undefined, ratLang: string): boolean {
  if (!ch) return false;
  if (ratLang === "r") return /[A-Za-z0-9_.]/.test(ch);
  if (ratLang === "js") return /[A-Za-z0-9_$]/.test(ch);
  return /[A-Za-z0-9_]/.test(ch);
}

function stripComment(line: string, ratLang: string): string {
  const marker = ratLang === "js" ? "//" : "#";
  const idx = line.indexOf(marker);
  return idx >= 0 ? line.slice(0, idx) : line;
}

function scanSymbol(
  document: vscode.TextDocument,
  name: string,
  ratLang: string,
): SymbolScan {
  const definitions: vscode.Location[] = [];
  const references: vscode.Location[] = [];
  const highlights: vscode.DocumentHighlight[] = [];

  for (const cell of parseCells(document)) {
    if (!cell.executable || cell.ratLang !== ratLang) continue;

    const lines = cell.code.split("\n");
    for (let i = 0; i < lines.length; i++) {
      const docLine = cell.openLine + 1 + i;
      const line = lines[i];
      const searchable = stripComment(line, ratLang);
      const defRanges = definitionRangesOnLine(searchable, docLine, name, ratLang);
      const defKeys = new Set(defRanges.map(rangeKey));

      for (const start of symbolStarts(searchable, name, ratLang)) {
        const range = new vscode.Range(docLine, start, docLine, start + name.length);
        const isDefinition = defKeys.has(rangeKey(range));
        const loc = new vscode.Location(document.uri, range);
        references.push(loc);
        highlights.push(new vscode.DocumentHighlight(
          range,
          isDefinition
            ? vscode.DocumentHighlightKind.Write
            : vscode.DocumentHighlightKind.Text,
        ));
      }

      definitions.push(...defRanges.map((range) => new vscode.Location(document.uri, range)));
    }
  }

  return { definitions: uniqueLocations(definitions), references: uniqueLocations(references), highlights };
}

function symbolStarts(line: string, name: string, ratLang: string): number[] {
  const starts: number[] = [];
  let from = 0;
  while (from <= line.length) {
    const idx = line.indexOf(name, from);
    if (idx === -1) break;
    const before = idx > 0 ? line[idx - 1] : undefined;
    const after = line[idx + name.length];
    if (!isSymbolChar(before, ratLang) && !isSymbolChar(after, ratLang)) {
      starts.push(idx);
    }
    from = idx + Math.max(1, name.length);
  }
  return starts;
}

function definitionRangesOnLine(
  line: string,
  docLine: number,
  name: string,
  ratLang: string,
): vscode.Range[] {
  const ranges: vscode.Range[] = [];
  const escaped = escapeRegExp(name);

  const addDirect = (pattern: RegExp) => {
    const match = line.match(pattern);
    if (!match || match.index === undefined) return;
    const offsetInMatch = match[0].indexOf(name);
    if (offsetInMatch < 0) return;
    const start = match.index + offsetInMatch;
    ranges.push(new vscode.Range(docLine, start, docLine, start + name.length));
  };

  switch (ratLang) {
    case "py":
      addDirect(new RegExp(`^\\s*(?:async\\s+)?def\\s+${escaped}\\b`));
      addDirect(new RegExp(`^\\s*class\\s+${escaped}\\b`));
      addDirect(new RegExp(`^\\s*${escaped}\\s*(?::[^=]+)?=(?!=)`));
      addDirect(new RegExp(`^\\s*for\\s+${escaped}\\b`));
      addDirect(new RegExp(`\\bas\\s+${escaped}\\b`));
      ranges.push(...pythonImportRanges(line, docLine, name));
      break;
    case "js":
      addDirect(new RegExp(`^\\s*(?:export\\s+)?(?:async\\s+)?function\\s+${escaped}\\b`));
      addDirect(new RegExp(`^\\s*(?:export\\s+)?class\\s+${escaped}\\b`));
      addDirect(new RegExp(`\\b(?:const|let|var)\\s+${escaped}\\b`));
      addDirect(new RegExp(`^\\s*${escaped}\\s*=(?!=)`));
      break;
    case "r":
      addDirect(new RegExp(`^\\s*${escaped}\\s*(?:<-|=)`));
      break;
    case "jl":
      addDirect(new RegExp(`^\\s*function\\s+${escaped}\\b`));
      addDirect(new RegExp(`^\\s*(?:mutable\\s+)?struct\\s+${escaped}\\b`));
      addDirect(new RegExp(`^\\s*${escaped}\\s*=(?!=)`));
      break;
    case "sh":
      addDirect(new RegExp(`^\\s*${escaped}\\s*=`));
      addDirect(new RegExp(`^\\s*(?:function\\s+${escaped}\\b|${escaped}\\s*\\(\\))`));
      break;
  }

  return uniqueRanges(ranges);
}

function pythonImportRanges(line: string, docLine: number, name: string): vscode.Range[] {
  const ranges: vscode.Range[] = [];

  const importMatch = line.match(/^\s*import\s+(.+)$/);
  if (importMatch?.index !== undefined) {
    const list = importMatch[1];
    const listOffset = line.indexOf(list);
    for (const part of list.split(",")) {
      const trimmed = part.trim();
      const alias = trimmed.match(/\bas\s+([A-Za-z_]\w*)$/)?.[1];
      const defined = alias ?? trimmed.split(".", 1)[0].trim();
      const localIndex = alias ? part.lastIndexOf(defined) : part.indexOf(defined);
      if (defined === name) addNameRange(line, docLine, name, listOffset + localIndex, ranges);
    }
  }

  const fromMatch = line.match(/^\s*from\s+\S+\s+import\s+(.+)$/);
  if (fromMatch?.index !== undefined) {
    const list = fromMatch[1];
    const listOffset = line.indexOf(list);
    for (const part of list.split(",")) {
      const trimmed = part.trim();
      const alias = trimmed.match(/\bas\s+([A-Za-z_]\w*)$/)?.[1];
      const defined = alias ?? trimmed.split(/\s+/, 1)[0].trim();
      const localIndex = alias ? part.lastIndexOf(defined) : part.indexOf(defined);
      if (defined === name) addNameRange(line, docLine, name, listOffset + localIndex, ranges);
    }
  }

  return ranges;
}

function addNameRange(
  line: string,
  docLine: number,
  name: string,
  hint: number,
  ranges: vscode.Range[],
): void {
  const start = hint >= 0 ? hint : line.indexOf(name);
  if (start < 0) return;
  ranges.push(new vscode.Range(docLine, start, docLine, start + name.length));
}

function rangeKey(range: vscode.Range): string {
  return `${range.start.line}:${range.start.character}:${range.end.line}:${range.end.character}`;
}

function uniqueRanges(ranges: vscode.Range[]): vscode.Range[] {
  const seen = new Set<string>();
  return ranges.filter((range) => {
    const key = rangeKey(range);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function uniqueLocations(locations: vscode.Location[]): vscode.Location[] {
  const seen = new Set<string>();
  return locations.filter((loc) => {
    const key = `${loc.uri.toString()}:${rangeKey(loc.range)}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function sortDefinitions(
  definitions: vscode.Location[],
  position: vscode.Position,
): vscode.Location[] {
  return [...definitions].sort((a, b) => definitionScore(a, position) - definitionScore(b, position));
}

function definitionScore(location: vscode.Location, position: vscode.Position): number {
  const lineDelta = location.range.start.line - position.line;
  // Prefer definitions above the cursor; among those, prefer the nearest one.
  if (lineDelta <= 0) return Math.abs(lineDelta);
  return 10_000 + lineDelta;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function scanAt(
  document: vscode.TextDocument,
  position: vscode.Position,
): { symbol: SymbolAtPosition; scan: SymbolScan } | null {
  const symbol = symbolAtPosition(document, position);
  if (!symbol) return null;
  return {
    symbol,
    scan: scanSymbol(document, symbol.name, symbol.cell.ratLang),
  };
}

async function runtimeDefinition(
  document: vscode.TextDocument,
  symbol: SymbolAtPosition,
): Promise<vscode.Location | null> {
  const { name } = resolveRuntime(symbol.cell.ratLang, document);
  const client = await existingOrRunningClient(name);
  if (!client) return null;

  try {
    const info = await client.look(symbol.lookupName);
    const source = sourceLocationFromInspection(info);
    if (!source || !fs.existsSync(source.path)) return null;
    return new vscode.Location(
      vscode.Uri.file(source.path),
      new vscode.Position(source.line, 0),
    );
  } catch {
    return null;
  }
}

function sourceLocationFromInspection(info: string): { path: string; line: number } | null {
  for (const line of info.split("\n")) {
    const match = line.match(/\bDefined in:\s+(.+?)\s*$/);
    if (!match) continue;

    const raw = match[1].trim();
    const pathLine = raw.match(/^(.*):(\d+)$/);
    if (pathLine) {
      return {
        path: pathLine[1],
        line: Math.max(0, Number(pathLine[2]) - 1),
      };
    }
    return { path: raw, line: 0 };
  }
  return null;
}

export class RatDefinitionProvider implements vscode.DefinitionProvider, vscode.DeclarationProvider {
  async provideDefinition(
    document: vscode.TextDocument,
    position: vscode.Position,
  ): Promise<vscode.Definition | null> {
    const result = scanAt(document, position);
    if (!result) return null;

    // Best result: use the live runtime's inspect info. This handles imports
    // and classes from installed packages, e.g. Ctrl+Click `dspy.Module`.
    const external = await runtimeDefinition(document, result.symbol);
    if (external) return external;

    // Fallback: document-local definitions. Return only the best candidate so
    // repeated imports do not open a noisy "Definitions (3)" peek panel.
    return sortDefinitions(result.scan.definitions, position)[0] ?? null;
  }

  async provideDeclaration(
    document: vscode.TextDocument,
    position: vscode.Position,
  ): Promise<vscode.Declaration | null> {
    const result = scanAt(document, position);
    if (!result) return null;

    // Declaration is the local binding/import when we can find one.
    const local = sortDefinitions(result.scan.definitions, position)[0];
    if (local) return local;

    return runtimeDefinition(document, result.symbol) as Promise<vscode.Declaration | null>;
  }
}

export class RatReferenceProvider implements vscode.ReferenceProvider {
  provideReferences(
    document: vscode.TextDocument,
    position: vscode.Position,
    context: vscode.ReferenceContext,
  ): vscode.ProviderResult<vscode.Location[]> {
    const result = scanAt(document, position);
    if (!result) return null;
    if (context.includeDeclaration) return result.scan.references;

    const defKeys = new Set(result.scan.definitions.map((loc) => rangeKey(loc.range)));
    return result.scan.references.filter((loc) => !defKeys.has(rangeKey(loc.range)));
  }
}

export class RatDocumentHighlightProvider implements vscode.DocumentHighlightProvider {
  provideDocumentHighlights(
    document: vscode.TextDocument,
    position: vscode.Position,
  ): vscode.ProviderResult<vscode.DocumentHighlight[]> {
    const result = scanAt(document, position);
    if (!result) return null;
    return result.scan.highlights;
  }
}
