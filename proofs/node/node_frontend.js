/**
 * Node.js REPL frontend — proof that eval can be intercepted.
 *
 * Usage:
 *     node node_frontend.js
 *
 * Uses Node's built-in `repl` module — readline, history, tab
 * completion, .editor mode, multiline. We only override eval.
 */

const repl = require("repl");

console.log(`rat js | Node ${process.version}`);
console.log("Local proof mode — interception active");
console.log("Shared namespace — other MCP clients would see your variables.\n");

// ── Shared namespace (simulates the MCP kernel) ─────────────────

const sharedContext = {};

// ── Intercepted REPL ────────────────────────────────────────────

const server = repl.start({
  prompt: "> ",

  // THIS IS THE ONLY OVERRIDE — everything else is real Node REPL
  eval: (code, context, filename, callback) => {
    // ── INTERCEPTION POINT ──
    // In production: mcpCall("run", { code })
    // For proof: execute in shared context via vm

    const trimmed = code.trim();
    if (!trimmed) return callback(null);

    try {
      // Use the REPL's context (which persists variables)
      const vm = require("vm");
      const result = vm.runInContext(trimmed, context, { filename });
      callback(null, result);
    } catch (e) {
      // Let the REPL handle error display
      callback(e);
    }
  },
});

// Customize the context — in production this would sync with the MCP kernel
server.context.rat = "Run AnyThing";

server.on("exit", () => {
  console.log("\nGoodbye.");
  process.exit(0);
});
