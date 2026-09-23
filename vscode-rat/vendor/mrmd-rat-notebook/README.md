# Vendored: mrmd rat-notebook

What a Markdown notebook run on rat means, as plain functions: rat's
output cleaning, plot markers, the result format a run leaves in the
document, and which result a cell owns. Shared with Chattering (which
loads it inside the mrmd-document bundle as `mrmdDocument.ratNotebook`),
so a notebook behaves the same in both editors.

- Version: 0.16.0 (`rat-notebook.js`, no dependencies)
- Source: `/home/maxime/Projects/mrmd-packages/mrmd-editor/src/rat-notebook.js`
- Source commit: `a2c5d4b`
- SHA-256: `01b879301c87d71169231b70b9d495bf5262e226e6c80de7d0c82422d5133b9b`
- `rat-notebook.d.ts` is written here, for `tsc`; update it with the API.

Used by: `src/cells.ts` and `src/documentModel.ts` (a result owns only
the images a run made — `isOwnedImageLine`), `src/queue.ts` (plot
markers — `splitPlots`; plots saved as `<assetsDir>/generated/<content
hash>.png`).

Not yet shared with Chattering: VS Code still writes the result as the
run streams, with the status on the fence (`output | ✓ 1.5s`); Chattering
writes once, output only. Both read both. Moving VS Code onto mrmd's
notebook runner removes the difference (it needs the light editor, see
Chattering design 63).

## Update

Copy `src/rat-notebook.js` from mrmd-editor into a new version folder,
record the commit and SHA-256 here, update the imports, run `npm test`
and `npm run lint`.
