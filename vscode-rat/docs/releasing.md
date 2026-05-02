# VS Code extension release checklist

This checklist is for publishing `vscode-rat` to the VS Code Marketplace and Open VSX.

## Versioning

- The Marketplace version is `vscode-rat/package.json` → `version`.
- Keep `vscode-rat/package-lock.json` in sync with the package version.
- Use extension-specific git tags like `vscode-rat-v0.2.2` to avoid collisions with repository-wide Rat CLI tags.

## Release notes

1. Find the last Marketplace version.
2. Review changes since that version, usually with:

   ```bash
   git log --oneline <last-release-ref>..HEAD -- vscode-rat cmd/rat internal
   git diff --stat <last-release-ref>..HEAD -- vscode-rat cmd/rat internal
   ```

3. Add a dated section to `vscode-rat/CHANGELOG.md`.
4. Keep notes user-facing first:
   - Highlights
   - Added
   - Changed
   - Fixed
   - Internal

## Validation

Run from the repository root unless noted:

```bash
cd vscode-rat
npm run lint
npm test
npm run build
npm run package
git diff --check
```

If Rat CLI/MCP changes are included in the release, also run from the repository root:

```bash
go test ./...
```

## Publish

From `vscode-rat/`:

```bash
npm run publish:marketplace
npm run publish:openvsx
```

Or publish both:

```bash
npm run publish:all
```

After publishing, tag the exact commit:

```bash
git tag vscode-rat-v$(node -p "require('./vscode-rat/package.json').version")
git push origin vscode-rat-v$(node -p "require('./vscode-rat/package.json').version")
```
