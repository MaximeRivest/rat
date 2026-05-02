import * as assert from "assert/strict";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";

import { parseCells, findOutputBlock } from "../src/cells";
import { detectFileLang, isRatFile } from "../src/langDetect";
import {
  grammarWasmForRatLang,
  markdownCellSnippets,
  ratLangForFence,
} from "../src/languages";
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

  assert.deepEqual(
    separateStatus("hello\n\n✓ 1ms | 1 var"),
    { body: "hello", status: "✓ 1ms | 1 var" },
  );

  const unknown = fakeDoc("hello", "/tmp/notes.txt", "plaintext");
  assert.deepEqual(detectFileLang(unknown as any), { mode: "unsupported", ratLang: null });
  assert.equal(isRatFile(unknown as any), false);

  process.env.XDG_CONFIG_HOME = fs.mkdtempSync(path.join(os.tmpdir(), "rat-vscode-test-"));
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
