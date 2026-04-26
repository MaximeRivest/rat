/**
 * resolve.ts — Decide which rat runtime name + cwd to use.
 *
 * Adds a first-class scope model for the current document:
 *   - notebook — this file gets its own runtime
 *   - project  — files in the workspace share one runtime (default)
 *   - global   — one runtime shared across projects for the user
 *
 * Resolution order:
 *   0. Manual runtime override (exact runtime name)
 *   1. YAML front-matter runtime binding
 *   2. VS Code setting `rat.runtimes`
 *   3. Explicit scope preference (notebook/project/global)
 *   4. Default scope = project
 */

import * as os from "os";
import * as path from "path";
import * as vscode from "vscode";
import { readState } from "./rat";

const ALIAS: Record<string, string> = {
  python: "py", py: "py", python3: "py",
  r: "r", R: "r",
  bash: "sh", sh: "sh", shell: "sh",
  julia: "jl", jl: "jl", ju: "jl",
  javascript: "js", js: "js", node: "js",
  pi: "pi",
  slack: "slack",
};

export type RuntimeScope = "notebook" | "project" | "global";

export interface RuntimeInfo {
  /** Name passed to `rat start` / `rat run` / `rat <name>` */
  name: string;
  /** Working directory for the runtime */
  cwd: string;
  /** Canonical rat language */
  lang: string;
  /** Semantic scope for the current document */
  scope: RuntimeScope;
}

let stateStore: vscode.Memento | null = null;

// Map<`${documentUri}::${ratLang}`, runtimeName>
const runtimeOverrides = new Map<string, string>();
// Map<`${documentUri}::${ratLang}`, scope>
const scopeOverrides = new Map<string, RuntimeScope>();

const RUNTIME_OVERRIDES_KEY = "rat.runtimeOverrides";
const SCOPE_OVERRIDES_KEY = "rat.scopeOverrides";

export function initRuntimeState(ctx: vscode.ExtensionContext): void {
  stateStore = ctx.workspaceState;

  const savedRuntimeOverrides =
    ctx.workspaceState.get<Record<string, string>>(RUNTIME_OVERRIDES_KEY, {});
  runtimeOverrides.clear();
  for (const [k, v] of Object.entries(savedRuntimeOverrides)) {
    runtimeOverrides.set(k, v);
  }

  const savedScopeOverrides =
    ctx.workspaceState.get<Record<string, RuntimeScope>>(SCOPE_OVERRIDES_KEY, {});
  scopeOverrides.clear();
  for (const [k, v] of Object.entries(savedScopeOverrides)) {
    if (v === "notebook" || v === "project" || v === "global") {
      scopeOverrides.set(k, v);
    }
  }
}

function docKey(documentUri: string, ratLang: string): string {
  return `${documentUri}::${ratLang}`;
}

function persistMaps(): void {
  if (!stateStore) return;
  const runtimeObj: Record<string, string> = {};
  for (const [k, v] of runtimeOverrides) runtimeObj[k] = v;
  const scopeObj: Record<string, RuntimeScope> = {};
  for (const [k, v] of scopeOverrides) scopeObj[k] = v;
  void stateStore.update(RUNTIME_OVERRIDES_KEY, runtimeObj);
  void stateStore.update(SCOPE_OVERRIDES_KEY, scopeObj);
}

export function setRuntimeOverride(
  documentUri: string,
  ratLang: string,
  runtimeName: string,
): void {
  runtimeOverrides.set(docKey(documentUri, ratLang), runtimeName);
  persistMaps();
}

export function clearRuntimeOverride(
  documentUri: string,
  ratLang: string,
): void {
  runtimeOverrides.delete(docKey(documentUri, ratLang));
  persistMaps();
}

export function getRuntimeOverride(
  documentUri: string,
  ratLang: string,
): string | undefined {
  return runtimeOverrides.get(docKey(documentUri, ratLang));
}

export function setScopeOverride(
  documentUri: string,
  ratLang: string,
  scope: RuntimeScope,
): void {
  scopeOverrides.set(docKey(documentUri, ratLang), scope);
  persistMaps();
}

export function getScopeOverride(
  documentUri: string,
  ratLang: string,
): RuntimeScope | undefined {
  return scopeOverrides.get(docKey(documentUri, ratLang));
}

export function clearScopeOverride(
  documentUri: string,
  ratLang: string,
): void {
  scopeOverrides.delete(docKey(documentUri, ratLang));
  persistMaps();
}

export function scopeLabel(scope: RuntimeScope): string {
  switch (scope) {
    case "notebook":
      return "Notebook";
    case "global":
      return "Global";
    default:
      return "Project";
  }
}

export function resolveRuntime(
  ratLang: string,
  document: vscode.TextDocument,
): RuntimeInfo {
  const docUri = document.uri.toString();
  const cwd = workspaceCwd(document);

  // 0. Manual runtime override (exact runtime name chosen by user)
  const override = getRuntimeOverride(docUri, ratLang);
  if (override) {
    return {
      name: override,
      cwd: inferRuntimeCwd(override, cwd),
      lang: ratLang,
      scope: getScopeOverride(docUri, ratLang) ?? inferScopeFromRuntimeName(override, ratLang, document),
    };
  }

  // 1. Front-matter runtime binding
  const fm = frontMatterRuntime(document, ratLang);
  if (fm) {
    return {
      name: fm,
      cwd: inferRuntimeCwd(fm, cwd),
      lang: ratLang,
      scope: inferScopeFromRuntimeName(fm, ratLang, document),
    };
  }

  // 2. VS Code setting
  const runtimes = vscode.workspace
    .getConfiguration("rat")
    .get<Record<string, string>>("runtimes", {});

  for (const [key, value] of Object.entries(runtimes)) {
    if (key === ratLang || ALIAS[key] === ratLang) {
      return {
        name: value,
        cwd: inferRuntimeCwd(value, cwd),
        lang: ratLang,
        scope: inferScopeFromRuntimeName(value, ratLang, document),
      };
    }
  }

  // 3. Explicit scope selection (default is project)
  const scope = getScopeOverride(docUri, ratLang) ?? "project";
  switch (scope) {
    case "notebook":
      return {
        name: notebookRuntimeName(ratLang, document),
        cwd,
        lang: ratLang,
        scope,
      };
    case "global":
      return {
        name: globalRuntimeName(ratLang),
        cwd: os.homedir(),
        lang: ratLang,
        scope,
      };
    case "project":
    default:
      return {
        name: projectRuntimeName(ratLang, cwd),
        cwd,
        lang: ratLang,
        scope: "project",
      };
  }
}

export function inferScopeFromRuntimeName(
  runtimeName: string,
  ratLang: string,
  document: vscode.TextDocument,
): RuntimeScope {
  if (runtimeName === globalRuntimeName(ratLang)) return "global";
  if (runtimeName === notebookRuntimeName(ratLang, document)) return "notebook";
  if (runtimeName === projectRuntimeName(ratLang, workspaceCwd(document))) return "project";
  return "project";
}

export function globalRuntimeName(ratLang: string): string {
  return `${ratLang}-global`;
}

export function projectRuntimeName(ratLang: string, cwd: string): string {
  const state = readState();

  for (const k of state.kernels) {
    if (k.lang === ratLang && isSameProject(k.cwd, cwd)) return k.name;
  }
  for (const r of state.runtimes) {
    if (r.lang === ratLang && isSameProject(r.cwd, cwd)) return r.name;
  }

  return `${ratLang}@${slug(path.basename(cwd) || "project")}`;
}

export function notebookRuntimeName(
  ratLang: string,
  document: vscode.TextDocument,
): string {
  const base = path.basename(document.fileName, path.extname(document.fileName)) || "file";
  const hash = shortHash(document.uri.toString());
  return `${ratLang}-nb-${slug(base)}-${hash}`;
}

function inferRuntimeCwd(runtimeName: string, fallback: string): string {
  const state = readState();
  const kernel = state.kernels.find((k) => k.name === runtimeName);
  if (kernel?.cwd) return kernel.cwd;
  const runtime = state.runtimes.find((r) => r.name === runtimeName);
  if (runtime?.cwd) return runtime.cwd;
  return fallback;
}

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

  const ratBlock = fmMatch[1].match(/^rat:\s*\r?\n((?:\s+.+\r?\n?)*)/m);
  if (!ratBlock) return null;

  for (const m of ratBlock[1].matchAll(/^\s+(\w+):\s*(\S+)/gm)) {
    const lang = m[1];
    const name = m[2];
    if (lang === ratLang || ALIAS[lang] === ratLang) return name;
  }
  return null;
}

function slug(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40) || "runtime";
}

function shortHash(value: string): string {
  let h = 2166136261;
  for (let i = 0; i < value.length; i++) {
    h ^= value.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return (h >>> 0).toString(36).slice(0, 6);
}
