/**
 * codeLens.ts — "▶ Run" / "▶▶ Run Above" buttons above each code cell.
 */

import * as vscode from "vscode";
import { parseCells } from "./cells";

export class RatCodeLensProvider implements vscode.CodeLensProvider {
  private _onDidChange = new vscode.EventEmitter<void>();
  readonly onDidChangeCodeLenses = this._onDidChange.event;

  refresh(): void {
    this._onDidChange.fire();
  }

  provideCodeLenses(document: vscode.TextDocument): vscode.CodeLens[] {
    const cells = parseCells(document);
    const lenses: vscode.CodeLens[] = [];

    for (const cell of cells) {
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
    }

    return lenses;
  }
}
