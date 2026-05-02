/**
 * blocks.ts — Detect "logical blocks" in source files using tree-sitter.
 *
 * A "logical block" is the top-level syntax node containing the cursor.
 * For Python that's a function def, class def, if/for/while block, or
 * a standalone expression/assignment.  Same idea for R, Julia, JS, shell.
 *
 * Uses web-tree-sitter with prebuilt .wasm grammars shipped in grammars/.
 */

import * as path from "path";
import * as vscode from "vscode";
import { grammarWasmForRatLang } from "./languages";

// web-tree-sitter ships as CJS — import the namespace
// eslint-disable-next-line @typescript-eslint/no-require-imports
const TreeSitter = require("web-tree-sitter");

// ── Types ──────────────────────────────────────────────────

export interface SourceBlock {
  /** The code text to send to the REPL */
  code: string;
  /** Range in the document (for decorations / cursor advance) */
  range: vscode.Range;
}

// ── Singleton init ─────────────────────────────────────────

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type TSParser = any;
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type TSLanguage = any;
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type TSTree = any;
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type SyntaxNode = any;

let parserReady: Promise<void> | null = null;
let ParserClass: TSParser | null = null;

const languages = new Map<string, TSLanguage>();
const parsers = new Map<string, TSParser>();

const GRAMMAR_DIR = path.join(__dirname, "..", "grammars");

async function ensureInit(): Promise<void> {
  if (!parserReady) {
    parserReady = TreeSitter.Parser.init({
      locateFile: () => path.join(GRAMMAR_DIR, "web-tree-sitter.wasm"),
    }).then(() => {
      ParserClass = TreeSitter.Parser;
    });
  }
  await parserReady;
}

async function getParser(ratLang: string): Promise<TSParser | null> {
  await ensureInit();
  if (!ParserClass) return null;

  const wasmFile = grammarWasmForRatLang(ratLang);
  if (!wasmFile) return null;

  if (!parsers.has(ratLang)) {
    const lang = await TreeSitter.Language.load(
      path.join(GRAMMAR_DIR, wasmFile),
    );
    languages.set(ratLang, lang);
    const p = new ParserClass();
    p.setLanguage(lang);
    parsers.set(ratLang, p);
  }
  return parsers.get(ratLang)!;
}

// ── Block detection ────────────────────────────────────────

/**
 * Find the top-level statement/block containing the cursor line.
 * Returns null if the cursor is on a blank/comment-only line with
 * no enclosing statement.
 */
export async function blockAtCursor(
  document: vscode.TextDocument,
  line: number,
  ratLang: string,
): Promise<SourceBlock | null> {
  const parser = await getParser(ratLang);
  if (!parser) return null;

  const tree: TSTree = parser.parse(document.getText());
  const root = tree.rootNode;

  // Walk up from cursor to the top-level enclosing node.
  // This ensures that pressing Shift+Enter inside a class method
  // sends the entire class, not just the method line.
  const node = findEnclosingBlock(root, line);
  if (!node) return null;

  return nodeToBlock(node, document);
}

/**
 * Find the next logical block after the given line.
 * Used for "advance" after executing a block.
 */
export async function nextBlock(
  document: vscode.TextDocument,
  afterLine: number,
  ratLang: string,
): Promise<SourceBlock | null> {
  const parser = await getParser(ratLang);
  if (!parser) return null;

  const tree: TSTree = parser.parse(document.getText());
  const root = tree.rootNode;

  for (let i = 0; i < root.childCount; i++) {
    const child = root.child(i)!;
    if (child.startPosition.row > afterLine && isSignificant(child)) {
      return nodeToBlock(child, document);
    }
  }
  return null;
}

/**
 * Get all top-level blocks in the document.
 */
export async function allBlocks(
  document: vscode.TextDocument,
  ratLang: string,
): Promise<SourceBlock[]> {
  const parser = await getParser(ratLang);
  if (!parser) return [];

  const tree: TSTree = parser.parse(document.getText());
  const root = tree.rootNode;
  const blocks: SourceBlock[] = [];

  for (let i = 0; i < root.childCount; i++) {
    const child = root.child(i)!;
    if (isSignificant(child)) {
      blocks.push(nodeToBlock(child, document));
    }
  }
  return blocks;
}

// ── Helpers ────────────────────────────────────────────────

function topLevelNodeAtLine(root: SyntaxNode, line: number): SyntaxNode | null {
  // Walk top-level children to find the one spanning `line`
  for (let i = 0; i < root.childCount; i++) {
    const child = root.child(i)!;
    if (child.startPosition.row <= line && child.endPosition.row >= line) {
      if (isSignificant(child)) return child;
    }
  }

  // Cursor is on a blank line between blocks.
  // Look for the nearest significant node below, then above.
  for (let i = 0; i < root.childCount; i++) {
    const child = root.child(i)!;
    if (child.startPosition.row > line && isSignificant(child)) {
      return child;
    }
  }
  // Nothing below — try the last significant node above
  for (let i = root.childCount - 1; i >= 0; i--) {
    const child = root.child(i)!;
    if (child.endPosition.row < line && isSignificant(child)) {
      return child;
    }
  }
  return null;
}

/**
 * Given a cursor line inside a document, find the deepest node at that
 * line and walk up to find the nearest "interesting" enclosing scope:
 * a method inside a class, or the top-level statement.
 *
 * Returns the innermost function/method node if inside a class, so the
 * user can re-send just that method.  The class is still needed as
 * context, so we return the class node in that case.
 *
 * For non-class contexts (module-level code), returns the top-level node.
 */
function findEnclosingBlock(root: SyntaxNode, line: number): SyntaxNode | null {
  // Find deepest node at this line
  let node = root.descendantForPosition({ row: line, column: 0 });
  if (!node) return topLevelNodeAtLine(root, line);

  // Walk up to find the top-level node.
  // NOTE: tree-sitter creates new JS wrapper objects on each .parent
  // access, so `node.parent !== root` always returns true.  Compare
  // by node id instead.
  while (node.parent && node.parent.id !== root.id) {
    node = node.parent;
  }

  if (!node || !isSignificant(node)) {
    return topLevelNodeAtLine(root, line);
  }
  return node;
}

/** Skip comment nodes and whitespace-only nodes. */
function isSignificant(node: SyntaxNode): boolean {
  if (!node) return false;
  const t = node.type;
  return t !== "comment" && t !== "line_comment" && t !== "block_comment" && t !== "\n";
}

function nodeToBlock(node: SyntaxNode, document: vscode.TextDocument): SourceBlock {
  const startLine = node.startPosition.row;
  const endLine = node.endPosition.row;
  const range = new vscode.Range(
    startLine,
    0,
    endLine,
    document.lineAt(Math.min(endLine, document.lineCount - 1)).text.length,
  );
  return {
    code: node.text,
    range,
  };
}
