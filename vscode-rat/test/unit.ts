import * as assert from "assert/strict";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";

import { parseCells, findOutputBlock } from "../src/cells";
import { parseRatNotebookDocument } from "../src/documentModel";
import { detectFileLang, isRatFile } from "../src/langDetect";
import {
  grammarWasmForRatLang,
  markdownCellSnippets,
  ratLangForFence,
} from "../src/languages";
import { parseSha256ChecksumText } from "../src/installRat";
import { rebaseInlineResultsForTest } from "../src/inlineOutput";
import { separateStatus } from "../src/output";

interface FakeLine {
  text: string;
  isEmptyOrWhitespace: boolean;
}

interface FakeDoc {
  fileName: string;
  languageId: string;
  lineCount: number;
  uri: { fsPath: string; toString: () => string };
  lineAt(index: number): FakeLine;
  getText(): string;
}

function fakeDoc(
  text: string,
  fileName = "/tmp/test.md",
  languageId = "markdown",
): FakeDoc {
  const lines = text.split("\n");
  return {
    fileName,
    languageId,
    lineCount: lines.length,
    uri: { fsPath: fileName, toString: () => `file://${fileName}` },
    lineAt(index: number) {
      const line = lines[index] ?? "";
      return { text: line, isEmptyOrWhitespace: line.trim().length === 0 };
    },
    getText() {
      return text;
    },
  };
}

async function main(): Promise<void> {
  assert.equal(ratLangForFence("python3"), "py");
  assert.equal(ratLangForFence("bash"), "sh");
  assert.equal(grammarWasmForRatLang("py"), "tree-sitter-python.wasm");
  assert.ok(markdownCellSnippets().some((snippet) => snippet.prefix === "pi"));

  const unsupportedOuterFence = [
    "````markdown",
    "```python",
    "print('example only')",
    "```",
    "````",
    "",
    "```r",
    "1 + 1",
    "```",
  ].join("\n");
  const cells = parseCells(fakeDoc(unsupportedOuterFence) as any);
  assert.equal(cells.length, 1);
  assert.equal(cells[0].ratLang, "r");
  assert.equal(cells[0].code, "1 + 1");

  const quarto = [
    "```{python}",
    "#| eval: false",
    "x = 1",
    "```",
  ].join("\n");
  const [quartoCell] = parseCells(fakeDoc(quarto) as any);
  assert.equal(quartoCell.ratLang, "py");
  assert.equal(quartoCell.executable, false);

  const outputDoc = fakeDoc([
    "```python",
    "1 + 1",
    "```",
    "",
    "```output | ✓ 1ms | 0 vars",
    "2",
    "```",
    "",
    "![plot](_assets/a.png)",
    "prose",
  ].join("\n"));
  const output = findOutputBlock(outputDoc as any, 2);
  assert.deepEqual(output, { startLine: 4, endLine: 6, imageEndLine: 8 });

  const model = parseRatNotebookDocument(outputDoc as any);
  assert.equal(model.cells.length, 1);
  assert.equal(model.outputs.length, 1);
  assert.equal(model.cells[0].output?.startLine, 4);
  assert.equal(model.outputs[0].pairedCellOpenLine, 0);

  const nestedFenceOutputDoc = fakeDoc([
    "```python",
    "print('fence')",
    "```",
    "",
    "````output | ✓ 1ms",
    "```text",
    "literal fence in output",
    "```",
    "````",
  ].join("\n"));
  const nestedOutput = findOutputBlock(nestedFenceOutputDoc as any, 2);
  assert.deepEqual(nestedOutput, { startLine: 4, endLine: 8, imageEndLine: 8 });
  const nestedModel = parseRatNotebookDocument(nestedFenceOutputDoc as any);
  assert.equal(nestedModel.cells[0].output?.endLine, 8);

  assert.deepEqual(
    separateStatus("hello\n\n✓ 1ms | 1 var"),
    { body: "hello", status: "✓ 1ms | 1 var" },
  );

  assert.deepEqual(
    rebaseInlineResultsForTest(
      [{ line: 10, text: "ok", isError: false }],
      [{ startLine: 3, startCharacter: 0, endLine: 3, endCharacter: 0, text: "a\nb\n" }],
    ),
    [{ line: 12, text: "ok", isError: false }],
  );
  assert.deepEqual(
    rebaseInlineResultsForTest(
      [{ line: 10, text: "stale", isError: true }],
      [{ startLine: 10, startCharacter: 1, endLine: 10, endCharacter: 2, text: "x" }],
    ),
    [],
  );

  const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
  assert.equal(parseSha256ChecksumText(`${hash}  rat-linux-amd64`, "rat-linux-amd64"), hash);
  assert.equal(parseSha256ChecksumText(`${hash} *rat-windows-amd64.exe`, "rat-windows-amd64.exe"), hash);
  assert.equal(parseSha256ChecksumText(`SHA256 (rat-darwin-arm64) = ${hash}`, "rat-darwin-arm64"), hash);
  assert.equal(parseSha256ChecksumText("# none", "rat-linux-amd64"), null);

  const unknown = fakeDoc("hello", "/tmp/notes.txt", "plaintext");
  assert.deepEqual(detectFileLang(unknown as any), { mode: "unsupported", ratLang: null });
  assert.equal(isRatFile(unknown as any), false);

  process.env.XDG_CONFIG_HOME = fs.mkdtempSync(path.join(os.tmpdir(), "rat-vscode-test-"));
  const stateDir = path.join(process.env.XDG_CONFIG_HOME, "rat");
  fs.mkdirSync(stateDir, { recursive: true });
  fs.writeFileSync(path.join(stateDir, "state.yaml"), [
    "kernels:",
    "    - name: py@space-project",
    "      lang: py",
    "      port: 8123",
    "      pid: 456",
    "      cwd: \"/tmp/project with spaces\"",
    "      venv: '/tmp/project with spaces/.venv'",
    "      status: running",
    "      started: 2026-01-01T00:00:00Z",
    "runtimes:",
    "    - name: py-saved",
    "      lang: py",
    "      cwd: /tmp/saved project",
    "      venv: /tmp/saved project/.venv",
  ].join("\n"));
  const rat = await import("../src/rat");
  const ratState = rat.readState();
  assert.equal(ratState.kernels[0].cwd, "/tmp/project with spaces");
  assert.equal(ratState.kernels[0].venv, "/tmp/project with spaces/.venv");
  assert.equal(ratState.runtimes[0].cwd, "/tmp/saved project");

  const vscode = await import("vscode") as any;
  const project = path.join(os.tmpdir(), "rat-vscode-project");
  vscode.__setWorkspaceFolder(project);
  vscode.__setConfig({ rat: { runtimes: {} } });

  const resolve = await import("../src/resolve");
  const runtimeDoc = fakeDoc("", path.join(project, "analysis.md"), "markdown");
  const docKey = runtimeDoc.uri.toString();
  assert.equal(resolve.getScopeOverride(docKey, "py"), undefined);

  const projectPreview = resolve.resolveRuntimeForScope("py", runtimeDoc as any, "project");
  assert.equal(projectPreview.name, "py@rat-vscode-project");
  assert.equal(projectPreview.scope, "project");
  assert.equal(projectPreview.cwd, project);

  const preview = resolve.resolveRuntimeForScope("py", runtimeDoc as any, "global");
  assert.equal(preview.name, "py-global");
  assert.equal(preview.scope, "global");
  assert.equal(preview.cwd, os.homedir());
  assert.equal(resolve.getScopeOverride(docKey, "py"), undefined);

  console.log("unit tests passed");
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
