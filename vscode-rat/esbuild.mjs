import * as esbuild from "esbuild";

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
};

if (watch) {
  const ctx = await esbuild.context(opts);
  await ctx.watch();
  console.log("watching...");
} else {
  await esbuild.build(opts);
  console.log("built dist/extension.js");
}
