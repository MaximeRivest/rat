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
import { ensureRatInstalled, resolveRatExecutable } from "./installRat";

function detectVenv(cwd: string): string | undefined {
  for (const dir of [".venv", "venv"]) {
    const candidate = path.join(cwd, dir);
    const py = process.platform === "win32"
      ? path.join(candidate, "Scripts", "python.exe")
      : path.join(candidate, "bin", "python");
    if (fs.existsSync(py)) return candidate;
  }
  return undefined;
}

// ── Debug output ───────────────────────────────────────────

const out = vscode.window.createOutputChannel("Rat");

export function log(msg: string): void {
  out.appendLine(`[${new Date().toISOString()}] ${msg}`);
}

// ── CLI helpers ────────────────────────────────────────────

function execFileRat(
  bin: string,
  args: string[],
  cwd: string | undefined,
  timeout: number,
  rejectOnError: boolean,
): Promise<{ stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    cp.execFile(
      bin,
      args,
      { cwd, timeout, env: { ...process.env } },
      (err, stdout, stderr) => {
        if (err) {
          if (isMissingExecutable(err)) {
            reject(err);
            return;
          }
          if (rejectOnError) {
            reject(
              new Error(stderr?.trim() || stdout?.trim() || (err as Error).message),
            );
            return;
          }
        }
        // rat start exits 0 whether new or already running
        resolve({ stdout: stdout ?? "", stderr: stderr ?? "" });
      },
    );
  });
}

function isMissingExecutable(err: unknown): boolean {
  const code = (err as NodeJS.ErrnoException | undefined)?.code;
  return code === "ENOENT" || code === "ENOTDIR" || code === "EACCES" || code === "ENOEXEC";
}

async function exec(
  args: string[],
  cwd?: string,
  timeout = 30_000,
  rejectOnError = false,
): Promise<{ stdout: string; stderr: string }> {
  try {
    return await execFileRat(resolveRatExecutable(), args, cwd, timeout, rejectOnError);
  } catch (err) {
    if (!isMissingExecutable(err)) throw err;
    const installed = await ensureRatInstalled();
    return execFileRat(installed, args, cwd, timeout, rejectOnError);
  }
}

/** Start a kernel, returning the resolved name and port. */
export async function ratStart(
  name: string,
  cwd: string,
): Promise<{ resolvedName: string; port: number } | null> {
  log(`ratStart("${name}", "${cwd}")`);
  const { stdout, stderr } = await exec(["start", name], cwd);
  log(`ratStart stdout: ${stdout.trim()}`);
  if (stderr.trim()) log(`ratStart stderr: ${stderr.trim()}`);
  // Parse: "py@myproject started on http://127.0.0.1:8722/mcp (PID 12345)"
  // Or:    "py@myproject already running on http://127.0.0.1:8722/mcp (PID 12345)"
  // Output may be on stdout or stderr depending on the command.
  const combined = stdout + "\n" + stderr;
  const m = combined.match(/(\S+)\s+(?:started|already running)\s+on\s+http:\/\/[^:]+:(\d+)/);
  if (m) {
    log(`ratStart resolved: name=${m[1]} port=${m[2]}`);
    return { resolvedName: m[1], port: parseInt(m[2], 10) };
  }
  log(`ratStart: could not parse output`);
  return null;
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

export async function ratRemove(name: string): Promise<void> {
  await exec(["remove", name]);
}

export async function ratCancel(name: string): Promise<void> {
  await exec(["cancel", name], undefined, 10_000);
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
  status: string;
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
      const name = yamlField(entry, "name");
      const port = Number(yamlField(entry, "port"));
      if (!name || !Number.isFinite(port)) return null;
      const pid = Number(yamlField(entry, "pid"));
      return {
        name,
        lang: yamlField(entry, "lang"),
        port,
        pid: Number.isFinite(pid) ? pid : 0,
        cwd: yamlField(entry, "cwd"),
        venv: yamlField(entry, "venv"),
        status: yamlField(entry, "status") || "running",
        started: yamlField(entry, "started"),
      };
    }),
    runtimes: parseSection<SavedRuntime>(content, "runtimes", (entry) => {
      const name = yamlField(entry, "name");
      if (!name) return null;
      return {
        name,
        lang: yamlField(entry, "lang"),
        cwd: yamlField(entry, "cwd"),
        venv: yamlField(entry, "venv"),
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
  // Split block into entries at `    - name:` boundaries.
  const entries = block[1].split(/(?=^\s*- name:)/m).filter(s => s.trim());
  for (const raw of entries) {
    // Flatten indentation so field regexes work.
    const entry = raw.replace(/^\s*- /, "").replace(/^\s{4,}/gm, "");
    const item = parse(entry);
    if (item) results.push(item);
  }
  return results;
}

function yamlField(entry: string, key: string): string {
  const escaped = key.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = entry.match(new RegExp(`^${escaped}:\\s*(.*)$`, "m"));
  return match ? parseYamlScalar(match[1]) : "";
}

function parseYamlScalar(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed || trimmed === "null" || trimmed === "~") return "";

  if (trimmed.startsWith("'") && trimmed.endsWith("'")) {
    return trimmed.slice(1, -1).replace(/''/g, "'");
  }

  if (trimmed.startsWith('"') && trimmed.endsWith('"')) {
    try {
      return JSON.parse(trimmed) as string;
    } catch {
      return trimmed.slice(1, -1);
    }
  }

  // For unquoted YAML scalars, keep spaces in paths but strip common
  // inline comments that are separated by whitespace.
  return trimmed.replace(/\s+#.*$/, "");
}

export function getKernelPort(name: string): number | null {
  const k = readState().kernels.find((k) => k.name === name);
  return k?.port ?? null;
}

// ── MCP client (Streamable HTTP) ───────────────────────────

type RequestTrack = false | "run";

export interface McpNotification {
  method: string;
  params: Record<string, unknown>;
}

type NotificationHandler = (notification: McpNotification) => void;

class RequestTimeoutError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "RequestTimeoutError";
  }
}

function isRequestTimeout(err: unknown): boolean {
  return err instanceof RequestTimeoutError ||
    (err instanceof Error && err.name === "RequestTimeoutError");
}

export class McpClient {
  private url: string;
  private sessionId: string | null = null;
  private nextId = 1;
  private activeRunReq: http.ClientRequest | null = null;
  private requests = new Set<http.ClientRequest>();
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

  async run(
    code: string,
    onOutput?: (chunk: string) => void,
  ): Promise<ToolResult> {
    const r = await this.callTool(
      "run",
      { code },
      "run",
      onOutput
        ? (notification) => {
          if (notification.method !== "rat/output") return;
          const text = notification.params.text;
          if (typeof text === "string" && text.length > 0) onOutput(text);
        }
        : undefined,
    );
    return this.parseToolResult(r, { runResult: true });
  }

  async sendInput(text: string): Promise<void> {
    await this.callTool("run", { input: text }, false, undefined, 5_000);
  }

  async look(at?: string): Promise<string> {
    const args: Record<string, unknown> = {};
    if (at) args.at = at;
    return this.parseToolResult(await this.callTool("look", args, false, undefined, 10_000)).text;
  }

  async lookFull(at: string): Promise<string> {
    return this.parseToolResult(await this.callTool("look", { at, full: true }, false, undefined, 10_000)).text;
  }

  async tail(n = 10, format = "json"): Promise<string> {
    return this.parseToolResult(
      await this.callTool("tail", { n, format }, false, undefined, 5_000),
    ).text;
  }

  async complete(code: string, cursor: number): Promise<string[]> {
    const r = await this.callTool("look", { code, cursor }, false, undefined, 8_000);
    const text = this.parseToolResult(r).text;
    return text
      ? text
        .split("\n")
        .map((line) => line.trimEnd())
        .filter((line) => line && line.trim() !== "No completions.")
      : [];
  }

  async status(): Promise<string> {
    return this.parseToolResult(
      await this.callTool("ctl", { op: "status" }, false, undefined, 2_000),
    ).text;
  }

  async reset(): Promise<void> {
    await this.callTool("ctl", { op: "reset" }, false, undefined, 5_000);
  }

  /**
   * Return stdout accumulated so far during the current execution.
   * Returns "" when idle or when nothing has been printed yet.
   * Uses a non-tracked HTTP request so it doesn't interfere with
   * abortCurrentRequest().
   */
  async partialOutput(): Promise<string> {
    return this.parseToolResult(
      await this.rpc("tools/call", { name: "ctl", arguments: { op: "output" } }, false, undefined, 2_000),
    ).text;
  }

  async cancel(): Promise<void> {
    // fire-and-forget cancel, don't await because the run request might
    // be in flight on the same client
    this.callTool("ctl", { op: "cancel" }, false, undefined, 5_000).catch(() => {});
  }

  /** Abort the in-flight run request (if any). */
  abortCurrentRequest(): void {
    this.activeRunReq?.destroy(new Error("MCP run request aborted"));
    this.activeRunReq = null;
  }

  dispose(): void {
    this._disposed = true;
    for (const req of this.requests) req.destroy();
    this.requests.clear();
    this.activeRunReq = null;
  }

  /* ---- internal ---- */

  private async callTool(
    name: string,
    args: Record<string, unknown>,
    track: RequestTrack = false,
    onNotification?: NotificationHandler,
    timeoutMs = 0,
    cancelOnTimeout = false,
  ): Promise<unknown> {
    try {
      return await this.rpc("tools/call", { name, arguments: args }, track, onNotification, timeoutMs);
    } catch (err) {
      if (cancelOnTimeout && isRequestTimeout(err)) {
        this.callTool("ctl", { op: "cancel" }, false, undefined, 5_000).catch(() => {});
      }
      throw err;
    }
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  private parseToolResult(result: any, opts: { runResult?: boolean } = {}): ToolResult {
    const content: Array<{ type: string; text?: string }> =
      result?.content ?? [];
    const text = content
      .filter((c) => c.type === "text")
      .map((c) => c.text ?? "")
      .join("\n");

    if (opts.runResult) {
      const structured = parseStructuredRunResult(result?.structuredContent, text, !!result?.isError);
      if (structured) return structured;
      return parseLegacyRunResult(text, !!result?.isError);
    }

    return { text, isError: !!result?.isError, rawText: text };
  }

  private rpc(
    method: string,
    params: unknown,
    track: RequestTrack = false,
    onNotification?: NotificationHandler,
    timeoutMs = 0,
  ): Promise<unknown> {
    const id = this.nextId++;
    const body = JSON.stringify({ jsonrpc: "2.0", id, method, params });
    return this.post(body, track, onNotification, timeoutMs).then((res) => {
      let payload = res.body;

      // SSE responses: extract the last `data:` line that carries our id.
      if (
        res.headers["content-type"]?.toString().includes("text/event-stream")
      ) {
        for (const line of res.body.split("\n")) {
          if (line.startsWith("data:")) {
            const raw = line.slice(5).trimStart();
            try {
              const obj = JSON.parse(raw);
              if (obj.id === id) {
                payload = raw;
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
    track: RequestTrack = false,
    onNotification?: NotificationHandler,
    timeoutMs = 0,
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

      let req: http.ClientRequest;
      let settled = false;
      const cleanup = () => {
        this.requests.delete(req);
        if (track === "run" && this.activeRunReq === req) {
          this.activeRunReq = null;
        }
      };
      const finishReject = (err: Error) => {
        if (settled) return;
        settled = true;
        cleanup();
        reject(err);
      };
      const finishResolve = (value: {
        statusCode: number;
        headers: http.IncomingHttpHeaders;
        body: string;
      }) => {
        if (settled) return;
        settled = true;
        cleanup();
        resolve(value);
      };

      req = http.request(
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
          let sseBuffer = "";
          let ended = false;
          const isEventStream = res.headers["content-type"]
            ?.toString()
            .includes("text/event-stream") ?? false;

          res.on("data", (chunk: Buffer) => {
            const text = chunk.toString();
            data += text;
            if (isEventStream && onNotification) {
              sseBuffer = drainSseEvents(sseBuffer + text, onNotification);
            }
          });
          res.on("end", () => {
            ended = true;
            if (isEventStream && onNotification && sseBuffer.trim()) {
              drainSseEvents(sseBuffer + "\n\n", onNotification);
            }
            finishResolve({
              statusCode: res.statusCode ?? 0,
              headers: res.headers,
              body: data,
            });
          });
          res.on("aborted", () => {
            finishReject(new Error("MCP response aborted"));
          });
          res.on("error", (err) => {
            finishReject(err instanceof Error ? err : new Error(String(err)));
          });
          res.on("close", () => {
            if (!ended) finishReject(new Error("MCP response closed before end"));
          });
        },
      );

      this.requests.add(req);
      if (track === "run") this.activeRunReq = req;
      if (timeoutMs > 0) {
        req.setTimeout(timeoutMs, () => {
          req.destroy(new RequestTimeoutError(`MCP request timed out after ${timeoutMs}ms`));
        });
      }
      req.on("error", (err) => {
        finishReject(err instanceof Error ? err : new Error(String(err)));
      });
      req.write(body);
      req.end();
    });
  }
}

export interface ToolResult {
  /** Body text without rat's status trailer when structured metadata is available. */
  text: string;
  isError: boolean;
  /** Rat execution status trailer, e.g. "✓ 150ms | 2 vars". */
  status?: string;
  durationMs?: number;
  vars?: number;
  execCount?: number;
  rawText?: string;
}

export function toolResultDisplayText(result: ToolResult): string {
  const body = result.text.trimEnd();
  if (!result.status) return body;
  return body ? `${body}\n\n${result.status}` : result.status;
}

function parseStructuredRunResult(
  structured: unknown,
  rawText: string,
  isError: boolean,
): ToolResult | null {
  if (!structured || typeof structured !== "object") return null;
  const value = structured as Record<string, unknown>;

  const success = typeof value.success === "boolean" ? value.success : !isError;
  const status = typeof value.status === "string" ? value.status : undefined;
  const output = typeof value.output === "string" ? value.output : undefined;
  const error = typeof value.error === "string" ? value.error : undefined;
  const durationMs = numberField(value, "durationMs") ?? numberField(value, "duration_ms");
  const vars = numberField(value, "vars");
  const execCount = numberField(value, "execCount") ?? numberField(value, "exec_count");

  if (output === undefined && error === undefined && !status) return null;

  return {
    text: success ? (output ?? "") : (error || output || rawText),
    isError: isError || !success,
    status,
    durationMs,
    vars,
    execCount,
    rawText,
  };
}

function parseLegacyRunResult(rawText: string, isError: boolean): ToolResult {
  const { body, status } = splitStatusTrailer(rawText);
  return { text: body, isError, status: status || undefined, rawText };
}

function splitStatusTrailer(text: string): { body: string; status: string } {
  const trimmed = text.trimEnd();
  if (!trimmed) return { body: "", status: "" };

  const lines = trimmed.split("\n");
  for (let i = lines.length - 1; i >= 0; i--) {
    const line = lines[i].trim();
    if (!line) continue;
    if (/^[✓✗] .+$/.test(line)) {
      const bodyLines = lines.slice(0, i);
      while (bodyLines.length > 0 && bodyLines[bodyLines.length - 1].trim() === "") {
        bodyLines.pop();
      }
      return { body: bodyLines.join("\n"), status: line };
    }
    break;
  }
  return { body: trimmed, status: "" };
}

function numberField(value: Record<string, unknown>, key: string): number | undefined {
  const raw = value[key];
  return typeof raw === "number" && Number.isFinite(raw) ? raw : undefined;
}

function drainSseEvents(
  buffer: string,
  onNotification: NotificationHandler,
): string {
  while (true) {
    const match = buffer.match(/\r?\n\r?\n/);
    if (!match || match.index === undefined) return buffer;

    const eventText = buffer.slice(0, match.index);
    buffer = buffer.slice(match.index + match[0].length);
    handleSseEvent(eventText, onNotification);
  }
}

function handleSseEvent(
  eventText: string,
  onNotification: NotificationHandler,
): void {
  const dataLines: string[] = [];
  for (const line of eventText.split(/\r?\n/)) {
    if (line.startsWith("data:")) dataLines.push(line.slice(5).trimStart());
  }
  if (dataLines.length === 0) return;

  try {
    const message = JSON.parse(dataLines.join("\n")) as {
      method?: unknown;
      params?: unknown;
    };
    if (typeof message.method !== "string") return;
    onNotification({
      method: message.method,
      params: message.params && typeof message.params === "object"
        ? message.params as Record<string, unknown>
        : {},
    });
  } catch {
    // Ignore malformed SSE keepalive / intermediary data.
  }
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
  log(`getClient("${name}", "${cwd}", lang=${lang})`);
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

  const state = readState();
  const known = state.kernels.some((k) => k.name === name) || state.runtimes.some((r) => r.name === name);

  if (!known && lang && name !== lang) {
    try {
      log(`getClient: creating named runtime ${name} (lang=${lang}, cwd=${cwd})`);
      await ratAdd(name, lang, cwd, lang === "py" ? detectVenv(cwd) : undefined);
    } catch (err) {
      log(`getClient: ratAdd skipped/failed for ${name}: ${err instanceof Error ? err.message : String(err)}`);
    }
  }

  log(`getClient: no pool hit for "${name}", calling ratStart`);
  // 2. Let the CLI resolve the name and start the kernel.
  const started = await ratStart(name, cwd);
  if (started) {
    try {
      const client = new McpClient(started.port);
      await client.initialize();
      pool.set(started.resolvedName, client);
      // Also alias the requested name so future lookups hit the pool.
      if (started.resolvedName !== name) {
        pool.set(name, client);
      }
      return client;
    } catch {
      // Port appeared but kernel is dead — continue to install
    }
  }

  log(`getClient: ratStart returned null or connect failed, trying install`);
  // 3. Auto-install: create venv, install deps, start kernel
  const installLang = lang ?? name;
  await vscode.window.withProgress(
    {
      location: vscode.ProgressLocation.Notification,
      title: `Rat: setting up ${installLang} for ${path.basename(cwd)}…`,
      cancellable: false,
    },
    () => ratInstall(installLang, cwd),
  );

  // 4. After install, try starting again.
  const retried = await ratStart(name, cwd);
  if (retried) {
    const client = new McpClient(retried.port);
    await client.initialize();
    pool.set(retried.resolvedName, client);
    if (retried.resolvedName !== name) {
      pool.set(name, client);
    }
    return client;
  }

  const errMsg = `Kernel "${name}" failed to start. Run: rat doctor ${installLang}`;
  log(`getClient: FAILED - ${errMsg}`);
  throw new Error(errMsg);
}

/** Return the pool entry without starting anything. */
export function existingClient(name: string): McpClient | undefined {
  return pool.get(name);
}

/**
 * Return an already-running client without starting a kernel.
 *
 * This is for latency-sensitive editor features like completions and hover:
 * they should reconnect to a running kernel after a VS Code reload, but they
 * should not create/install/start kernels merely because the user typed a dot.
 */
export async function existingOrRunningClient(name: string): Promise<McpClient | undefined> {
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

  const kernel = readState().kernels.find((k) => k.name === name);
  if (!kernel) return undefined;

  try {
    const client = new McpClient(kernel.port);
    await client.initialize();
    pool.set(name, client);
    return client;
  } catch {
    return undefined;
  }
}

export function disposeAll(): void {
  for (const c of pool.values()) c.dispose();
  pool.clear();
}
