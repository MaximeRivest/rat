/**
 * langDetect.ts — Detect rat language from file extension or VS Code languageId.
 *
 * Two modes:
 *   - "notebook" — markdown / quarto / rmd files with fenced code cells
 *   - "source"   — plain .py / .r / .jl / .js / .sh files sent to REPL
 */

import * as path from "path";
import type * as vscode from "vscode";

export type FileMode = "notebook" | "source";

export interface FileLang {
  mode: FileMode;
  /** Canonical rat language — null for notebook mode (determined per-cell) */
  ratLang: string | null;
}

// ── Extension → ratLang ────────────────────────────────────

const EXT_MAP: Record<string, string> = {
  ".py": "py",
  ".pyw": "py",
  ".r": "r",
  ".R": "r",
  ".jl": "jl",
  ".js": "js",
  ".mjs": "js",
  ".sh": "sh",
  ".bash": "sh",
  ".zsh": "sh",
};

const NOTEBOOK_EXTS = new Set([".md", ".qmd", ".rmd"]);

// VS Code languageId → ratLang (for files without standard extensions)
const LANGID_MAP: Record<string, string> = {
  python: "py",
  r: "r",
  julia: "jl",
  javascript: "js",
  shellscript: "sh",
  bash: "sh",
};

const NOTEBOOK_LANGIDS = new Set(["markdown", "quarto", "rmarkdown"]);

// ── Public API ─────────────────────────────────────────────

export function detectFileLang(doc: vscode.TextDocument): FileLang {
  const ext = path.extname(doc.fileName).toLowerCase();

  // Notebook mode
  if (NOTEBOOK_EXTS.has(ext) || NOTEBOOK_LANGIDS.has(doc.languageId)) {
    return { mode: "notebook", ratLang: null };
  }

  // Source mode — try extension first (case-sensitive for .R)
  const byExt = EXT_MAP[path.extname(doc.fileName)] ?? EXT_MAP[ext];
  if (byExt) return { mode: "source", ratLang: byExt };

  const byLang = LANGID_MAP[doc.languageId];
  if (byLang) return { mode: "source", ratLang: byLang };

  return { mode: "notebook", ratLang: null }; // fallback
}

/** Is this document something rat can work with? */
export function isRatFile(doc: vscode.TextDocument): boolean {
  const { mode, ratLang } = detectFileLang(doc);
  return mode === "notebook" || ratLang !== null;
}
