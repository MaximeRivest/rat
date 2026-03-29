/**
 * Deno REPL frontend that routes execution to a shared MCP kernel.
 *
 * Usage:
 *     deno run --allow-net --allow-env deno_frontend.ts [--server http://127.0.0.1:8720/mcp]
 *
 * Uses Deno's built-in eval and a readline-style REPL. Deno doesn't expose
 * its internal REPL for customization, so we build a minimal one using
 * prompt() + eval(). The experience is: real V8 execution, multiline support,
 * Deno APIs available, but the namespace is shared via MCP.
 *
 * For the proof, execution runs locally through a shared global scope.
 * Replace localEval with mcpEval to connect to a real MCP server.
 */

// ── MCP client (minimal) ────────────────────────────────────────

let msgId = 0;

async function mcpCall(
  serverUrl: string,
  method: string,
  params: Record<string, unknown> = {},
): Promise<Record<string, unknown>> {
  msgId++;
  const payload = {
    jsonrpc: "2.0",
    id: msgId,
    method,
    params,
  };

  try {
    const resp = await fetch(serverUrl, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    const body = await resp.json();
    if (body.error) return { error: body.error };
    return body.result ?? {};
  } catch (e) {
    return { error: String(e) };
  }
}

async function mcpInitialize(serverUrl: string) {
  return mcpCall(serverUrl, "initialize", {
    protocolVersion: "2025-03-26",
    capabilities: {},
    clientInfo: { name: "rat-deno-repl", version: "0.1.0" },
  });
}

async function mcpTool(
  serverUrl: string,
  tool: string,
  args: Record<string, unknown>,
) {
  return mcpCall(serverUrl, "tools/call", { name: tool, arguments: args });
}

function displayMcpResult(result: Record<string, unknown>) {
  const content = (result.content ?? []) as Array<Record<string, string>>;
  for (const item of content) {
    if (item.type === "text" && item.text) {
      console.log(item.text);
    }
  }
}

// ── Shared namespace (local proof mode) ──────────────────────────

// Use globalThis as the shared namespace for local eval
// This means variables persist across evaluations — same as a real REPL.

function localEval(code: string): unknown {
  try {
    // Rewrite const/let to var so variables persist across eval calls.
    // In a real kernel, the subprocess maintains one continuous scope.
    // This is a proof-of-concept workaround for eval()'s block scoping.
    const patched = code.replace(
      /^([ \t]*)(const|let)([ \t]+)/gm,
      "$1var$3",
    );
    // Indirect eval — runs in global scope
    const result = (0, eval)(patched);
    return result;
  } catch (e) {
    if (e instanceof Error) {
      console.error(`${e.constructor.name}: ${e.message}`);
    } else {
      console.error(e);
    }
    return undefined;
  }
}

// ── REPL ─────────────────────────────────────────────────────────

function isIncomplete(code: string): boolean {
  // Heuristic: check for unmatched brackets/parens/braces/template literals
  let parens = 0, brackets = 0, braces = 0, backticks = 0;
  let inString: string | null = null;

  for (let i = 0; i < code.length; i++) {
    const ch = code[i];
    const prev = i > 0 ? code[i - 1] : "";

    if (inString) {
      if (ch === inString && prev !== "\\") inString = null;
      continue;
    }

    if (ch === '"' || ch === "'") { inString = ch; continue; }
    if (ch === "`") { backticks++; continue; }
    if (ch === "(") parens++;
    if (ch === ")") parens--;
    if (ch === "[") brackets++;
    if (ch === "]") brackets--;
    if (ch === "{") braces++;
    if (ch === "}") braces--;
  }

  if (parens > 0 || brackets > 0 || braces > 0 || backticks % 2 !== 0) {
    return true;
  }

  // Ends with operator — likely continues
  const trimmed = code.trimEnd();
  if (/[+\-*/%=<>&|,{([]$/.test(trimmed)) return true;

  return false;
}

async function startRepl(serverUrl: string) {
  const version = Deno.version.deno;
  console.log(`rat deno | Deno ${version}`);

  if (serverUrl) {
    const result = await mcpInitialize(serverUrl);
    if ("error" in result) {
      console.error(`Cannot connect to MCP server: ${result.error}`);
      console.error(`Start the server first, then retry.`);
      Deno.exit(1);
    }
    const info = (result as Record<string, Record<string, string>>).serverInfo;
    console.log(`MCP mode — executing on ${serverUrl} (${info?.name ?? "unknown"})`);
  } else {
    console.log("Local proof mode — interception active");
  }

  console.log("Shared namespace — other MCP clients see your variables.\n");

  const encoder = new TextEncoder();
  const decoder = new TextDecoder();
  let buffer = "";

  while (true) {
    const prompt = buffer ? "  " : "> ";

    // Write prompt
    await Deno.stdout.write(encoder.encode(prompt));

    // Read line
    const buf = new Uint8Array(4096);
    const n = await Deno.stdin.read(buf);

    if (n === null) {
      // EOF (Ctrl+D)
      console.log();
      break;
    }

    const line = decoder.decode(buf.subarray(0, n)).replace(/\n$/, "");

    // Accumulate
    buffer = buffer ? buffer + "\n" + line : line;

    // Check if incomplete
    if (isIncomplete(buffer)) continue;

    const code = buffer.trim();
    buffer = "";

    if (!code) continue;

    // Special commands
    if (code === ".exit" || code === "exit") break;

    if (serverUrl) {
      // MCP mode
      const result = await mcpTool(serverUrl, "run", { code });
      displayMcpResult(result);
    } else {
      // Local proof mode
      const result = localEval(code);
      if (result !== undefined) {
        console.log(Deno.inspect(result, { colors: true, depth: 4 }));
      }
    }
  }
}

// ── Entry point ──────────────────────────────────────────────────

let serverUrl = "";
const args = Deno.args;

for (let i = 0; i < args.length; i++) {
  if (args[i] === "--server" && i + 1 < args.length) {
    serverUrl = args[i + 1];
  }
}

await startRepl(serverUrl);
