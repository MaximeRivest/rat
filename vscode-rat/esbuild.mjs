import * as esbuild from "esbuild";
import * as fs from "fs";
import * as path from "path";

const watch = process.argv.includes("--watch");

/** @type {esbuild.BuildOptions} */
const opts = {
  entryPoints: ["src/extension.ts"],
  bundle: true,
  outfile: "dist/extension.js",
  external: ["vscode"],
  format: "cjs",
  platform: "node",
  target: "node18",
  sourcemap: true,
  minify: !watch,
  // web-tree-sitter loads .wasm at runtime via fs — don't bundle it
  loader: { ".wasm": "file" },
};

// Copy grammar .wasm files to dist/../grammars so they're findable at runtime
// (blocks.ts resolves them relative to __dirname)
function copyGrammars() {
  const src = path.resolve("grammars");
  const dest = path.resolve("grammars"); // stays in place; __dirname = dist/
  // grammars/ is already at the right level (sibling of dist/)
  // Just verify they exist
  if (!fs.existsSync(src)) {
    console.warn("⚠ grammars/ directory not found — tree-sitter block detection will fail");
  }
}

if (watch) {
  const ctx = await esbuild.context(opts);
  await ctx.watch();
  copyGrammars();
  console.log("watching...");
} else {
  await esbuild.build(opts);
  copyGrammars();
  console.log("built dist/extension.js");
}
