/**
 * resolve.ts — Decide which rat runtime name + cwd to use.
 *
 * Resolution order:
 *   0. Manual override      (user picked via Quick Pick / status bar)
 *   1. YAML front-matter    `rat: { python: py-ml }`
 *   2. VS Code setting      `"rat.runtimes": { "python": "py-ml" }`
 *   3. State-aware match    find kernel/runtime matching (lang, project)
 *   4. Default               canonical lang name ("py", "sh", …)
 *                            or project-qualified ("py@proj") if bare is taken
 *
 * CWD is always the VS Code workspace folder that contains the file.
 */

import * as vscode from "vscode";
import * as path from "path";
import { readState } from "./rat";

const ALIAS: Record<string, string> = {
  python: "py", py: "py", python3: "py",
  r: "r",       R: "r",
  bash: "sh",   sh: "sh", shell: "sh",
  julia: "jl",  jl: "jl", ju: "jl",
  javascript: "js", js: "js", node: "js",
  pi: "pi",
  slack: "slack",
};

export interface RuntimeInfo {
  /** Name passed to `rat start` / `rat run` (e.g. "py" or "py@myproject") */
  name: string;
  /** Working directory for the kernel */
  cwd: string;
  /** Canonical rat language (e.g. "py", "sh", "r") */
  lang: string;
}

// ── Manual override (per-document, per-language) ───────────

// Map<documentUri, Map<ratLang, runtimeName>>
const overrides = new Map<string, Map<string, string>>();

/** Set a manual runtime override for a document + language. */
export function setRuntimeOverride(
  documentUri: string,
  ratLang: string,
  runtimeName: string,
): void {
  if (!overrides.has(documentUri)) overrides.set(documentUri, new Map());
  overrides.get(documentUri)!.set(ratLang, runtimeName);
}

/** Clear override for a document + language. */
export function clearRuntimeOverride(
  documentUri: string,
  ratLang: string,
): void {
  overrides.get(documentUri)?.delete(ratLang);
}

/** Get the current override (if any). */
export function getRuntimeOverride(
  documentUri: string,
  ratLang: string,
): string | undefined {
  return overrides.get(documentUri)?.get(ratLang);
}

// ── Main resolver ──────────────────────────────────────────

export function resolveRuntime(
  ratLang: string,
  document: vscode.TextDocument,
): RuntimeInfo {
  const cwd = workspaceCwd(document);

  // 0. Manual override (user picked via Quick Pick)
  const ov = overrides.get(document.uri.toString())?.get(ratLang);
  if (ov) return { name: ov, cwd, lang: ratLang };

  // 1. Front-matter
  const fm = frontMatterRuntime(document, ratLang);
  if (fm) return { name: fm, cwd, lang: ratLang };

  // 2. VS Code setting
  const runtimes = vscode.workspace
    .getConfiguration("rat")
    .get<Record<string, string>>("runtimes", {});

  for (const [key, value] of Object.entries(runtimes)) {
    if (key === ratLang || ALIAS[key] === ratLang) {
      return { name: value, cwd, lang: ratLang };
    }
  }

  // 3. State-aware: find an existing kernel/runtime for this project
  const state = readState();

  for (const k of state.kernels) {
    if (k.lang === ratLang && isSameProject(k.cwd, cwd)) {
      return { name: k.name, cwd, lang: ratLang };
    }
  }

  for (const r of state.runtimes) {
    if (r.lang === ratLang && isSameProject(r.cwd, cwd)) {
      return { name: r.name, cwd, lang: ratLang };
    }
  }

  // 4. No match — determine name.
  // If the bare lang name is taken by a different project, use a
  // project-qualified name (matches the CLI's `rat py` behaviour).
  const bareTaken = state.kernels.some(
    (k) => k.name === ratLang && !isSameProject(k.cwd, cwd),
  );
  const name = bareTaken
    ? `${ratLang}@${path.basename(cwd)}`
    : ratLang;

  return { name, cwd, lang: ratLang };
}

// ── helpers ────────────────────────────────────────────────

function workspaceCwd(document: vscode.TextDocument): string {
  const ws = vscode.workspace.getWorkspaceFolder(document.uri);
  return ws ? ws.uri.fsPath : path.dirname(document.uri.fsPath);
}

function isSameProject(cwd1: string, cwd2: string): boolean {
  return path.resolve(cwd1) === path.resolve(cwd2);
}

function frontMatterRuntime(
  document: vscode.TextDocument,
  ratLang: string,
): string | null {
  const text = document.getText();
  const fmMatch = text.match(/^---\r?\n([\s\S]*?)\r?\n---/);
  if (!fmMatch) return null;

  // Look for a `rat:` block
  const ratBlock = fmMatch[1].match(/^rat:\s*\r?\n((?:\s+.+\r?\n?)*)/m);
  if (!ratBlock) return null;

  for (const m of ratBlock[1].matchAll(/^\s+(\w+):\s*(\S+)/gm)) {
    const lang = m[1];
    const name = m[2];
    if (lang === ratLang || ALIAS[lang] === ratLang) return name;
  }
  return null;
}
