/**
 * codeLens.ts — "▶ Run" / "▶▶ Run Above" buttons above each code cell.
 */

import * as vscode from "vscode";
import { parseRatNotebookDocument } from "./documentModel";

export class RatCodeLensProvider implements vscode.CodeLensProvider {
  private _onDidChange = new vscode.EventEmitter<void>();
  readonly onDidChangeCodeLenses = this._onDidChange.event;

  refresh(): void {
    this._onDidChange.fire();
  }

  provideCodeLenses(document: vscode.TextDocument): vscode.CodeLens[] {
    const model = parseRatNotebookDocument(document);
    const lenses: vscode.CodeLens[] = [];

    for (const cell of model.cells) {
      if (!cell.executable) continue;

      const range = new vscode.Range(cell.openLine, 0, cell.openLine, 0);

      lenses.push(
        new vscode.CodeLens(range, {
          title: "Run",
          command: "rat.runCellAt",
          arguments: [cell.openLine],
          tooltip: `Run this ${cell.lang} cell`,
        }),
      );

      lenses.push(
        new vscode.CodeLens(range, {
          title: " Run Above",
          command: "rat.runAboveAt",
          arguments: [cell.openLine],
          tooltip: "Run all cells from the top through this one",
        }),
      );

      if (cell.ratLang === "py") {
        lenses.push(
          new vscode.CodeLens(range, {
            title: " Debug",
            command: "rat.debugPythonCellAt",
            arguments: [cell.openLine],
            tooltip: "Run this Python cell in the VS Code debugger",
          }),
        );

        lenses.push(
          new vscode.CodeLens(range, {
            title: " Debug Above",
            command: "rat.debugPythonAboveAt",
            arguments: [cell.openLine],
            tooltip: "Run Python cells from the top through this one in the VS Code debugger",
          }),
        );
      }
    }

    return lenses;
  }
}
