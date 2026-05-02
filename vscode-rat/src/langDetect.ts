/**
 * langDetect.ts — Detect rat language from file extension or VS Code languageId.
 *
 * Modes:
 *   - "notebook"    — markdown / quarto / rmd files with fenced code cells
 *   - "source"      — plain .py / .r / .jl / .js / .sh files sent to REPL
 *   - "unsupported" — any other file
 */

import * as path from "path";
import type * as vscode from "vscode";
import {
  NOTEBOOK_EXTENSIONS,
  NOTEBOOK_LANGUAGE_IDS,
  ratLangForSourceExtension,
  ratLangForVscodeLanguageId,
} from "./languages";

export type FileMode = "notebook" | "source" | "unsupported";

export interface FileLang {
  mode: FileMode;
  /** Canonical rat language — null for notebook/unsupported modes */
  ratLang: string | null;
}

const NOTEBOOK_EXTS = new Set<string>(NOTEBOOK_EXTENSIONS);
const NOTEBOOK_LANGIDS = new Set<string>(NOTEBOOK_LANGUAGE_IDS);

// ── Public API ─────────────────────────────────────────────

export function detectFileLang(doc: vscode.TextDocument): FileLang {
  const ext = path.extname(doc.fileName).toLowerCase();

  // Notebook mode
  if (NOTEBOOK_EXTS.has(ext) || NOTEBOOK_LANGIDS.has(doc.languageId)) {
    return { mode: "notebook", ratLang: null };
  }

  // Source mode — try extension first (case-sensitive for .R)
  const byExt = ratLangForSourceExtension(path.extname(doc.fileName))
    ?? ratLangForSourceExtension(ext);
  if (byExt) return { mode: "source", ratLang: byExt };

  const byLang = ratLangForVscodeLanguageId(doc.languageId);
  if (byLang) return { mode: "source", ratLang: byLang };

  return { mode: "unsupported", ratLang: null };
}

/** Is this document something rat can work with? */
export function isRatFile(doc: vscode.TextDocument): boolean {
  const { mode, ratLang } = detectFileLang(doc);
  return mode === "notebook" || (mode === "source" && ratLang !== null);
}
