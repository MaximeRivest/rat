/**
 * runtimePicker.ts — Quick Pick for selecting / switching runtimes.
 *
 * Shown when the user clicks the runtime status bar item or runs
 * "Rat: Select Runtime".  Lists running kernels, saved runtimes,
 * and action items (add, restart, stop).
 */

import * as vscode from "vscode";
import * as path from "path";
import {
  readState,
  ratAdd,
  ratStart,
  type KernelInfo,
  type SavedRuntime,
} from "./rat";
import {
  setRuntimeOverride,
  clearRuntimeOverride,
  getRuntimeOverride,
} from "./resolve";
import { parseCells } from "./cells";

// ── Pick items ─────────────────────────────────────────────

interface RuntimePickItem extends vscode.QuickPickItem {
  action:
    | { kind: "select"; name: string }
    | { kind: "clear" }
    | { kind: "add" }
    | { kind: "restart"; name: string }
    | { kind: "stop"; name: string };
}

// ── Main picker ────────────────────────────────────────────

/**
 * Show a Quick Pick listing available runtimes for the current
 * document's primary language.  The user can select a runtime,
 * add a new one, or manage existing ones.
 */
export async function showRuntimePicker(): Promise<string | undefined> {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;

  // Determine the primary language from code cells in the document
  const cells = parseCells(editor.document);
  if (cells.length === 0) {
    vscode.window.showInformationMessage(
      "No code cells found in this document",
    );
    return;
  }

  const lang = cells[0].ratLang;
  const docUri = editor.document.uri.toString();
  const currentOverride = getRuntimeOverride(docUri, lang);

  const state = readState();
  const items: RuntimePickItem[] = [];

  // Running kernels matching the language
  const matching = state.kernels.filter((k) => k.lang === lang);
  for (const k of matching) {
    const isCurrent = currentOverride
      ? k.name === currentOverride
      : k.name === lang;
    items.push({
      label: `${isCurrent ? "$(check) " : "     "}${k.name}`,
      description: `${shortPath(k.cwd)}${k.venv ? "  (.venv)" : ""}`,
      detail: `Running on port ${k.port}`,
      action: { kind: "select", name: k.name },
    });
  }

  // Saved runtimes (not running) matching the language
  const runningNames = new Set(state.kernels.map((k) => k.name));
  for (const r of state.runtimes) {
    if (r.lang !== lang || runningNames.has(r.name)) continue;
    items.push({
      label: `     ${r.name}`,
      description: `${shortPath(r.cwd)}${r.venv ? "  (.venv)" : ""}  [stopped]`,
      action: { kind: "select", name: r.name },
    });
  }

  // Separator + actions
  items.push({
    label: "",
    kind: vscode.QuickPickItemKind.Separator,
    action: { kind: "clear" },
  });

  if (currentOverride) {
    items.push({
      label: "$(close) Clear override (use default)",
      action: { kind: "clear" },
    });
  }

  items.push({
    label: "$(add) Add new runtime…",
    action: { kind: "add" },
  });

  if (matching.length > 0) {
    const current = matching.find(
      (k) => k.name === (currentOverride ?? lang),
    );
    if (current) {
      items.push({
        label: `$(debug-restart) Restart ${current.name}`,
        action: { kind: "restart", name: current.name },
      });
      items.push({
        label: `$(debug-stop) Stop ${current.name}`,
        action: { kind: "stop", name: current.name },
      });
    }
  }

  const picked = await vscode.window.showQuickPick(items, {
    placeHolder: `Select ${lang} runtime for this document`,
    matchOnDescription: true,
  });
  if (!picked) return;

  switch (picked.action.kind) {
    case "select": {
      const name = picked.action.name;
      // Start if not running
      if (!runningNames.has(name)) {
        try {
          const rt = state.runtimes.find((r) => r.name === name);
          await ratStart(name, rt?.cwd ?? process.cwd());
        } catch (err: unknown) {
          vscode.window.showErrorMessage(
            `Rat: ${err instanceof Error ? err.message : err}`,
          );
          return;
        }
      }
      setRuntimeOverride(docUri, lang, name);
      return name;
    }

    case "clear":
      clearRuntimeOverride(docUri, lang);
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

// ── Add-runtime wizard ─────────────────────────────────────

async function addRuntimeWizard(
  defaultLang: string,
): Promise<string | undefined> {
  const name = await vscode.window.showInputBox({
    prompt: "Runtime name",
    placeHolder: `e.g. ${defaultLang}-myproject`,
    validateInput: (v) =>
      /^[\w@.-]+$/.test(v) ? null : "Use letters, digits, -, _, @, .",
  });
  if (!name) return;

  // Pick directory
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

  // Auto-detect venv
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

    // Set as override for current document
    const editor = vscode.window.activeTextEditor;
    if (editor) {
      setRuntimeOverride(editor.document.uri.toString(), defaultLang, name);
    }
    return name;
  } catch (err: unknown) {
    vscode.window.showErrorMessage(
      `Rat: ${err instanceof Error ? err.message : err}`,
    );
    return;
  }
}

function shortPath(p: string): string {
  const home = process.env.HOME || process.env.USERPROFILE || "";
  if (home && p.startsWith(home)) return "~" + p.slice(home.length);
  return p;
}
