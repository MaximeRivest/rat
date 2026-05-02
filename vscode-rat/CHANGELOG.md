# Changelog

Release notes for the Rat VS Code extension. Marketplace releases should keep this file user-facing and concise.

## Pattern

Each release should include, in this order:

1. **Highlights** — 2–5 bullets for the Marketplace release page.
2. **Added** — new user-visible capabilities.
3. **Changed** — behavior or workflow changes.
4. **Fixed** — bugs and reliability fixes.
5. **Internal** — test/build/protocol changes worth noting for maintainers.

Use extension-specific tags such as `vscode-rat-v0.2.2` when tagging VS Code extension releases, so they do not collide with repository-wide Rat CLI tags.

## [Unreleased]

## [0.2.3] - 2026-05-02

### Highlights

- The VS Code extension installer now mirrors the Rat CLI into the user's normal terminal PATH, so `rat` works from terminals instead of only from inside the extension.
- **Rat: Install CLI** now acts as an install/update command and refreshes the PATH-visible binary.

### Changed

- Extension-managed CLI installs still keep a fallback copy in VS Code global storage, but the extension prefers the PATH-visible user install when available.
- The integrated terminal REPL command uses the same PATH-visible Rat binary after install/update.

### Fixed

- First-use CLI install no longer leaves users with a working extension but no `rat` command in their shell.

## [0.2.2] - 2026-05-02

### Highlights

- Rat Markdown is now much more robust: code execution goes through the shared execution controller, output streams live, cancellation is centralized, and stale webview edits are protected from overwriting newer text-editor changes.
- Source files and Markdown notebooks now share stronger parsing and output models, including safer fenced-output handling and better behavior around unsupported files/fences.
- Runtime and kernel interactions are more reliable, with structured MCP run results, streaming output notifications, safer cancellation, and improved state parsing for paths with spaces.
- Editor intelligence is cleaner: live completions still prefer running Rat kernels, while language-server fallback now uses in-memory virtual documents instead of temporary files.
- Release/package hygiene improved with a unit-test harness, checksum-aware CLI auto-install, and this changelog pattern for future Marketplace releases.

### Added

- Shared notebook document model used by CodeLens, decorations, and output pairing.
- Central language registry for aliases, source extensions, VS Code language ids, tree-sitter grammars, syntax highlighting, and Markdown cell snippets.
- Rat Markdown page/appearance settings:
  - `rat.markdownPageMode`
  - `rat.markdownThemeAssociations`
  - `rat.markdownPageCanvasBackground`
  - `rat.markdownFontScale`
- **Rat: Configure Rat Markdown Appearance** command.
- `pi` Markdown cell snippet.
- In-memory `rat-fallback:` virtual documents for LSP fallback completions/signature help.
- Inline source-result rebasing when edits insert/delete lines above previous results.
- Optional SHA-256 verification for the extension-managed Rat CLI download.
- VS Code unit-test harness covering language detection, fenced-cell parsing, output pairing, runtime resolution, state parsing, inline-output rebasing, and checksum parsing.

### Changed

- Rat Markdown custom editor execution now routes through the shared `ExecutionController` instead of managing its own direct kernel runs.
- Execution scheduling is serialized per runtime, allowing independent runtimes to run concurrently while preserving order within each kernel namespace.
- Runtime scope previews in the picker are now pure and no longer mutate persisted overrides just to render labels.
- Completion/signature fallback no longer creates temporary source files on disk.
- Source-file inline diagnostics/results are re-rendered after document changes so they do not drift as easily.
- Rat Markdown webview edits now carry document versions and reload on external edit conflicts rather than overwriting newer document text.

### Fixed

- Unsupported/plaintext files are no longer treated as Rat notebook files.
- Unsupported fenced Markdown blocks are skipped as whole fences, preventing runnable cells from being detected inside examples or literal Markdown blocks.
- Queue pause no longer hides the fact that a cell is still running/cancellable.
- Cancellation now targets the active run request instead of being overwritten by status/control polling.
- Output blocks can contain nested Markdown code fences without corrupting the document by using long-enough output fences.
- Structured run status avoids misclassifying normal output lines that happen to start with `✓` or `✗`.
- Runtime state parsing handles quoted YAML scalars and paths containing spaces.
- Markdown snippet completion no longer corrupts lines after trailing whitespace.
- Rat Markdown webview run cancellation is routed through the shared controller.
- CLI auto-install cleans up failed downloads and verifies downloaded binaries when checksums are available.

### Internal

- Added structured MCP run-result handling with legacy text fallback.
- Added streaming `rat/output` notification handling for live output.
- Kept `ctl(status)` polling only for stdin prompt detection.
- Added `rat status --json` support on the CLI side for machine-readable runtime state.
- Added `test/**` to `.vscodeignore` so test sources stay out of packaged VSIX files.

## [0.2.1] - 2026-04-26

- Last published VS Code Marketplace baseline before this changelog was introduced.
