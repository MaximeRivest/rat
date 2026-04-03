/**
 * rat.ts — Talk to rat kernels.
 *
 * Two channels:
 *   1. CLI  (`rat start`, `rat stop`) — lifecycle management.
 *   2. MCP-over-HTTP (JSON-RPC to /mcp) — code execution, look, ctl.
 *
 * The MCP client is a minimal hand-rolled implementation of the
 * Streamable-HTTP transport so we carry zero npm runtime deps.
 */

import * as http from "http";
import * as cp from "child_process";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import * as vscode from "vscode";

// ── CLI helpers ────────────────────────────────────────────

function ratBin(): string {
  return vscode.workspace.getConfiguration("rat").get("path", "rat");
}

function exec(
  args: string[],
  cwd?: string,
  timeout = 30_000,
  rejectOnError = false,
): Promise<{ stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    cp.execFile(
      ratBin(),
      args,
      { cwd, timeout, env: { ...process.env } },
      (err, stdout, stderr) => {
        if (err && (err as NodeJS.ErrnoException).code === "ENOENT") {
          reject(
            new Error(
              "rat not found.  Install: curl -fsSL https://runanything.dev/install.sh | sh",
            ),
          );
          return;
        }
        if (err && rejectOnError) {
          reject(
            new Error(stderr?.trim() || stdout?.trim() || (err as Error).message),
          );
          return;
        }
        // rat start exits 0 whether new or already running
        resolve({ stdout: stdout ?? "", stderr: stderr ?? "" });
      },
    );
  });
}

export async function ratStart(name: string, cwd: string): Promise<void> {
  await exec(["start", name], cwd);
}

export async function ratStop(name: string): Promise<void> {
  await exec(["stop", name], undefined, 10_000);
}

export async function ratRestart(name: string, cwd: string): Promise<void> {
  await exec(["restart", name], cwd);
}

export async function ratAdd(
  name: string,
  lang: string,
  cwd: string,
  venv?: string,
): Promise<void> {
  const args = ["add", name, "--lang", lang, "--cwd", cwd];
  if (venv) args.push("--venv", venv);
  await exec(args, cwd);
}

export async function ratRm(name: string): Promise<void> {
  await exec(["rm", name]);
}

/**
 * Run `rat install <lang>` in the given directory.
 * Creates venv if needed, installs language deps (e.g. IPython + jedi),
 * and may start a kernel.  Longer timeout because package installation
 * can take a while on first run.
 */
export async function ratInstall(
  lang: string,
  cwd: string,
): Promise<void> {
  await exec(["install", lang], cwd, 120_000, true);
}

// ── State file ─────────────────────────────────────────────

function stateFilePath(): string {
  if (os.platform() === "darwin") {
    return path.join(
      os.homedir(),
      "Library",
      "Application Support",
      "rat",
      "state.yaml",
    );
  }
  const dir =
    process.env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config");
  return path.join(dir, "rat", "state.yaml");
}

// ── State types ────────────────────────────────────────────

export interface KernelInfo {
  name: string;
  lang: string;
  port: number;
  pid: number;
  cwd: string;
  venv: string;
  started: string;
}

export interface SavedRuntime {
  name: string;
  lang: string;
  cwd: string;
  venv: string;
}

export interface RatState {
  kernels: KernelInfo[];
  runtimes: SavedRuntime[];
}

/** Read the full state file (running kernels + saved runtimes). */
export function readState(): RatState {
  let content: string;
  try {
    content = fs.readFileSync(stateFilePath(), "utf-8");
  } catch {
    return { kernels: [], runtimes: [] };
  }
  return {
    kernels: parseSection<KernelInfo>(content, "kernels", (entry) => {
      const name = entry.match(/name:\s*(\S+)/);
      const lang = entry.match(/lang:\s*(\S+)/);
      const port = entry.match(/port:\s*(\d+)/);
      const pid = entry.match(/pid:\s*(\d+)/);
      const cwd = entry.match(/cwd:\s*(\S+)/);
      const venv = entry.match(/venv:\s*(\S+)/);
      const started = entry.match(/started:\s*(\S+)/);
      if (!name || !port) return null;
      return {
        name: name[1],
        lang: lang?.[1] ?? "",
        port: parseInt(port[1], 10),
        pid: pid ? parseInt(pid[1], 10) : 0,
        cwd: cwd?.[1] ?? "",
        venv: venv?.[1] ?? "",
        started: started?.[1] ?? "",
      };
    }),
    runtimes: parseSection<SavedRuntime>(content, "runtimes", (entry) => {
      const name = entry.match(/name:\s*(\S+)/);
      const lang = entry.match(/lang:\s*(\S+)/);
      const cwd = entry.match(/cwd:\s*(\S+)/);
      const venv = entry.match(/venv:\s*(\S+)/);
      if (!name) return null;
      return {
        name: name[1],
        lang: lang?.[1] ?? "",
        cwd: cwd?.[1] ?? "",
        venv: venv?.[1] ?? "",
      };
    }),
  };
}

function parseSection<T>(
  yaml: string,
  section: string,
  parse: (entry: string) => T | null,
): T[] {
  const re = new RegExp(
    `${section}:\\s*\\n([\\s\\S]*?)(?:\\n[a-z]|$)`,
  );
  const block = yaml.match(re);
  if (!block) return [];
  const results: T[] = [];
  for (const m of block[1].matchAll(/- name:[^\n]*(\n(?:  [^\n]*\n?)*)/g)) {
    const entry = "name:" + m[0].slice("- name:".length);
    const item = parse(entry);
    if (item) results.push(item);
  }
  return results;
}

export function getKernelPort(name: string): number | null {
  const k = readState().kernels.find((k) => k.name === name);
  return k?.port ?? null;
}

// ── MCP client (Streamable HTTP) ───────────────────────────

export class McpClient {
  private url: string;
  private sessionId: string | null = null;
  private nextId = 1;
  private activeReq: http.ClientRequest | null = null;
  private _disposed = false;

  constructor(port: number) {
    this.url = `http://127.0.0.1:${port}/mcp`;
  }

  /* ---- public API ---- */

  async initialize(): Promise<void> {
    await this.rpc("initialize", {
      protocolVersion: "2025-03-26",
      capabilities: {},
      clientInfo: { name: "rat-vscode", version: "0.1.0" },
    });
    await this.notify("notifications/initialized", {});
  }

  async run(code: string): Promise<ToolResult> {
    const r = await this.callTool("run", { code });
    return this.parseToolResult(r);
  }

  async sendInput(text: string): Promise<void> {
    await this.callTool("run", { input: text });
  }

  async look(at?: string): Promise<string> {
    const args: Record<string, unknown> = {};
    if (at) args.at = at;
    return this.parseToolResult(await this.callTool("look", args)).text;
  }

  async complete(code: string, cursor: number): Promise<string[]> {
    const r = await this.callTool("look", { code, cursor });
    const text = this.parseToolResult(r).text;
    return text ? text.split("\n").filter(Boolean) : [];
  }

  async status(): Promise<string> {
    return this.parseToolResult(
      await this.callTool("ctl", { op: "status" }),
    ).text;
  }

  /**
   * Return stdout accumulated so far during the current execution.
   * Returns "" when idle or when nothing has been printed yet.
   * Uses a non-tracked HTTP request so it doesn't interfere with
   * abortCurrentRequest().
   */
  async partialOutput(): Promise<string> {
    return this.parseToolResult(
      await this.rpc("tools/call", { name: "ctl", arguments: { op: "output" } }, false),
    ).text;
  }

  async cancel(): Promise<void> {
    // fire-and-forget cancel, don't await because the run request might
    // be in flight on the same client
    this.callTool("ctl", { op: "cancel" }).catch(() => {});
  }

  /** Abort the in-flight HTTP request (if any). */
  abortCurrentRequest(): void {
    this.activeReq?.destroy();
    this.activeReq = null;
  }

  dispose(): void {
    this._disposed = true;
    this.abortCurrentRequest();
  }

  /* ---- internal ---- */

  private async callTool(
    name: string,
    args: Record<string, unknown>,
  ): Promise<unknown> {
    return this.rpc("tools/call", { name, arguments: args }, true);
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  private parseToolResult(result: any): ToolResult {
    const content: Array<{ type: string; text?: string }> =
      result?.content ?? [];
    const text = content
      .filter((c) => c.type === "text")
      .map((c) => c.text ?? "")
      .join("\n");
    return { text, isError: !!result?.isError };
  }

  private rpc(
    method: string,
    params: unknown,
    track = true,
  ): Promise<unknown> {
    const id = this.nextId++;
    const body = JSON.stringify({ jsonrpc: "2.0", id, method, params });
    return this.post(body, track).then((res) => {
      let payload = res.body;

      // SSE responses: extract the last `data:` line that carries our id.
      if (
        res.headers["content-type"]?.toString().includes("text/event-stream")
      ) {
        for (const line of res.body.split("\n")) {
          if (line.startsWith("data: ")) {
            try {
              const obj = JSON.parse(line.slice(6));
              if (obj.id === id) {
                payload = line.slice(6);
                break;
              }
            } catch {
              /* skip */
            }
          }
        }
      }

      const data = JSON.parse(payload);
      if (data.error) {
        throw new Error(data.error.message ?? JSON.stringify(data.error));
      }
      return data.result;
    });
  }

  private notify(method: string, params: unknown): Promise<void> {
    const body = JSON.stringify({ jsonrpc: "2.0", method, params });
    return this.post(body).then(() => {});
  }

  private post(
    body: string,
    track = true,
  ): Promise<{
    statusCode: number;
    headers: http.IncomingHttpHeaders;
    body: string;
  }> {
    return new Promise((resolve, reject) => {
      if (this._disposed) {
        reject(new Error("client disposed"));
        return;
      }

      const u = new URL(this.url);
      const hdrs: Record<string, string> = {
        "Content-Type": "application/json",
        Accept: "application/json, text/event-stream",
        "Content-Length": String(Buffer.byteLength(body)),
      };
      if (this.sessionId) hdrs["Mcp-Session-Id"] = this.sessionId;

      const req = http.request(
        {
          hostname: u.hostname,
          port: u.port,
          path: u.pathname,
          method: "POST",
          headers: hdrs,
        },
        (res) => {
          const sid = res.headers["mcp-session-id"];
          if (sid) this.sessionId = Array.isArray(sid) ? sid[0] : sid;

          let data = "";
          res.on("data", (chunk: Buffer) => (data += chunk.toString()));
          res.on("end", () => {
            this.activeReq = null;
            resolve({
              statusCode: res.statusCode ?? 0,
              headers: res.headers,
              body: data,
            });
          });
        },
      );

      if (track) this.activeReq = req;
      req.on("error", (err) => {
        if (track) this.activeReq = null;
        reject(err);
      });
      req.write(body);
      req.end();
    });
  }
}

export interface ToolResult {
  text: string;
  isError: boolean;
}

// ── Connection pool ────────────────────────────────────────

const pool = new Map<string, McpClient>();

/**
 * Get or create an MCP client for the named kernel.
 *
 * Lifecycle:
 *   1. Check pool for a live connection → return it.
 *   2. Try `rat start <name>` → connect if port appears.
 *   3. If start failed, run `rat install <lang>` (creates venv,
 *      installs deps, starts kernel) and then connect.
 *   4. For project-qualified names (e.g. "py@proj"), register the
 *      runtime via `rat add` before starting.
 */
export async function getClient(
  name: string,
  cwd: string,
  lang?: string,
): Promise<McpClient> {
  // 1. Try existing pool connection
  const existing = pool.get(name);
  if (existing) {
    try {
      await existing.status();
      return existing;
    } catch {
      existing.dispose();
      pool.delete(name);
    }
  }

  // 2. Try starting the kernel directly
  await ratStart(name, cwd);
  let port = getKernelPort(name);
  if (port) {
    try {
      const client = new McpClient(port);
      await client.initialize();
      pool.set(name, client);
      return client;
    } catch {
      // Port in state but kernel is dead — continue to install
    }
  }

  // 3. Auto-install: create venv, install deps, start kernel
  if (!lang) {
    throw new Error(
      `Kernel "${name}" failed to start. Try: rat install`,
    );
  }

  await vscode.window.withProgress(
    {
      location: vscode.ProgressLocation.Notification,
      title: `Rat: setting up ${lang} for ${path.basename(cwd)}…`,
      cancellable: false,
    },
    () => ratInstall(lang, cwd),
  );

  // After install, check if our kernel is now running
  port = getKernelPort(name);

  // 4. If name is project-qualified (e.g. "py@proj"), install started
  //    a kernel with the bare lang name. Register + start ours.
  if (!port && name !== lang) {
    // Check if install started the bare-name kernel for THIS project
    const state = readState();
    const bareKernel = state.kernels.find(
      (k) =>
        k.name === lang &&
        path.resolve(k.cwd) === path.resolve(cwd),
    );
    if (bareKernel) {
      // The bare name belongs to us — use it directly
      const client = new McpClient(bareKernel.port);
      await client.initialize();
      pool.set(bareKernel.name, client);
      return client;
    }

    // Register and start a project-qualified runtime
    await ratAdd(name, lang, cwd);
    await ratStart(name, cwd);
    port = getKernelPort(name);
  }

  if (!port) {
    throw new Error(
      `Kernel "${name}" failed to start. Run: rat doctor ${lang}`,
    );
  }

  const client = new McpClient(port);
  await client.initialize();
  pool.set(name, client);
  return client;
}

/** Return the pool entry without starting anything. */
export function existingClient(name: string): McpClient | undefined {
  return pool.get(name);
}

export function disposeAll(): void {
  for (const c of pool.values()) c.dispose();
  pool.clear();
}
