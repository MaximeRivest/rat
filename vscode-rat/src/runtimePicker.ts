/**
 * runtimePicker.ts — Quick Pick for selecting / switching runtimes.
 *
 * Shown when the user clicks the runtime status bar item or runs
 * "Rat: Select Runtime". It exposes the semantic scope model:
 *   - notebook
 *   - project
 *   - global
 * and lets the user open a REPL, select an existing runtime, or
 * manage the current runtime.
 */

import * as path from "path";
import * as vscode from "vscode";
import {
  readState,
  ratAdd,
  ratStart,
} from "./rat";
import {
  clearRuntimeOverride,
  getRuntimeOverride,
  getScopeOverride,
  inferScopeFromRuntimeName,
  projectRuntimeName,
  resolveRuntime,
  resolveRuntimeForScope,
  scopeLabel,
  setRuntimeOverride,
  setScopeOverride,
  type RuntimeScope,
} from "./resolve";
import { parseCells } from "./cells";
import { detectFileLang } from "./langDetect";

interface RuntimePickItem extends vscode.QuickPickItem {
  action:
    | { kind: "openRepl" }
    | { kind: "scope"; scope: RuntimeScope }
    | { kind: "select"; name: string }
    | { kind: "clear" }
    | { kind: "add" }
    | { kind: "restart" }
    | { kind: "stop" };
}

export async function showRuntimePicker(): Promise<string | undefined> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;

  const lang = currentDocumentLang(editor.document);
  if (!lang) {
    vscode.window.showInformationMessage("No rat runtime available for this file");
    return;
  }

  const docUri = editor.document.uri.toString();
  const current = resolveRuntime(lang, editor.document);
  const explicitOverride = getRuntimeOverride(docUri, lang);
  const explicitScope = getScopeOverride(docUri, lang);

  const state = readState();
  const runningNames = new Set(state.kernels.map((k) => k.name));
  const matching = dedupeRuntimeChoices(lang, [
    ...state.kernels.filter((k) => k.lang === lang).map((k) => ({
      name: k.name,
      cwd: k.cwd,
      venv: k.venv,
      detail: `Running on port ${k.port}`,
    })),
    ...state.runtimes
      .filter((r) => r.lang === lang && !runningNames.has(r.name))
      .map((r) => ({
        name: r.name,
        cwd: r.cwd,
        venv: r.venv,
        detail: "Stopped",
      })),
  ]);

  const items: RuntimePickItem[] = [
    {
      label: `$(terminal) Open REPL for ${current.name}`,
      description: `${scopeLabel(current.scope)} scope`,
      detail: shortPath(current.cwd),
      action: { kind: "openRepl" },
    },
    {
      label: "",
      kind: vscode.QuickPickItemKind.Separator,
      action: { kind: "openRepl" },
    },
    {
      label: `${current.scope === "notebook" ? "$(check) " : ""}Notebook scope`,
      description: "This file gets its own runtime",
      detail: resolveRuntimeForScope(lang, editor.document, "notebook").name,
      action: { kind: "scope", scope: "notebook" },
    },
    {
      label: `${current.scope === "project" ? "$(check) " : ""}Project scope`,
      description: "Share one runtime with this workspace",
      detail: resolveRuntimeForScope(lang, editor.document, "project").name,
      action: { kind: "scope", scope: "project" },
    },
    {
      label: `${current.scope === "global" ? "$(check) " : ""}Global scope`,
      description: "Share one runtime across projects",
      detail: resolveRuntimeForScope(lang, editor.document, "global").name,
      action: { kind: "scope", scope: "global" },
    },
    {
      label: "",
      kind: vscode.QuickPickItemKind.Separator,
      action: { kind: "openRepl" },
    },
  ];

  for (const r of matching) {
    const inferredScope = inferScopeFromRuntimeName(r.name, lang, editor.document);
    const isCurrent = r.name === current.name;
    items.push({
      label: `${isCurrent ? "$(check) " : ""}${r.name}`,
      description: `${shortPath(r.cwd)}${r.venv ? "  (.venv)" : ""}`,
      detail: `${r.detail} · ${scopeLabel(inferredScope)}`,
      action: { kind: "select", name: r.name },
    });
  }

  items.push(
    {
      label: "",
      kind: vscode.QuickPickItemKind.Separator,
      action: { kind: "openRepl" },
    },
    {
      label: "$(add) Add new runtime…",
      action: { kind: "add" },
    },
    {
      label: `$(debug-restart) Restart ${current.name}`,
      action: { kind: "restart" },
    },
    {
      label: `$(debug-stop) Stop ${current.name}`,
      action: { kind: "stop" },
    },
  );

  if (explicitOverride || explicitScope) {
    items.push({
      label: "$(close) Clear file override",
      description: "Return to default project scope",
      action: { kind: "clear" },
    });
  }

  const picked = await vscode.window.showQuickPick(items, {
    placeHolder: `${lang} → ${current.name} · ${scopeLabel(current.scope)} scope`,
    matchOnDescription: true,
    matchOnDetail: true,
  });
  if (!picked) return;

  switch (picked.action.kind) {
    case "openRepl":
      await vscode.commands.executeCommand("rat.openRepl");
      return current.name;

    case "scope": {
      clearRuntimeOverride(docUri, lang);
      setScopeOverride(docUri, lang, picked.action.scope);
      const next = resolveRuntime(lang, editor.document);
      vscode.window.showInformationMessage(
        `Rat: ${path.basename(editor.document.fileName)} now uses ${scopeLabel(picked.action.scope)} scope (${next.name})`,
      );
      return next.name;
    }

    case "select": {
      const name = picked.action.name;
      const runtime = matching.find((r) => r.name === name);
      if (!runningNames.has(name)) {
        try {
          await ratStart(name, runtime?.cwd ?? current.cwd);
        } catch (err: unknown) {
          vscode.window.showErrorMessage(
            `Rat: ${err instanceof Error ? err.message : err}`,
          );
          return;
        }
      }
      setRuntimeOverride(docUri, lang, name);
      setScopeOverride(docUri, lang, inferScopeFromRuntimeName(name, lang, editor.document));
      return name;
    }

    case "clear":
      clearRuntimeOverride(docUri, lang);
      setScopeOverride(docUri, lang, "project");
      return;

    case "add":
      return addRuntimeWizard(lang);

    case "restart":
      await vscode.commands.executeCommand("rat.restartKernel");
      return;

    case "stop":
      await vscode.commands.executeCommand("rat.stopKernel");
      return;
  }
}

function currentDocumentLang(document: vscode.TextDocument): string | null {
  const fl = detectFileLang(document);
  if (fl.mode === "source") return fl.ratLang;
  if (fl.mode !== "notebook") return null;
  const cells = parseCells(document);
  return cells[0]?.ratLang ?? null;
}

async function addRuntimeWizard(defaultLang: string): Promise<string | undefined> {
  const name = await vscode.window.showInputBox({
    prompt: "Runtime name",
    placeHolder: `e.g. ${defaultLang}-myproject`,
    validateInput: (v) =>
      /^[\w@.-]+$/.test(v) ? null : "Use letters, digits, -, _, @, .",
  });
  if (!name) return;

  const folders = vscode.workspace.workspaceFolders;
  let cwd: string;
  if (folders && folders.length === 1) {
    cwd = folders[0].uri.fsPath;
  } else {
    const picked = await vscode.window.showOpenDialog({
      canSelectFolders: true,
      canSelectFiles: false,
      canSelectMany: false,
      openLabel: "Select project directory",
    });
    if (!picked || picked.length === 0) return;
    cwd = picked[0].fsPath;
  }

  let venv: string | undefined;
  for (const dir of [".venv", "venv"]) {
    const p = path.join(cwd, dir, "bin", "python");
    try {
      const fs = await import("fs");
      fs.accessSync(p);
      venv = path.join(cwd, dir);
      break;
    } catch {
      /* not found */
    }
  }

  try {
    await ratAdd(name, defaultLang, cwd, venv);
    await ratStart(name, cwd);
    vscode.window.showInformationMessage(`Rat: ${name} added and started`);

    const editor = vscode.window.activeTextEditor;
    if (editor) {
      setRuntimeOverride(editor.document.uri.toString(), defaultLang, name);
      setScopeOverride(
        editor.document.uri.toString(),
        defaultLang,
        inferScopeFromRuntimeName(name, defaultLang, editor.document),
      );
    }
    return name;
  } catch (err: unknown) {
    vscode.window.showErrorMessage(
      `Rat: ${err instanceof Error ? err.message : err}`,
    );
    return;
  }
}

function dedupeRuntimeChoices(
  lang: string,
  choices: Array<{ name: string; cwd: string; venv: string; detail: string }>,
): Array<{ name: string; cwd: string; venv: string; detail: string }> {
  const groups = new Map<string, typeof choices>();

  for (const choice of choices) {
    const key = `${lang}::${path.resolve(choice.cwd)}::${aliasKey(choice.name)}`;
    const group = groups.get(key);
    if (group) group.push(choice);
    else groups.set(key, [choice]);
  }

  const deduped: typeof choices = [];
  for (const group of groups.values()) {
    if (group.length === 1) {
      deduped.push(group[0]);
      continue;
    }

    const preferredName = projectRuntimeName(lang, group[0].cwd);
    const preferred = group.find((choice) => choice.name === preferredName)
      ?? group.find((choice) => choice.detail.startsWith("Running"))
      ?? group[0];
    deduped.push(preferred);
  }

  return deduped;
}

function aliasKey(name: string): string {
  return name.toLowerCase().replace(/[-_]+/g, "_");
}

function shortPath(p: string): string {
  const home = process.env.HOME || process.env.USERPROFILE || "";
  if (home && p.startsWith(home)) return "~" + p.slice(home.length);
  return p;
}
