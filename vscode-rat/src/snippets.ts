/**
 * snippets.ts — Quick cell-creation completions.
 *
 * Typing `py`, `sh`, `r`, `ju`, or `js` at the start of a line
 * and pressing Tab inserts a fenced code cell.
 *
 * Implemented as a CompletionItemProvider (not a static snippets
 * file) so it works even when markdown quick-suggestions are off
 * — VS Code still shows completions on explicit Tab / Ctrl+Space.
 */

import * as vscode from "vscode";

interface CellSnippet {
  prefix: string;
  fence: string;
  label: string;
}

export const CELL_SNIPPETS: CellSnippet[] = [
  { prefix: "py", fence: "python", label: "Python cell" },
  { prefix: "sh", fence: "bash", label: "Shell cell" },
  { prefix: "r", fence: "r", label: "R cell" },
  { prefix: "ju", fence: "julia", label: "Julia cell" },
  { prefix: "js", fence: "javascript", label: "JavaScript cell" },
  { prefix: "pi", fence: "pi", label: "Pi cell" },
];

export class RatSnippetProvider implements vscode.CompletionItemProvider {
  provideCompletionItems(
    document: vscode.TextDocument,
    position: vscode.Position,
  ): vscode.CompletionItem[] | null {
    // Only trigger at the start of a line (only whitespace before cursor)
    const lineText = document.lineAt(position.line).text;
    const beforeCursor = lineText.slice(0, position.character);
    if (beforeCursor.trim().length === 0) return null; // nothing typed yet
    if (beforeCursor.trimStart() !== beforeCursor.trim()) {
      // There's trailing whitespace after the typed text — skip
    }

    // Only match if the line so far is just the prefix (possibly with leading whitespace)
    const typed = beforeCursor.trim();

    const items: vscode.CompletionItem[] = [];
    for (const s of CELL_SNIPPETS) {
      if (!s.prefix.startsWith(typed)) continue;

      const item = new vscode.CompletionItem(
        s.prefix,
        vscode.CompletionItemKind.Snippet,
      );
      item.detail = `Insert \`\`\`${s.fence} cell`;
      item.documentation = new vscode.MarkdownString(
        `Inserts a fenced \`${s.fence}\` code cell`,
      );
      item.insertText = new vscode.SnippetString(
        "```" + s.fence + "\n$0\n```",
      );
      // Replace the typed prefix on the current line
      item.range = new vscode.Range(
        position.line,
        position.character - typed.length,
        position.line,
        position.character,
      );
      item.sortText = "!" + s.prefix; // sort to the top
      item.filterText = s.prefix;
      items.push(item);
    }

    return items.length > 0 ? items : null;
  }
}
