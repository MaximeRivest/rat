import * as esbuild from "esbuild";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { fileURLToPath } from "url";
import { spawnSync } from "child_process";

const root = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const outdir = fs.mkdtempSync(path.join(os.tmpdir(), "rat-vscode-tests-"));
const outfile = path.join(outdir, "unit.cjs");

const vscodeStub = String.raw`
class Position {
  constructor(line, character) {
    this.line = line;
    this.character = character;
  }
}

class Range {
  constructor(a, b, c, d) {
    if (a instanceof Position && b instanceof Position) {
      this.start = a;
      this.end = b;
    } else {
      this.start = new Position(a, b);
      this.end = new Position(c, d);
    }
  }
}

class ThemeColor { constructor(id) { this.id = id; } }
class MarkdownString {
  constructor(value = '') { this.value = value; this.isTrusted = false; }
  appendCodeblock(value, language) { const fence = String.fromCharCode(96, 96, 96); this.value += fence + (language || '') + '\n' + value + '\n' + fence; return this; }
  appendMarkdown(value) { this.value += value; return this; }
}
class EventEmitter {
  constructor() { this.listeners = []; this.event = (listener) => { this.listeners.push(listener); return { dispose() {} }; }; }
  fire(value) { for (const listener of this.listeners) listener(value); }
  dispose() { this.listeners = []; }
}

const state = { workspaceFolder: null, config: {} };
function configFor(section) {
  return {
    get(key, fallback) {
      const scoped = section ? state.config[section] : state.config;
      return scoped && Object.prototype.hasOwnProperty.call(scoped, key) ? scoped[key] : fallback;
    },
    update(key, value) {
      const scoped = section ? (state.config[section] ??= {}) : state.config;
      scoped[key] = value;
      return Promise.resolve();
    },
  };
}

const Uri = {
  file(fsPath) { return { fsPath, scheme: 'file', path: fsPath, toString: () => 'file://' + fsPath }; },
  parse(value) { return { fsPath: value.replace(/^file:\/\//, ''), scheme: value.split(':', 1)[0], path: value, toString: () => value }; },
  joinPath(base, ...parts) {
    const joined = [base.fsPath ?? base.path ?? '', ...parts].join('/').replace(/\/+/g, '/');
    return Uri.file(joined);
  },
};

const workspace = {
  workspaceFolders: [],
  getWorkspaceFolder() { return state.workspaceFolder ? { uri: Uri.file(state.workspaceFolder) } : undefined; },
  getConfiguration(section) { return configFor(section); },
  openTextDocument() { throw new Error('openTextDocument not implemented in test stub'); },
  applyEdit() { return Promise.resolve(true); },
  onDidChangeConfiguration() { return { dispose() {} }; },
  onDidChangeTextDocument() { return { dispose() {} }; },
};

const window = {
  activeTextEditor: undefined,
  createOutputChannel() { return { appendLine() {}, append() {}, show() {}, dispose() {} }; },
  createTextEditorDecorationType() { return { dispose() {} }; },
  createStatusBarItem() { return { show() {}, hide() {}, dispose() {} }; },
  showInformationMessage() { return Promise.resolve(undefined); },
  showErrorMessage() { return Promise.resolve(undefined); },
  withProgress(_opts, task) { return task({ report() {} }); },
  onDidChangeActiveColorTheme() { return { dispose() {} }; },
  onDidChangeActiveTextEditor() { return { dispose() {} }; },
  onDidChangeTextEditorSelection() { return { dispose() {} }; },
};

const languages = {
  createDiagnosticCollection() { return { set() {}, delete() {}, dispose() {} }; },
};

const commands = { executeCommand() { return Promise.resolve(undefined); } };
const env = { openExternal() { return Promise.resolve(true); } };
const ProgressLocation = { Notification: 15 };
const StatusBarAlignment = { Left: 1, Right: 2 };
const DiagnosticSeverity = { Error: 0 };
const CompletionItemKind = { Function: 1, Method: 2, Module: 3, Class: 4, Property: 5, Keyword: 6, Constant: 7, File: 8, Folder: 9, Variable: 10, Snippet: 11 };
const DocumentHighlightKind = { Text: 0, Write: 1 };
const QuickPickItemKind = { Separator: -1 };
const ViewColumn = { Active: -1, Beside: -2 };
const ColorThemeKind = { Dark: 2, HighContrast: 3 };

module.exports = {
  Position,
  Range,
  ThemeColor,
  MarkdownString,
  EventEmitter,
  Uri,
  workspace,
  window,
  languages,
  commands,
  env,
  ProgressLocation,
  StatusBarAlignment,
  DiagnosticSeverity,
  CompletionItemKind,
  DocumentHighlightKind,
  QuickPickItemKind,
  ViewColumn,
  ColorThemeKind,
  __setWorkspaceFolder(folder) { state.workspaceFolder = folder; workspace.workspaceFolders = folder ? [{ uri: Uri.file(folder) }] : []; },
  __setConfig(config) { state.config = config; },
};
`;

await esbuild.build({
  entryPoints: [path.join(root, "test", "unit.ts")],
  bundle: true,
  platform: "node",
  format: "cjs",
  outfile,
  sourcemap: false,
  external: ["web-tree-sitter"],
  plugins: [
    {
      name: "vscode-stub",
      setup(build) {
        build.onResolve({ filter: /^vscode$/ }, () => ({ path: "vscode", namespace: "vscode-stub" }));
        build.onLoad({ filter: /.*/, namespace: "vscode-stub" }, () => ({ contents: vscodeStub, loader: "js" }));
      },
    },
  ],
});

const result = spawnSync(process.execPath, [outfile], {
  cwd: root,
  stdio: "inherit",
  env: { ...process.env },
});

process.exit(result.status ?? 1);
