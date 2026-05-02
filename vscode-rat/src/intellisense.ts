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

import * as fs from "fs/promises";
import * as os from "os";
import * as path from "path";
import * as vscode from "vscode";
import {
  parseCells,
  cellAtLine,
  codeContext,
  wordAtPosition,
  type CodeCell,
} from "./cells";
import { existingOrRunningClient } from "./rat";
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

const LANG_ID: Record<string, string> = {
  py: "python",
  r: "r",
  jl: "julia",
  js: "javascript",
  sh: "shellscript",
};

const LANG_EXT: Record<string, string> = {
  python: ".py",
  r: ".R",
  julia: ".jl",
  javascript: ".js",
  shellscript: ".sh",
};

const fallbackDocumentUris = new Set<string>();

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

  if (fl.mode !== "notebook") return null;

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

interface ParsedCompletion {
  label: string;
  kindText?: string;
}

interface RuntimeCompletionData {
  runtimeName: string;
  lookup: string;
  kindText?: string;
}

const runtimeCompletionData = new WeakMap<vscode.CompletionItem, RuntimeCompletionData>();

const COMPLETION_KINDS = new Set([
  "attribute",
  "class",
  "constant",
  "directory",
  "file",
  "folder",
  "function",
  "instance",
  "keyword",
  "method",
  "module",
  "package",
  "param",
  "parameter",
  "property",
  "statement",
  "type",
  "value",
  "variable",
]);

function parseCompletionLine(line: string): ParsedCompletion | null {
  const raw = line.trimEnd();
  if (!raw || raw.trim() === "No completions.") return null;

  // Kernel completions are display-formatted as "label<padding>kind", e.g.
  // "np.acos                 instance". VS Code must insert only `label`.
  const match =
    raw.match(/^(.*?)\t+([A-Za-z][\w-]*)$/) ??
    raw.match(/^(.*?)\s{2,}([A-Za-z][\w-]*)$/) ??
    raw.match(/^(.*\S)\s+([A-Za-z][\w-]*)$/);

  if (match) {
    const maybeKind = match[2].toLowerCase();
    if (COMPLETION_KINDS.has(maybeKind)) {
      const label = match[1].trimEnd();
      return label ? { label, kindText: maybeKind } : null;
    }
  }

  return { label: raw.trim() };
}

function completionKind(kindText: string | undefined, label: string): vscode.CompletionItemKind {
  switch (kindText) {
    case "function":
      return vscode.CompletionItemKind.Function;
    case "method":
      return vscode.CompletionItemKind.Method;
    case "module":
    case "package":
      return vscode.CompletionItemKind.Module;
    case "class":
    case "type":
      return vscode.CompletionItemKind.Class;
    case "property":
    case "attribute":
      return vscode.CompletionItemKind.Property;
    case "keyword":
    case "statement":
      return vscode.CompletionItemKind.Keyword;
    case "constant":
      return vscode.CompletionItemKind.Constant;
    case "file":
      return vscode.CompletionItemKind.File;
    case "folder":
    case "directory":
      return vscode.CompletionItemKind.Folder;
  }

  if (label.endsWith("(")) return vscode.CompletionItemKind.Function;
  if (label.endsWith("=")) return vscode.CompletionItemKind.Property;
  if (label === label.toUpperCase() && label.length > 1) {
    return vscode.CompletionItemKind.Constant;
  }
  return vscode.CompletionItemKind.Variable;
}

function completionReplaceRange(
  document: vscode.TextDocument,
  position: vscode.Position,
  insertText: string,
): vscode.Range {
  const line = document.lineAt(position.line).text;
  const col = Math.min(position.character, line.length);
  let start = col;
  while (start > 0 && /[^\s|&;(){}\[\]<>'",]/.test(line[start - 1])) start--;

  const prefix = line.slice(start, col);
  let replaceStart = start;

  // Some kernels return full dotted names ("np.acos"), others return only the
  // attribute suffix ("acos"). Replace the dotted prefix only when the insert
  // text includes it; otherwise replace just the part after the final dot.
  if (prefix.includes(".") && !insertText.startsWith(prefix)) {
    replaceStart = start + prefix.lastIndexOf(".") + 1;
  }

  return new vscode.Range(position.line, replaceStart, position.line, col);
}

interface FallbackContext {
  languageId: string;
  code: string;
  position: vscode.Position;
}

function fallbackContext(
  document: vscode.TextDocument,
  position: vscode.Position,
  ctx: CodeCtx,
): FallbackContext | null {
  const languageId = LANG_ID[ctx.ratLang];
  if (!languageId) return null;

  if (ctx.kind === "source") {
    return {
      languageId,
      code: document.getText(),
      position,
    };
  }

  const cells = parseCells(document).filter(
    (cell) => cell.executable &&
      cell.ratLang === ctx.ratLang &&
      cell.openLine <= ctx.cell.openLine,
  );

  const lines: string[] = [];
  let targetLineOffset = 0;
  for (const cell of cells) {
    if (cell.openLine === ctx.cell.openLine) {
      targetLineOffset = lines.length;
      lines.push(...cell.code.split("\n"));
      break;
    }

    lines.push(...cell.code.split("\n"));
    lines.push("");
  }

  const lineInCell = position.line - (ctx.cell.openLine + 1);
  return {
    languageId,
    code: lines.join("\n"),
    position: new vscode.Position(
      targetLineOffset + Math.max(0, lineInCell),
      position.character,
    ),
  };
}

async function lspFallbackCompletions(
  document: vscode.TextDocument,
  position: vscode.Position,
  ctx: CodeCtx,
  token: vscode.CancellationToken,
  triggerCharacter?: string,
): Promise<vscode.CompletionItem[] | null> {
  const fallback = fallbackContext(document, position, ctx);
  if (!fallback) return null;

  const fallbackDoc = await openFallbackDocument(fallback);
  if (!fallbackDoc) return null;

  try {
    const result = await vscode.commands.executeCommand<vscode.CompletionList>(
      "vscode.executeCompletionItemProvider",
      fallbackDoc.document.uri,
      fallback.position,
      triggerCharacter,
    );
    if (token.isCancellationRequested || !result) return null;

    const sourceItems = Array.isArray(result) ? result : result.items;
    return sourceItems
      .map((item) => cloneFallbackCompletion(item, document, position))
      .filter((item): item is vscode.CompletionItem => item !== null);
  } catch {
    return null;
  } finally {
    await fallbackDoc.dispose();
  }
}

async function openFallbackDocument(
  fallback: FallbackContext,
): Promise<{ document: vscode.TextDocument; dispose: () => Promise<void> } | null> {
  const ext = LANG_EXT[fallback.languageId] ?? ".txt";
  const dir = path.join(os.tmpdir(), "rat-vscode-lsp-fallback");
  const file = path.join(
    dir,
    `fallback-${process.pid}-${Date.now()}-${Math.random().toString(36).slice(2)}${ext}`,
  );
  const uri = vscode.Uri.file(file);

  try {
    await fs.mkdir(dir, { recursive: true });
    await fs.writeFile(file, fallback.code, "utf8");

    let document = await vscode.workspace.openTextDocument(uri);
    if (document.languageId !== fallback.languageId) {
      document = await vscode.languages.setTextDocumentLanguage(document, fallback.languageId);
    }

    fallbackDocumentUris.add(document.uri.toString());
    return {
      document,
      dispose: async () => {
        fallbackDocumentUris.delete(document.uri.toString());
        try {
          await fs.unlink(file);
        } catch {
          // Best effort cleanup.
        }
      },
    };
  } catch {
    try {
      await fs.unlink(file);
    } catch {
      // ignore
    }
    return null;
  }
}

function cloneFallbackCompletion(
  item: vscode.CompletionItem,
  document: vscode.TextDocument,
  position: vscode.Position,
): vscode.CompletionItem | null {
  const label = completionLabelText(item.label);
  if (!label) return null;

  const cloned = new vscode.CompletionItem(item.label, item.kind);
  cloned.detail = item.detail;
  cloned.documentation = item.documentation;
  cloned.filterText = item.filterText;
  cloned.sortText = item.sortText;
  cloned.preselect = item.preselect;
  cloned.commitCharacters = item.commitCharacters;

  const insertText = item.insertText ?? item.textEdit?.newText ?? label;
  cloned.insertText = insertText;
  cloned.range = completionReplaceRange(
    document,
    position,
    typeof insertText === "string" ? insertText : label,
  );

  return cloned;
}

function completionLabelText(label: string | vscode.CompletionItemLabel): string {
  return typeof label === "string" ? label : label.label;
}

function completionLookupExpression(
  document: vscode.TextDocument,
  position: vscode.Position,
  insertText: string,
): string {
  const line = document.lineAt(position.line).text;
  const col = Math.min(position.character, line.length);
  let start = col;
  while (start > 0 && /[^\s|&;(){}\[\]<>'",]/.test(line[start - 1])) start--;

  const prefix = line.slice(start, col);
  const cleanInsert = insertText.replace(/\($/, "");
  if (!prefix.includes(".")) return cleanInsert;
  if (cleanInsert.startsWith(prefix)) return cleanInsert;

  const base = prefix.slice(0, prefix.lastIndexOf(".") + 1);
  return base + cleanInsert;
}

export class RatCompletionProvider
  implements vscode.CompletionItemProvider
{
  async provideCompletionItems(
    document: vscode.TextDocument,
    position: vscode.Position,
    token: vscode.CancellationToken,
    context: vscode.CompletionContext,
  ): Promise<vscode.CompletionItem[] | null> {
    if (fallbackDocumentUris.has(document.uri.toString())) return null;

    const ctx = getContext(document, position);
    if (!ctx) return null;

    const runtimeItems = await runtimeCompletions(document, position, ctx, token);
    if (runtimeItems && runtimeItems.length > 0) return runtimeItems;

    return lspFallbackCompletions(
      document,
      position,
      ctx,
      token,
      context.triggerCharacter,
    );
  }

  async resolveCompletionItem(
    item: vscode.CompletionItem,
    token: vscode.CancellationToken,
  ): Promise<vscode.CompletionItem> {
    const data = runtimeCompletionData.get(item);
    if (!data) return item;

    const client = await existingOrRunningClient(data.runtimeName);
    if (!client || token.isCancellationRequested) return item;

    try {
      const info = await client.look(data.lookup);
      if (token.isCancellationRequested || !info || info.endsWith(": not found")) {
        return item;
      }

      const signature = signatureFromInspection(info, data.lookup);
      if (signature) {
        item.detail = signature;
      } else if (data.kindText) {
        item.detail = `rat · ${data.kindText}`;
      }

      const md = new vscode.MarkdownString();
      const source = sourceLocationFromInspection(info);
      if (source) {
        md.appendMarkdown(`[Open definition](${definitionCommandLink(source)})\n\n`);
      }
      md.appendCodeblock(info, "text");
      md.isTrusted = true;
      item.documentation = md;
    } catch {
      // Keep the base completion item.
    }

    return item;
  }
}

async function runtimeCompletions(
  document: vscode.TextDocument,
  position: vscode.Position,
  ctx: CodeCtx,
  token: vscode.CancellationToken,
): Promise<vscode.CompletionItem[] | null> {
  const { name } = resolveRuntime(ctx.ratLang, document);
  const client = await existingOrRunningClient(name);
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

    const parsed = items
      .map(parseCompletionLine)
      .filter((item): item is ParsedCompletion => item !== null);

    if (parsed.length === 0) return null;

    return parsed.map(({ label, kindText }) => {
      let insertText = label;
      if (insertText.endsWith("(")) {
        insertText = insertText.slice(0, -1);
      }

      const ci = new vscode.CompletionItem(
        label,
        completionKind(kindText, label),
      );
      ci.insertText = insertText;
      ci.range = completionReplaceRange(document, position, insertText);
      ci.detail = kindText ? `rat · ${kindText}` : "rat";
      runtimeCompletionData.set(ci, {
        runtimeName: name,
        lookup: completionLookupExpression(document, position, insertText),
        kindText,
      });
      return ci;
    });
  } catch {
    return null;
  }
}

// ── Signature help ─────────────────────────────────────────

export class RatSignatureHelpProvider implements vscode.SignatureHelpProvider {
  async provideSignatureHelp(
    document: vscode.TextDocument,
    position: vscode.Position,
    token: vscode.CancellationToken,
    context: vscode.SignatureHelpContext,
  ): Promise<vscode.SignatureHelp | null> {
    if (fallbackDocumentUris.has(document.uri.toString())) return null;

    const ctx = getContext(document, position);
    if (!ctx) return null;

    const fallback = await lspFallbackSignatureHelp(
      document,
      position,
      ctx,
      token,
      context.triggerCharacter,
    );
    if (fallback?.signatures.length) return fallback;

    return runtimeSignatureHelp(document, position, ctx, token);
  }
}

async function lspFallbackSignatureHelp(
  document: vscode.TextDocument,
  position: vscode.Position,
  ctx: CodeCtx,
  token: vscode.CancellationToken,
  triggerCharacter?: string,
): Promise<vscode.SignatureHelp | null> {
  const fallback = fallbackContext(document, position, ctx);
  if (!fallback) return null;

  const fallbackDoc = await openFallbackDocument(fallback);
  if (!fallbackDoc) return null;

  try {
    const result = await vscode.commands.executeCommand<vscode.SignatureHelp>(
      "vscode.executeSignatureHelpProvider",
      fallbackDoc.document.uri,
      fallback.position,
      triggerCharacter,
    );
    if (token.isCancellationRequested || !result?.signatures.length) return null;
    return result;
  } catch {
    return null;
  } finally {
    await fallbackDoc.dispose();
  }
}

async function runtimeSignatureHelp(
  document: vscode.TextDocument,
  position: vscode.Position,
  ctx: CodeCtx,
  token: vscode.CancellationToken,
): Promise<vscode.SignatureHelp | null> {
  const call = callAtPosition(document, position, ctx);
  if (!call) return null;

  const { name } = resolveRuntime(ctx.ratLang, document);
  const client = await existingOrRunningClient(name);
  if (!client) return null;

  try {
    const info = await client.look(call.callee);
    if (token.isCancellationRequested || !info || info.endsWith(": not found")) return null;

    const signature = signatureFromInspection(info, call.callee);
    if (!signature) return null;

    const help = new vscode.SignatureHelp();
    const sig = new vscode.SignatureInformation(signature, new vscode.MarkdownString().appendCodeblock(info, "text"));
    sig.parameters = signatureParameters(signature).map((param) => new vscode.ParameterInformation(param));
    help.signatures = [sig];
    help.activeSignature = 0;
    help.activeParameter = Math.min(call.activeParameter, Math.max(0, sig.parameters.length - 1));
    return help;
  } catch {
    return null;
  }
}

function callAtPosition(
  document: vscode.TextDocument,
  position: vscode.Position,
  ctx: CodeCtx,
): { callee: string; activeParameter: number } | null {
  let code: string;
  let cursor: number;
  if (ctx.kind === "cell") {
    const cc = codeContext(ctx.cell, position);
    code = cc.code;
    cursor = cc.cursor;
  } else {
    code = document.getText(new vscode.Range(0, 0, position.line, position.character));
    cursor = code.length;
  }

  const prefix = code.slice(0, cursor);
  const openParen = findCallOpenParen(prefix);
  if (openParen < 0) return null;

  let end = openParen;
  while (end > 0 && /\s/.test(prefix[end - 1])) end--;
  let start = end;
  while (start > 0 && /[A-Za-z0-9_.$]/.test(prefix[start - 1])) start--;

  const callee = prefix.slice(start, end).replace(/^\.+|\.+$/g, "");
  if (!callee || /^\d/.test(callee)) return null;

  return {
    callee,
    activeParameter: activeParameterIndex(prefix.slice(openParen + 1)),
  };
}

function findCallOpenParen(prefix: string): number {
  let depth = 0;
  let quote: string | null = null;
  for (let i = prefix.length - 1; i >= 0; i--) {
    const ch = prefix[i];
    if (quote) {
      if (ch === quote && prefix[i - 1] !== "\\") quote = null;
      continue;
    }
    if (ch === "'" || ch === '"') {
      quote = ch;
      continue;
    }
    if (ch === ")" || ch === "]" || ch === "}") depth++;
    else if (ch === "(" && depth === 0) return i;
    else if (ch === "(" || ch === "[" || ch === "{") depth = Math.max(0, depth - 1);
  }
  return -1;
}

function activeParameterIndex(argsPrefix: string): number {
  let depth = 0;
  let count = 0;
  let quote: string | null = null;
  for (let i = 0; i < argsPrefix.length; i++) {
    const ch = argsPrefix[i];
    if (quote) {
      if (ch === quote && argsPrefix[i - 1] !== "\\") quote = null;
      continue;
    }
    if (ch === "'" || ch === '"') {
      quote = ch;
      continue;
    }
    if (ch === "(" || ch === "[" || ch === "{") depth++;
    else if (ch === ")" || ch === "]" || ch === "}") depth = Math.max(0, depth - 1);
    else if (ch === "," && depth === 0) count++;
  }
  return count;
}

function signatureFromInspection(info: string, fallbackName: string): string | null {
  for (const line of info.split("\n")) {
    const trimmed = line.trim();
    if (/^\([^)]*\)/.test(trimmed)) return `${fallbackName}${trimmed}`;
    if (/^[\w.]+\s*\(.+\)/.test(trimmed)) return trimmed;
  }
  return null;
}

function signatureParameters(signature: string): string[] {
  const open = signature.indexOf("(");
  const close = signature.lastIndexOf(")");
  if (open < 0 || close <= open) return [];
  return splitTopLevel(signature.slice(open + 1, close));
}

function splitTopLevel(text: string): string[] {
  const parts: string[] = [];
  let start = 0;
  let depth = 0;
  let quote: string | null = null;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (quote) {
      if (ch === quote && text[i - 1] !== "\\") quote = null;
      continue;
    }
    if (ch === "'" || ch === '"') {
      quote = ch;
      continue;
    }
    if (ch === "(" || ch === "[" || ch === "{") depth++;
    else if (ch === ")" || ch === "]" || ch === "}") depth = Math.max(0, depth - 1);
    else if (ch === "," && depth === 0) {
      const part = text.slice(start, i).trim();
      if (part) parts.push(part);
      start = i + 1;
    }
  }
  const last = text.slice(start).trim();
  if (last) parts.push(last);
  return parts;
}

// ── Hover ──────────────────────────────────────────────────

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

function definitionCommandLink(source: { path: string; line: number }): string {
  const pos = new vscode.Position(source.line, 0);
  const args = encodeURIComponent(JSON.stringify([
    vscode.Uri.file(source.path),
    { selection: new vscode.Range(pos, pos) },
  ]));
  return `command:vscode.open?${args}`;
}

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
    const client = await existingOrRunningClient(name);
    if (!client) return null;

    try {
      const info = await client.look(word);
      if (token.isCancellationRequested || !info) return null;

      const langId = LANG_HIGHLIGHT[ctx.ratLang] ?? ctx.ratLang;
      const md = new vscode.MarkdownString();
      const source = sourceLocationFromInspection(info);
      if (source) {
        md.appendMarkdown(`[Open definition](${definitionCommandLink(source)})\n\n`);
      }
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
