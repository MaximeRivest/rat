import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import * as crypto from "crypto";
import { fileURLToPath } from "url";
import * as vscode from "vscode";
import type {
  AgentSession,
  AgentSessionEvent,
  SessionEntry,
  SessionHeader,
} from "@earendil-works/pi-coding-agent";

import { ratLangForFence } from "./cells";
import type { ExecutionController } from "./queue";
import { existingOrRunningClient } from "./rat";
import { projectRuntimeName } from "./resolve";

const MODE_COMMAND = "mode";
const MODE_ENTRY_TYPE = "mode-switch";
const DEFAULT_PROMPT_MODE = "";

interface PromptModeOption {
  value: string;
  label: string;
  opener?: string;
  appendix?: string;
  systemPrompt?: string;
  removeSections?: string[];
}

interface PromptModeDefinition {
  key: string;
  label: string;
  opener: string;
  appendix?: string;
  systemPrompt?: string;
  removeSections?: string[];
}

const SYSTEM_PROMPT_SECTIONS = new Set([
  "available_tools",
  "custom_tools_note",
  "guidelines",
  "pi_docs",
  "append_prompt",
  "project_context",
  "skills",
  "date",
  "cwd",
]);

interface WebviewMessage {
  type: string;
  id?: number;
  entryId?: string;
  targetId?: string;
  text?: string;
  code?: string;
  language?: string;
  mode?: string;
  modeDefinition?: PromptModeDefinition;
  sessionName?: string;
  label?: string;
  summarize?: boolean;
  customInstructions?: string;
  providerId?: string;
  modelId?: string;
  authType?: "oauth" | "api_key";
  apiKey?: string;
  promptId?: string;
  value?: string;
  at?: string | null;
  cursor?: number;
  url?: string;
  mimeType?: string;
  assetType?: string;
}

interface TreeNodeDto {
  id: string;
  parentId: string | null;
  type: string;
  role: string;
  timestamp: string;
  label?: string;
  labelTimestamp?: string;
  preview: string;
  markdown: string;
  children: TreeNodeDto[];
}

interface SessionTreeDto {
  roots: TreeNodeDto[];
  leafId: string | null;
  activePathIds: string[];
}

interface SessionLine {
  raw: string;
  entry: Record<string, any>;
}

interface EditableSessionEntry {
  id: string;
  role: string;
  timestamp: string;
  markdown: string;
  cwd: string;
  sessionPath: string;
}

interface DisplaySession {
  header: SessionHeader | null;
  editable: EditableSessionEntry[];
  mode: string;
  sessionName: string;
}

interface SessionListItem {
  label: string;
  path: string;
  cwd: string;
  time: string;
  mtime: number;
}

type PiSdk = typeof import("@earendil-works/pi-coding-agent");
let piSdkPromise: Promise<PiSdk> | undefined;
function loadPiSdk(): Promise<PiSdk> {
  piSdkPromise ??= import("@earendil-works/pi-coding-agent");
  return piSdkPromise;
}

type WebviewHost = {
  webview: vscode.Webview;
  title?: string;
  onDidDispose?: (listener: () => void) => vscode.Disposable;
};

export class PiSessionMrmdEditor implements vscode.WebviewViewProvider, vscode.Disposable {
  static readonly viewType = "ratPiSessions";

  private readonly disposables: vscode.Disposable[] = [];
  private readonly sessionLeafOverrides = new Map<string, string | null>();
  private readonly authPromptResolvers = new Map<string, (value: string | undefined) => void>();
  private readonly displaySessionCache = new Map<string, { mtimeMs: number; leafId: string | null | undefined; value: DisplaySession }>();
  private view?: vscode.WebviewView;

  static open(ctx: vscode.ExtensionContext, execution: ExecutionController, uri?: vscode.Uri): Promise<void> {
    return new PiSessionMrmdEditor(ctx, execution).open(uri);
  }

  constructor(
    private readonly ctx: vscode.ExtensionContext,
    private readonly execution: ExecutionController,
    private readonly onOpen?: () => unknown,
  ) {}

  dispose(): void {
    this.disposables.forEach((d) => d.dispose());
  }

  async show(uri?: vscode.Uri): Promise<void> {
    await Promise.resolve(this.onOpen?.());
    if (this.view && !uri) {
      this.view.show?.(true);
      await this.postSessions(this.view, "workspace");
      return;
    }
    await this.open(uri);
  }

  async resolveWebviewView(webviewView: vscode.WebviewView): Promise<void> {
    await Promise.resolve(this.onOpen?.());
    this.view = webviewView;
    webviewView.webview.options = {
      enableScripts: true,
      localResourceRoots: [vscode.Uri.joinPath(this.ctx.extensionUri, "media")],
    };
    this.setupWebview(webviewView, undefined);
    webviewView.onDidDispose(() => {
      if (this.view === webviewView) this.view = undefined;
    }, undefined, this.disposables);
  }

  async open(uri?: vscode.Uri): Promise<void> {
    await Promise.resolve(this.onOpen?.());
    const panel = vscode.window.createWebviewPanel(
      "rat.piSessionMrmdEditor",
      uri ? `Pi Session: ${path.basename(uri.fsPath)}` : "Pi Sessions",
      vscode.ViewColumn.Active,
      {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [
          vscode.Uri.joinPath(this.ctx.extensionUri, "media"),
          ...(uri ? [vscode.Uri.file(path.dirname(uri.fsPath))] : []),
          ...(vscode.workspace.workspaceFolders ?? []).map((folder) => folder.uri),
        ],
      },
    );

    this.setupWebview(panel, uri);
  }

  private setupWebview(host: WebviewHost, uri?: vscode.Uri): void {
    host.webview.html = this.html(host.webview, uri);

    const disposables: vscode.Disposable[] = [];
    let currentUri = uri;

    disposables.push(host.webview.onDidReceiveMessage(async (message: WebviewMessage & { path?: string; scope?: string }) => {
      try {
        switch (message.type) {
          case "ready":
            if (currentUri) {
              await this.postSession(host, currentUri);
            } else {
              await this.postSessions(host, "workspace");
            }
            break;
          case "refresh":
            if (currentUri) {
              await this.postSession(host, currentUri);
            } else {
              await this.postSessions(host, message.scope === "all" ? "all" : "workspace");
            }
            break;
          case "loadAllSessions":
            await this.postSessions(host, "all");
            break;
          case "newSession":
            currentUri = await this.createNewSession(host);
            host.title = `Pi Session: ${path.basename(currentUri.fsPath)}`;
            await this.postSession(host, currentUri);
            break;
          case "openSession":
            if (message.path) {
              currentUri = vscode.Uri.file(message.path);
              host.title = `Pi Session: ${path.basename(currentUri.fsPath)}`;
              await this.postSession(host, currentUri);
            } else {
              currentUri = undefined;
              host.title = "Pi Sessions";
              await this.postSessions(host, "workspace");
            }
            break;
          case "reload":
            if (currentUri) await this.postSession(host, currentUri);
            else await this.postSessions(host, message.scope === "all" ? "all" : "workspace");
            break;
          case "editEntry":
            if (currentUri) await this.updateEntry(host, currentUri, message);
            break;
          case "sendUserMessage":
            if (currentUri) await this.sendUserMessage(host, currentUri, message);
            break;
          case "setMode":
            if (currentUri) await this.setMode(host, currentUri, message);
            break;
          case "getModeDefinition":
            if (currentUri && typeof message.id === "number") await this.getModeDefinition(host, currentUri, message);
            break;
          case "saveModeDefinition":
            if (currentUri && typeof message.id === "number") await this.saveModeDefinition(host, currentUri, message);
            break;
          case "getSystemPrompt":
            if (currentUri && typeof message.id === "number") await this.getSystemPrompt(host, currentUri, message);
            break;
          case "setSessionName":
            if (currentUri) await this.setSessionName(host, currentUri, message);
            break;
          case "getTree":
            if (currentUri && typeof message.id === "number") await this.getTree(host, currentUri, message);
            break;
          case "navigateTree":
            if (currentUri && typeof message.id === "number") await this.navigateTree(host, currentUri, message);
            break;
          case "setTreeLabel":
            if (currentUri && typeof message.id === "number") await this.setTreeLabel(host, currentUri, message);
            break;
          case "getRuntimeControlsState":
            if (currentUri && typeof message.id === "number") await this.getRuntimeControlsState(host, currentUri, message);
            break;
          case "listModels":
            if (currentUri && typeof message.id === "number") await this.listModels(host, currentUri, message);
            break;
          case "setModel":
            if (currentUri && typeof message.id === "number") await this.setModel(host, currentUri, message);
            break;
          case "setThinkingLevel":
            if (currentUri && typeof message.id === "number") await this.setThinkingLevel(host, currentUri, message);
            break;
          case "listAuthProviders":
            if (currentUri && typeof message.id === "number") await this.listAuthProviders(host, currentUri, message);
            break;
          case "loginApiKey":
            if (currentUri && typeof message.id === "number") await this.loginApiKey(host, currentUri, message);
            break;
          case "loginOAuth":
            if (currentUri && typeof message.id === "number") await this.loginOAuth(host, currentUri, message);
            break;
          case "logoutProvider":
            if (currentUri && typeof message.id === "number") await this.logoutProvider(host, currentUri, message);
            break;
          case "authPromptResponse":
            if (message.promptId) this.authPromptResolvers.get(message.promptId)?.(typeof message.value === "string" ? message.value : undefined);
            break;
          case "ratRun":
            if (currentUri) await this.handleRatRun(host, currentUri, message);
            break;
          case "ratComplete":
            if (currentUri) await this.handleRatComplete(host, currentUri, message);
            break;
          case "ratInspect":
            if (currentUri) await this.handleRatInspect(host, currentUri, message);
            break;
          case "ratAsset":
            if (currentUri) await this.handleRatAsset(host, currentUri, message);
            break;
        }
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : String(err);
        if (typeof message.id === "number") {
          await host.webview.postMessage({ type: "rpcError", id: message.id, error: msg });
        } else {
          await host.webview.postMessage({ type: "showError", message: msg });
        }
      }
    }));

    if (host.onDidDispose) {
      host.onDidDispose(() => disposables.forEach((d) => d.dispose()));
    } else {
      this.disposables.push(...disposables);
    }
  }

  private async postSessions(host: WebviewHost, scope: "workspace" | "all" = "workspace"): Promise<void> {
    await host.webview.postMessage({ type: "piStatus", text: "Scanning sessions…" });
    const sessions = await listPiSessions(1000, scope).catch(() => []);
    await host.webview.postMessage({ type: "sessions", sessions, scope, limit: 1000 });
  }

  private applyLeafOverride(manager: any, sessionPath: string): void {
    if (!this.sessionLeafOverrides.has(sessionPath)) return;
    const leafId = this.sessionLeafOverrides.get(sessionPath) ?? null;
    if (leafId === null) {
      manager.resetLeaf();
    } else if (manager.getEntry?.(leafId)) {
      manager.branch(leafId);
    } else {
      this.sessionLeafOverrides.delete(sessionPath);
    }
  }

  private setLeafOverride(sessionPath: string, leafId: string | null): void {
    this.sessionLeafOverrides.set(sessionPath, leafId);
    this.displaySessionCache.delete(sessionPath);
  }

  private async postSession(panel: WebviewHost, uri: vscode.Uri): Promise<void> {
    await panel.webview.postMessage({ type: "sessionLoading", step: "read", text: "Opening session…" });
    const parsed = await this.readSessionForDisplay(uri, (text) => panel.webview.postMessage({ type: "sessionLoading", step: "read", text }));
    const cwd = parsed.header?.cwd ?? path.dirname(uri.fsPath);
    await panel.webview.postMessage({ type: "sessionLoading", step: "render", text: `Rendering ${parsed.editable.length} messages…` });
    await panel.webview.postMessage({
      type: "init",
      entries: parsed.editable,
      title: path.basename(uri.fsPath),
      cwd,
      mode: parsed.mode || "",
      modes: [],
      sessionName: parsed.sessionName,
    });

    await panel.webview.postMessage({ type: "sessionLoading", step: "modes", text: "Loading prompt modes…" });
    const modes = await loadPromptModeOptions(cwd);
    await panel.webview.postMessage({ type: "modesLoaded", modes, mode: parsed.mode || modes[0]?.value || "" });
  }

  private async readSessionForDisplay(uri: vscode.Uri, progress?: (text: string) => Thenable<boolean> | Promise<boolean>): Promise<DisplaySession> {
    const stat = await fs.promises.stat(uri.fsPath).catch(() => null);
    const overrideLeafId = this.sessionLeafOverrides.has(uri.fsPath) ? this.sessionLeafOverrides.get(uri.fsPath) : undefined;
    const cached = this.displaySessionCache.get(uri.fsPath);
    if (cached && cached.mtimeMs === (stat?.mtimeMs ?? 0) && cached.leafId === overrideLeafId) {
      void progress?.("Using cached session…");
      return cached.value;
    }

    void progress?.("Loading Pi session engine…");
    const { SessionManager } = await loadPiSdk();
    void progress?.("Opening session branch…");
    const manager = SessionManager.open(uri.fsPath);
    this.applyLeafOverride(manager, uri.fsPath);
    const header = manager.getHeader() ?? null;
    const cwd = manager.getCwd() || (typeof header?.cwd === "string" ? header.cwd : path.dirname(uri.fsPath));
    const branch = manager.getBranch();
    void progress?.(`Preparing ${branch.length} entries…`);
    const editable = branch
      .map((entry: SessionEntry) => this.toEditable(entry, cwd, uri.fsPath))
      .filter((entry: EditableSessionEntry | null): entry is EditableSessionEntry => !!entry);
    const value = { header, editable, mode: modeFromRawEntries(branch), sessionName: manager.getSessionName() ?? "" };
    this.displaySessionCache.set(uri.fsPath, { mtimeMs: stat?.mtimeMs ?? 0, leafId: overrideLeafId, value });
    return value;
  }

  private async readSessionLines(uri: vscode.Uri): Promise<{ header: SessionHeader | null; lines: SessionLine[] }> {
    const lines: SessionLine[] = [];
    let header: SessionHeader | null = null;

    const raw = await fs.promises.readFile(uri.fsPath, "utf8").catch((err: NodeJS.ErrnoException) => {
      if (err.code === "ENOENT") return "";
      throw err;
    });

    for (const line of raw.split(/\r?\n/)) {
      if (!line.trim()) continue;
      const entry = JSON.parse(line) as Record<string, any>;
      if (entry.type === "session") header = entry as SessionHeader;
      lines.push({ raw: line, entry });
    }

    return { header, lines };
  }

  private toEditable(entry: SessionEntry, cwd: string, sessionPath: string): EditableSessionEntry | null {
    if (entry.type === "message") {
      const role = entry.message?.role;
      if (role !== "user" && role !== "assistant" && role !== "toolResult") return null;
      return {
        id: entry.id,
        role: role === "toolResult" ? `tool:${entry.message.toolName ?? "result"}` : role,
        timestamp: entry.timestamp,
        markdown: messageContentToMarkdown(entry.message),
        cwd,
        sessionPath,
      };
    }
    if (entry.type === "custom_message") {
      return {
        id: entry.id,
        role: `custom:${entry.customType ?? "message"}`,
        timestamp: entry.timestamp,
        markdown: customContentToMarkdown(entry.content),
        cwd,
        sessionPath,
      };
    }
    return null;
  }

  private async setSessionName(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    const name = typeof message.sessionName === "string" ? message.sessionName.trim() : "";
    if (!name) return;
    const { SessionManager } = await loadPiSdk();
    const manager = SessionManager.open(uri.fsPath);
    this.applyLeafOverride(manager, uri.fsPath);
    manager.appendSessionInfo(name);
    this.setLeafOverride(uri.fsPath, manager.getLeafId());
    await panel.webview.postMessage({ type: "sessionNameChanged", sessionName: name });
  }

  private async getTree(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number") return;
    const { SessionManager } = await loadPiSdk();
    const manager = SessionManager.open(uri.fsPath);
    this.applyLeafOverride(manager, uri.fsPath);
    await this.postRpcResult(panel, message.id, buildTreeDto(manager));
  }

  private async setTreeLabel(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number" || !message.targetId) return;
    const { SessionManager } = await loadPiSdk();
    const manager = SessionManager.open(uri.fsPath);
    this.applyLeafOverride(manager, uri.fsPath);
    const activeLeafId = manager.getLeafId();
    const label = typeof message.label === "string" && message.label.trim() ? message.label.trim() : undefined;
    manager.appendLabelChange(message.targetId, label);
    this.setLeafOverride(uri.fsPath, activeLeafId);
    this.applyLeafOverride(manager, uri.fsPath);
    await this.postRpcResult(panel, message.id, buildTreeDto(manager));
  }

  private async navigateTree(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number" || !message.targetId) return;
    const { createAgentSession, getAgentDir, SessionManager } = await loadPiSdk();
    const manager = SessionManager.open(uri.fsPath);
    this.applyLeafOverride(manager, uri.fsPath);
    const cwd = manager.getCwd() || path.dirname(uri.fsPath);
    const created = await createAgentSession({ cwd, agentDir: getAgentDir(), sessionManager: manager });
    const session = created.session;
    try {
      await bindPiWebviewExtensions(session, panel);
      const result = await session.navigateTree(message.targetId, {
        summarize: !!message.summarize,
        customInstructions: typeof message.customInstructions === "string" && message.customInstructions.trim() ? message.customInstructions : undefined,
      });
      if (!result.cancelled && !result.aborted) {
        this.setLeafOverride(uri.fsPath, manager.getLeafId());
      }
      await this.postRpcResult(panel, message.id, {
        cancelled: result.cancelled,
        aborted: result.aborted,
        editorText: result.editorText ?? "",
        summaryEntryId: result.summaryEntry?.id,
        tree: buildTreeDto(manager),
      });
      if (!result.cancelled && !result.aborted) {
        await this.postSession(panel, uri);
      }
    } finally {
      session.dispose();
    }
  }

  private async withControlSession<T>(uri: vscode.Uri, fn: (session: AgentSession, manager: any, created: any) => Promise<T>): Promise<T> {
    const { createAgentSession, getAgentDir, SessionManager } = await loadPiSdk();
    const manager = SessionManager.open(uri.fsPath);
    this.applyLeafOverride(manager, uri.fsPath);
    const cwd = manager.getCwd() || path.dirname(uri.fsPath);
    const created = await createAgentSession({ cwd, agentDir: getAgentDir(), sessionManager: manager });
    const session = created.session;
    try {
      await bindPiWebviewExtensions(session, { webview: { postMessage: async () => true } as any });
      return await fn(session, manager, created);
    } finally {
      session.dispose();
    }
  }

  private runtimeControlsDto(session: AgentSession, created?: any): any {
    const current = session.model ? modelDto(session.model, session.modelRegistry) : null;
    const availableThinkingLevels = session.getAvailableThinkingLevels?.() ?? [];
    return {
      model: current,
      thinkingLevel: session.thinkingLevel,
      availableThinkingLevels,
      supportsThinking: session.supportsThinking?.() ?? availableThinkingLevels.length > 0,
      activeToolNames: session.getActiveToolNames?.() ?? [],
      modelFallbackMessage: created?.modelFallbackMessage,
    };
  }

  private async getRuntimeControlsState(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number") return;
    const result = await this.withControlSession(uri, async (session, _manager, created) => this.runtimeControlsDto(session, created));
    await this.postRpcResult(panel, message.id, result);
  }

  private async listModels(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number") return;
    const result = await this.withControlSession(uri, async (session) => {
      session.modelRegistry.refresh();
      const all = session.modelRegistry.getAll();
      const available = await Promise.resolve(session.modelRegistry.getAvailable());
      const availableKeys = new Set(available.map((model: any) => `${model.provider}/${model.id}`));
      return {
        current: session.model ? { provider: session.model.provider, id: session.model.id } : null,
        models: all.map((model: any) => ({
          ...modelDto(model, session.modelRegistry),
          available: availableKeys.has(`${model.provider}/${model.id}`),
          authStatus: session.modelRegistry.getProviderAuthStatus(model.provider),
        })),
        error: session.modelRegistry.getError(),
      };
    });
    await this.postRpcResult(panel, message.id, result);
  }

  private async setModel(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number" || !message.providerId || !message.modelId) return;
    const result = await this.withControlSession(uri, async (session, manager) => {
      session.modelRegistry.refresh();
      const model = session.modelRegistry.find(message.providerId!, message.modelId!);
      if (!model) throw new Error(`Model not found: ${message.providerId}/${message.modelId}`);
      await session.setModel(model);
      this.setLeafOverride(uri.fsPath, manager.getLeafId());
      return this.runtimeControlsDto(session);
    });
    await this.postRpcResult(panel, message.id, result);
    await this.postSession(panel, uri);
  }

  private async setThinkingLevel(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number" || typeof message.value !== "string") return;
    const result = await this.withControlSession(uri, async (session, manager) => {
      const levels = session.getAvailableThinkingLevels?.() ?? [];
      if (!levels.includes(message.value as any)) throw new Error(`Thinking level not available: ${message.value}`);
      session.setThinkingLevel(message.value as any);
      this.setLeafOverride(uri.fsPath, manager.getLeafId());
      return this.runtimeControlsDto(session);
    });
    await this.postRpcResult(panel, message.id, result);
    await this.postSession(panel, uri);
  }

  private async listAuthProviders(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number") return;
    const result = await this.withControlSession(uri, async (session) => authProvidersDto(session));
    await this.postRpcResult(panel, message.id, result);
  }

  private async loginApiKey(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number" || !message.providerId || typeof message.apiKey !== "string") return;
    const result = await this.withControlSession(uri, async (session) => {
      const apiKey = message.apiKey!.trim();
      if (!apiKey) throw new Error("API key cannot be empty.");
      session.modelRegistry.authStorage.set(message.providerId!, { type: "api_key", key: apiKey });
      session.modelRegistry.refresh();
      return { controls: this.runtimeControlsDto(session), auth: authProvidersDto(session) };
    });
    await this.postRpcResult(panel, message.id, result);
  }

  private async logoutProvider(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number" || !message.providerId) return;
    const result = await this.withControlSession(uri, async (session) => {
      session.modelRegistry.authStorage.logout(message.providerId!);
      session.modelRegistry.refresh();
      return { controls: this.runtimeControlsDto(session), auth: authProvidersDto(session) };
    });
    await this.postRpcResult(panel, message.id, result);
  }

  private async loginOAuth(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number" || !message.providerId) return;
    const providerId = message.providerId;
    const result = await this.withControlSession(uri, async (session) => {
      const provider = session.modelRegistry.authStorage.getOAuthProviders().find((candidate: any) => candidate.id === providerId);
      if (!provider) throw new Error(`OAuth provider not found: ${providerId}`);
      const abort = new AbortController();
      await session.modelRegistry.authStorage.login(providerId as any, {
        onAuth: (info: any) => {
          void panel.webview.postMessage({ type: "authFlow", event: "auth", providerId, url: info.url, instructions: info.instructions });
          if (info.url) void vscode.env.openExternal(vscode.Uri.parse(info.url));
        },
        onPrompt: async (prompt: any) => (await this.waitForAuthPrompt(panel, providerId, prompt.message, prompt.placeholder)) ?? "",
        onProgress: (text: string) => {
          void panel.webview.postMessage({ type: "authFlow", event: "progress", providerId, text });
        },
        onSelect: async (prompt: any) => {
          const labels = (prompt.options || []).map((option: any) => option.label).join(" / ");
          const selected = await this.waitForAuthPrompt(panel, providerId, `${prompt.message} (${labels})`, prompt.options?.[0]?.label);
          return prompt.options?.find((option: any) => option.id === selected || option.label === selected)?.id;
        },
        onManualCodeInput: async () => (await this.waitForAuthPrompt(panel, providerId, "Paste redirect URL or code", "")) ?? "",
        signal: abort.signal,
      });
      session.modelRegistry.refresh();
      return { controls: this.runtimeControlsDto(session), auth: authProvidersDto(session) };
    });
    await this.postRpcResult(panel, message.id, result);
  }

  private waitForAuthPrompt(panel: WebviewHost, providerId: string, prompt: string, placeholder?: string): Promise<string | undefined> {
    const promptId = crypto.randomUUID();
    void panel.webview.postMessage({ type: "authFlow", event: "prompt", providerId, promptId, prompt, placeholder });
    return new Promise((resolve) => {
      this.authPromptResolvers.set(promptId, (value) => {
        this.authPromptResolvers.delete(promptId);
        resolve(value);
      });
    });
  }

  private async updateEntry(panel: WebviewHost, uri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (!message.entryId || typeof message.text !== "string") return;
    const parsed = await this.readSessionLines(uri);
    let changed = false;

    for (const line of parsed.lines) {
      if (line.entry.id !== message.entryId) continue;
      if (line.entry.type === "message" && line.entry.message) {
        line.entry.message.content = markdownToMessageContent(line.entry.message.content, message.text);
        line.raw = JSON.stringify(line.entry);
        changed = true;
      } else if (line.entry.type === "custom_message") {
        line.entry.content = markdownToCustomContent(line.entry.content, message.text);
        line.raw = JSON.stringify(line.entry);
        changed = true;
      }
      break;
    }

    if (!changed) return;
    await fs.promises.writeFile(uri.fsPath, parsed.lines.map((line) => line.raw).join("\n") + "\n", "utf8");
    this.displaySessionCache.delete(uri.fsPath);
    await panel.webview.postMessage({ type: "saved", entryId: message.entryId });
  }

  private async createNewSession(panel: WebviewHost): Promise<vscode.Uri> {
    const cwd = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? process.cwd();
    const timestamp = new Date().toISOString();
    const id = crypto.randomUUID();
    const dir = getDefaultSessionDirFast(cwd);
    const file = path.join(dir, `${timestamp.replace(/[:.]/g, "-")}_${id}.jsonl`);
    const header: SessionHeader = { type: "session", version: 3, id, timestamp, cwd };
    await fs.promises.mkdir(dir, { recursive: true });
    await fs.promises.writeFile(file, `${JSON.stringify(header)}\n`, "utf8");
    this.sessionLeafOverrides.delete(file);
    this.displaySessionCache.delete(file);
    await panel.webview.postMessage({ type: "piStatus", text: `Ready: ${shortPath(file)}` });
    return vscode.Uri.file(file);
  }

  private async setMode(panel: WebviewHost, sessionUri: vscode.Uri, message: WebviewMessage): Promise<void> {
    const mode = await parsePromptModeForSession(sessionUri, message.mode);
    if (!mode) return;
    await this.runModeCommand(panel, sessionUri, mode);
    await panel.webview.postMessage({ type: "modeChanged", mode });
  }

  private async runModeCommand(panel: WebviewHost, sessionUri: vscode.Uri, mode: string): Promise<void> {
    const { createAgentSession, getAgentDir, SessionManager } = await loadPiSdk();
    const manager = SessionManager.open(sessionUri.fsPath);
    this.applyLeafOverride(manager, sessionUri.fsPath);
    const cwd = manager.getCwd() || path.dirname(sessionUri.fsPath);
    const created = await createAgentSession({ cwd, agentDir: getAgentDir(), sessionManager: manager });
    const session = created.session;
    try {
      await bindPiWebviewExtensions(session, panel);
      await session.prompt(`/${MODE_COMMAND} ${mode}`);
      this.setLeafOverride(sessionUri.fsPath, manager.getLeafId());
    } finally {
      session.dispose();
    }
  }

  private async getModeDefinition(panel: WebviewHost, sessionUri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number") return;
    const mode = typeof message.mode === "string" ? message.mode : "";
    const definition = await loadPromptModeDefinition(sessionUri, mode);
    await this.postRpcResult(panel, message.id, definition);
  }

  private async saveModeDefinition(panel: WebviewHost, sessionUri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number") return;
    const definition = normalizePromptModeDefinition(message.modeDefinition);
    if (!definition) {
      await this.postRpcError(panel, message.id, "Mode requires key, label, and opener. Key must start with a letter and contain only letters, numbers, hyphens, or underscores.");
      return;
    }
    await savePromptModeDefinition(definition);
    const cwd = await sessionCwd(sessionUri);
    const modes = await loadPromptModeOptions(cwd);
    await this.postRpcResult(panel, message.id, { mode: definition.key, modes });
  }

  private async getSystemPrompt(panel: WebviewHost, sessionUri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number") return;
    try {
      const text = await reconstructSystemPrompt(sessionUri, typeof message.text === "string" ? message.text : "");
      await this.postRpcResult(panel, message.id, { text });
    } catch (err: unknown) {
      await this.postRpcError(panel, message.id, err instanceof Error ? err.message : String(err));
    }
  }

  private async sendUserMessage(
    panel: WebviewHost,
    sessionUri: vscode.Uri,
    message: WebviewMessage,
  ): Promise<void> {
    if (typeof message.text !== "string" || !message.text.trim()) return;

    const { createAgentSession, getAgentDir, SessionManager } = await loadPiSdk();
    const manager = SessionManager.open(sessionUri.fsPath);
    this.applyLeafOverride(manager, sessionUri.fsPath);
    const requestedMode = await parsePromptModeForSession(sessionUri, message.mode);
    const cwd = manager.getCwd() || path.dirname(sessionUri.fsPath);
    await panel.webview.postMessage({ type: "piStatus", text: "Starting Pi SDK session…" });

    let sdkSession: AgentSession | undefined;
    try {
      const created = await createAgentSession({
        cwd,
        agentDir: getAgentDir(),
        sessionManager: manager,
      });
      sdkSession = created.session;
      await bindPiWebviewExtensions(sdkSession, panel);
      if (requestedMode && modeFromRawEntries(manager.getBranch()) !== requestedMode) {
        await sdkSession.prompt(`/${MODE_COMMAND} ${requestedMode}`);
      }
      if (created.modelFallbackMessage) {
        await panel.webview.postMessage({ type: "piStatus", text: created.modelFallbackMessage });
      }

      let postedSystemPrompt = false;
      const postSystemPrompt = () => {
        if (postedSystemPrompt || !sdkSession) return;
        postedSystemPrompt = true;
        void panel.webview.postMessage({ type: "piSystemPrompt", text: sdkSession.systemPrompt });
      };
      const unsubscribe = sdkSession.subscribe((event) => {
        if (event.type === "agent_start" || (event.type === "message_start" && event.message.role === "assistant")) {
          postSystemPrompt();
        }
        void this.forwardPiEvent(panel, event);
      });
      try {
        await sdkSession.prompt(message.text);
      } finally {
        unsubscribe();
      }

      this.setLeafOverride(sessionUri.fsPath, manager.getLeafId());
      await panel.webview.postMessage({ type: "piDone", success: true, reload: false });
      await this.postSession(panel, sessionUri);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      await panel.webview.postMessage({ type: "showError", message: msg });
      await panel.webview.postMessage({ type: "piDone", success: false, reload: false });
    } finally {
      sdkSession?.dispose();
    }
  }

  private async forwardPiEvent(panel: WebviewHost, event: AgentSessionEvent): Promise<void> {
    if (event.type === "message_start" && event.message.role === "assistant") {
      await panel.webview.postMessage({ type: "piAssistantStart", id: `assistant-${Date.now()}` });
      return;
    }

    if (event.type === "message_update" || event.type === "message_end") {
      const text = assistantMarkdownFromEvent(event);
      if (text !== null) {
        await panel.webview.postMessage({ type: "piStream", text });
        return;
      }
    }

    if (event.type === "tool_execution_start") {
      await panel.webview.postMessage({
        type: "piToolStart",
        toolCallId: event.toolCallId,
        toolName: event.toolName,
        args: event.args,
      });
    } else if (event.type === "tool_execution_update") {
      await panel.webview.postMessage({
        type: "piToolUpdate",
        toolCallId: event.toolCallId,
        toolName: event.toolName,
        markdown: toolEventMarkdown(event.toolName, event.partialResult, false),
      });
    } else if (event.type === "tool_execution_end") {
      await panel.webview.postMessage({
        type: "piToolEnd",
        toolCallId: event.toolCallId,
        toolName: event.toolName,
        markdown: toolEventMarkdown(event.toolName, event.result, event.isError),
      });
    } else if (event.type === "compaction_start") {
      await panel.webview.postMessage({ type: "piStatus", text: `Pi compaction: ${event.reason}` });
    } else if (event.type === "auto_retry_start") {
      await panel.webview.postMessage({ type: "piStatus", text: `Pi retry ${event.attempt}/${event.maxAttempts}…` });
    } else if (event.type === "agent_start") {
      await panel.webview.postMessage({ type: "piStatus", text: "Pi is responding…" });
    }
  }

  private async handleRatRun(
    panel: WebviewHost,
    sessionUri: vscode.Uri,
    message: WebviewMessage,
  ): Promise<void> {
    if (typeof message.id !== "number") return;
    const runtime = await this.runtimeForMessage(sessionUri, message.language);
    if (!runtime) {
      await panel.webview.postMessage({
        type: "ratDone",
        id: message.id,
        success: false,
        stdout: "",
        stderr: `No rat runtime for language: ${message.language ?? ""}`,
        error: { message: `No rat runtime for language: ${message.language ?? ""}` },
      });
      return;
    }

    const document = await vscode.workspace.openTextDocument(sessionUri);
    this.execution.enqueue({
      kind: "webview",
      id: message.id,
      code: message.code ?? "",
      document,
      webviewPanel: panel,
      runtimeName: runtime.name,
      cwd: runtime.cwd,
      lang: runtime.lang,
    });
  }

  private async handleRatComplete(panel: WebviewHost, sessionUri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number") return;
    const runtime = await this.runtimeForMessage(sessionUri, message.language);
    if (!runtime) return this.postRpcResult(panel, message.id, { items: [] });
    const client = await existingOrRunningClient(runtime.name);
    if (!client) return this.postRpcResult(panel, message.id, { items: [] });
    try {
      const items = await client.complete(message.code ?? "", message.cursor ?? 0);
      await this.postRpcResult(panel, message.id, { items });
    } catch {
      await this.postRpcResult(panel, message.id, { items: [] });
    }
  }

  private async handleRatInspect(panel: WebviewHost, sessionUri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number") return;
    const runtime = await this.runtimeForMessage(sessionUri, message.language);
    if (!runtime) return this.postRpcResult(panel, message.id, { text: "" });
    const client = await existingOrRunningClient(runtime.name);
    if (!client) return this.postRpcResult(panel, message.id, { text: "" });
    try {
      const text = message.at ? await client.look(message.at) : await client.look();
      await this.postRpcResult(panel, message.id, { text });
    } catch {
      await this.postRpcResult(panel, message.id, { text: "" });
    }
  }

  private async handleRatAsset(panel: WebviewHost, sessionUri: vscode.Uri, message: WebviewMessage): Promise<void> {
    if (typeof message.id !== "number") return;
    try {
      const cwd = await sessionCwd(sessionUri);
      const src = localPathFromAssetUrl(message.url ?? "");
      if (!src || !fs.existsSync(src)) throw new Error(`Asset not found: ${message.url ?? ""}`);
      const assetsAbs = path.join(cwd, "_assets");
      await fs.promises.mkdir(assetsAbs, { recursive: true });
      const dest = uniquePath(path.join(assetsAbs, path.basename(src)));
      await fs.promises.copyFile(src, dest);
      await this.postRpcResult(panel, message.id, { assetPath: dest, relativePath: path.relative(cwd, dest).split(path.sep).join("/") });
    } catch (err: unknown) {
      await this.postRpcError(panel, message.id, err instanceof Error ? err.message : String(err));
    }
  }

  private async runtimeForMessage(sessionUri: vscode.Uri, language: string | undefined): Promise<{ name: string; cwd: string; lang: string } | null> {
    const ratLang = ratLangForFence(language);
    if (!ratLang) return null;
    const cwd = await sessionCwd(sessionUri);
    return { name: projectRuntimeName(ratLang, cwd), cwd, lang: ratLang };
  }

  private async postRpcResult(panel: WebviewHost, id: number, result: unknown): Promise<void> {
    await panel.webview.postMessage({ type: "rpcResult", id, result });
  }

  private async postRpcError(panel: WebviewHost, id: number, error: string): Promise<void> {
    await panel.webview.postMessage({ type: "rpcError", id, error });
  }

  private html(webview: vscode.Webview, uri: vscode.Uri | undefined): string {
    const nonce = getNonce();
    const mrmdUri = webview.asWebviewUri(vscode.Uri.joinPath(this.ctx.extensionUri, "media", "mrmd.iife.min.js"));
    const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(this.ctx.extensionUri, "media", "piSessionEditor.js"));
    const title = uri ? escapeHtml(path.basename(uri.fsPath)) : "Pi Sessions";

    return /* html */ `<!doctype html>
<html>
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src ${webview.cspSource} https: http: data: blob:; font-src ${webview.cspSource} data:; style-src ${webview.cspSource} 'unsafe-inline'; script-src ${webview.cspSource} 'nonce-${nonce}' 'unsafe-eval';">
  <title>${title}</title>
  <style nonce="${nonce}">
    html, body { margin: 0; padding: 0; color: var(--vscode-editor-foreground); background: var(--vscode-editor-background); font-family: var(--vscode-font-family); height: 100%; overflow: hidden; }
    #toolbar {
      position: sticky;
      top: 0;
      z-index: 100;
      display: flex;
      gap: 8px;
      align-items: center;
      padding: 7px 14px;
      border-bottom: 1px solid color-mix(in srgb, var(--vscode-editor-foreground) 8%, transparent);
      background: color-mix(in srgb, var(--vscode-editor-background) 94%, transparent);
      backdrop-filter: blur(10px);
    }
    #view-title { font-weight: 600; letter-spacing: .01em; }
    #status { opacity: .58; font-size: 11px; }
    #session-name {
      flex: 0 1 360px;
      min-width: 120px;
      max-width: 40vw;
      margin: 0 auto;
      color: var(--vscode-input-foreground);
      background: transparent;
      border: 1px solid transparent;
      border-radius: 4px;
      padding: 2px 8px;
      font: inherit;
      font-size: 12px;
      font-weight: 600;
      line-height: 18px;
      text-align: center;
      outline: none;
    }
    #session-name:hover { border-color: var(--vscode-input-border, color-mix(in srgb, var(--vscode-editor-foreground) 14%, transparent)); background: var(--vscode-input-background); }
    #session-name:focus { border-color: var(--vscode-focusBorder); background: var(--vscode-input-background); outline: 1px solid var(--vscode-focusBorder); outline-offset: -1px; }
    #session-name:disabled { opacity: 0; pointer-events: none; }
    button {
      color: var(--vscode-descriptionForeground);
      background: transparent;
      border: 1px solid color-mix(in srgb, var(--vscode-editor-foreground) 10%, transparent);
      border-radius: 6px;
      padding: 2px 8px;
      cursor: pointer;
      font: inherit;
      line-height: 18px;
    }
    button:hover { color: var(--vscode-foreground); background: color-mix(in srgb, var(--vscode-editor-foreground) 7%, transparent); }
    #mode-select {
      color: var(--vscode-dropdown-foreground, var(--vscode-foreground));
      background: var(--vscode-dropdown-background, var(--vscode-input-background));
      border: 1px solid var(--vscode-dropdown-border, var(--vscode-input-border, color-mix(in srgb, var(--vscode-editor-foreground) 16%, transparent)));
      border-radius: 2px;
      padding: 2px 22px 2px 6px;
      font: inherit;
      font-size: 11px;
      line-height: 18px;
      max-width: 170px;
      height: 24px;
      outline: none;
      color-scheme: light dark;
    }
    #mode-select:hover {
      background: var(--vscode-list-hoverBackground, var(--vscode-dropdown-background, var(--vscode-input-background)));
      border-color: var(--vscode-focusBorder, var(--vscode-dropdown-border, var(--vscode-input-border)));
    }
    #mode-select:focus {
      border-color: var(--vscode-focusBorder);
      outline: 1px solid var(--vscode-focusBorder);
      outline-offset: -1px;
    }
    #mode-select:disabled { opacity: 0.45; }
    #mode-select option {
      color: var(--vscode-dropdown-foreground, var(--vscode-foreground));
      background: var(--vscode-dropdown-background, var(--vscode-input-background));
    }
    button.secondary { color: var(--vscode-descriptionForeground); background: transparent; border-color: color-mix(in srgb, var(--vscode-editor-foreground) 10%, transparent); }
    button.secondary:hover { background: color-mix(in srgb, var(--vscode-editor-foreground) 7%, transparent); }
    button.icon { width: 28px; height: 24px; padding: 0; display: inline-flex; align-items: center; justify-content: center; }

    #container { height: calc(100% - 40px); overflow-y: auto; display: flex; flex-direction: column; }
    #home-view, #editor-view { display: none; padding: 12px; flex: 1; }
    #home-view.active, #editor-view.active { display: block; }

    /* Home View (Session List) */
    #home-view { max-width: 800px; margin: 0 auto; width: 100%; box-sizing: border-box; }
    #search-container { position: sticky; top: 0; background: var(--vscode-editor-background); padding: 4px 0 12px; z-index: 10; }
    #search { width: 100%; box-sizing: border-box; padding: 6px 10px; font: inherit; color: var(--vscode-input-foreground); background: var(--vscode-input-background); border: 1px solid var(--vscode-input-border, transparent); }
    .session-item { display: block; width: 100%; padding: 10px 12px; margin: 0 0 8px; text-align: left; border: 1px solid var(--vscode-panel-border); border-radius: 6px; color: var(--vscode-foreground); background: var(--vscode-editor-background); cursor: pointer; }
    .session-item:hover { background: var(--vscode-list-hoverBackground); border-color: var(--vscode-focusBorder); }
    .session-item .title { display: block; font-weight: 600; margin-bottom: 4px; }
    .session-item .meta { display: block; color: var(--vscode-descriptionForeground); font-size: 11px; }

    /* Editor View */
    #session { max-width: 1040px; margin: 0 auto; padding-bottom: 20px; }
    .card {
      position: relative;
      margin: 0 0 12px;
      border: 1px solid color-mix(in srgb, var(--vscode-editor-foreground) 8%, transparent);
      border-radius: 8px;
      overflow: hidden;
      background: color-mix(in srgb, var(--vscode-editor-foreground) 2.5%, var(--vscode-editor-background));
      box-shadow: none;
    }
    .card > header {
      position: absolute;
      top: 8px;
      right: 8px;
      z-index: 5;
      display: flex;
      gap: 6px;
      align-items: center;
      padding: 0;
      border: 0;
      background: transparent;
      opacity: 0.38;
      transition: opacity 120ms ease;
    }
    .card:hover > header, .card.editable > header { opacity: 1; }
    .role-dot {
      display: inline-block;
      width: 5px;
      height: 5px;
      border-radius: 999px;
      background: var(--vscode-descriptionForeground);
      opacity: 0.7;
    }
    .role-system {
      border-style: dashed;
      border-color: color-mix(in srgb, var(--vscode-descriptionForeground) 20%, transparent);
      background: color-mix(in srgb, var(--vscode-editor-foreground) 1.8%, var(--vscode-editor-background));
    }
    .role-system details { padding: 8px 12px; }
    .role-system summary { cursor: pointer; color: var(--vscode-descriptionForeground); font-size: 11px; user-select: none; }
    .role-system .system-prompt-hint { opacity: 0.62; margin-left: 6px; font-weight: 400; }
    .role-system pre {
      margin: 8px 0 0;
      padding: 10px;
      max-height: 55vh;
      overflow: auto;
      white-space: pre-wrap;
      color: var(--vscode-editor-foreground);
      background: var(--vscode-textCodeBlock-background, color-mix(in srgb, var(--vscode-editor-foreground) 6%, transparent));
      border: 1px solid var(--vscode-panel-border, transparent);
      border-radius: 4px;
      font-family: var(--vscode-editor-font-family, monospace);
      font-size: 12px;
      line-height: 1.45;
    }
    .role-user { border-color: color-mix(in srgb, var(--vscode-textLink-foreground) 22%, transparent); }
    .role-assistant {
      border-color: transparent;
      background: transparent;
    }
    .role-assistant > header { display: none; }
    .role-tool, [class*="role-tool"] {
      border-style: dashed;
      border-color: color-mix(in srgb, var(--vscode-charts-orange) 18%, transparent);
    }
    .role-user .role-dot { background: var(--vscode-textLink-foreground); }
    .role-assistant .role-dot { background: var(--vscode-descriptionForeground); }
    .role-tool .role-dot, [class*="role-tool"] .role-dot { background: var(--vscode-charts-orange); }
    .card > header .save {
      display: none;
      margin: 0;
      padding: 1px 6px;
      color: var(--vscode-descriptionForeground);
      background: color-mix(in srgb, var(--vscode-editor-foreground) 6%, transparent);
      border: 1px solid color-mix(in srgb, var(--vscode-editor-foreground) 10%, transparent);
      border-radius: 999px;
      font-size: 10px;
      line-height: 16px;
    }
    .card:hover > header .save, .card.editable > header .save { display: inline-block; }
    .editor { min-height: 34px; }
    
    #composer {
      position: sticky;
      bottom: 0;
      z-index: 50;
      max-width: 1100px;
      margin: 20px auto 0;
      padding: 12px 0 14px;
      border-top: 1px solid var(--vscode-panel-border);
      background: var(--vscode-editor-background);
      box-shadow: 0 -12px 24px var(--vscode-editor-background);
    }
    #compose { border: 1px solid var(--vscode-panel-border); border-radius: 6px; min-height: 120px; max-height: 45vh; overflow: auto; }
    #composer header { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; color: var(--vscode-descriptionForeground); font-size: 12px; }
    #composer header .hint { opacity: .62; font-size: 11px; }
    #composer header .compose-spacer { flex: 1; }
    .compose-actions { display: flex; align-items: center; justify-content: flex-end; flex-wrap: wrap; gap: 6px; }
    .compose-actions button, .compose-actions select {
      height: 24px;
      min-width: 0;
      color: var(--vscode-descriptionForeground);
      background: color-mix(in srgb, var(--vscode-editor-foreground) 2%, transparent);
      border: 1px solid color-mix(in srgb, var(--vscode-editor-foreground) 12%, transparent);
      border-radius: 999px;
      padding: 1px 9px;
      font: inherit;
      font-size: 11px;
      line-height: 18px;
    }
    .compose-actions button:hover, .compose-actions select:hover { color: var(--vscode-foreground); background: color-mix(in srgb, var(--vscode-editor-foreground) 7%, transparent); border-color: color-mix(in srgb, var(--vscode-focusBorder) 55%, transparent); }
    .compose-actions button:disabled, .compose-actions select:disabled { opacity: .45; cursor: default; }
    .compose-actions .primary-send { color: var(--vscode-button-foreground); background: var(--vscode-button-background); border-color: var(--vscode-button-background); font-weight: 600; }
    .compose-actions .primary-send:hover { background: var(--vscode-button-hoverBackground); }
    .compose-actions .new-chat { width: 24px; padding: 0; font-size: 15px; }
    .compose-actions #mode-select { max-width: 138px; padding-right: 18px; color-scheme: light dark; }

    .modal-backdrop {
      position: fixed;
      inset: 0;
      z-index: 1000;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 24px;
      background: color-mix(in srgb, var(--vscode-editor-background) 62%, transparent);
    }
    .modal {
      width: min(720px, 100%);
      max-height: min(760px, 92vh);
      overflow: auto;
      color: var(--vscode-foreground);
      background: var(--vscode-editorWidget-background, var(--vscode-quickInput-background, var(--vscode-editor-background)));
      border: 1px solid var(--vscode-editorWidget-border, var(--vscode-focusBorder));
      border-radius: 6px;
      box-shadow: 0 8px 32px color-mix(in srgb, #000 35%, transparent);
    }
    .modal header { padding: 14px 16px 8px; border-bottom: 1px solid var(--vscode-panel-border); }
    .modal header h2 { margin: 0; font-size: 14px; font-weight: 600; }
    .modal form { padding: 14px 16px 16px; }
    .modal label { display: block; margin: 0 0 12px; color: var(--vscode-descriptionForeground); font-size: 12px; }
    .modal fieldset { margin: 0 0 12px; padding: 10px 12px; border: 1px solid var(--vscode-panel-border); border-radius: 4px; }
    .modal legend { padding: 0 4px; color: var(--vscode-descriptionForeground); font-size: 12px; }
    .modal .check-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 6px 12px; }
    .modal .check-grid label { display: flex; align-items: center; gap: 7px; margin: 0; }
    .modal .check-grid input { width: auto; margin: 0; }
    .modal .hint { margin: -4px 0 12px; color: var(--vscode-descriptionForeground); opacity: .72; font-size: 11px; }
    .modal input, .modal textarea {
      display: block;
      width: 100%;
      box-sizing: border-box;
      margin-top: 5px;
      color: var(--vscode-input-foreground);
      background: var(--vscode-input-background);
      border: 1px solid var(--vscode-input-border, transparent);
      border-radius: 2px;
      padding: 6px 8px;
      font: inherit;
      outline: none;
    }
    .modal input:focus, .modal textarea:focus { border-color: var(--vscode-focusBorder); outline: 1px solid var(--vscode-focusBorder); outline-offset: -1px; }
    .modal textarea { min-height: 86px; resize: vertical; font-family: var(--vscode-editor-font-family, monospace); font-size: 12px; line-height: 1.45; }
    .modal .actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 14px; }
    .modal .primary { color: var(--vscode-button-foreground); background: var(--vscode-button-background); border-color: var(--vscode-button-background); }
    .modal .primary:hover { background: var(--vscode-button-hoverBackground); }

    .tree-modal { width: min(1180px, 100%); height: min(820px, 94vh); display: flex; flex-direction: column; }
    .tree-modal header { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
    .tree-layout { display: grid; grid-template-columns: minmax(300px, 1fr) minmax(280px, 420px); gap: 12px; min-height: 0; padding: 12px; flex: 1; }
    .tree-panel { min-height: 0; border: 1px solid var(--vscode-panel-border); border-radius: 6px; overflow: hidden; background: color-mix(in srgb, var(--vscode-editor-foreground) 2%, transparent); }
    .tree-left { display: flex; flex-direction: column; }
    .tree-controls { display: flex; gap: 8px; padding: 8px; border-bottom: 1px solid var(--vscode-panel-border); }
    .tree-controls input, .tree-controls select, .tree-actions input, .tree-actions select, .tree-actions textarea { color: var(--vscode-input-foreground); background: var(--vscode-input-background); border: 1px solid var(--vscode-input-border, transparent); border-radius: 2px; padding: 5px 7px; font: inherit; font-size: 12px; }
    .tree-controls input { flex: 1; }
    .tree-list { min-height: 0; overflow: auto; padding: 6px; }
    .tree-node { width: 100%; display: flex; align-items: center; gap: 6px; margin: 0; padding-top: 5px; padding-bottom: 5px; border: 0; border-radius: 4px; text-align: left; color: var(--vscode-foreground); }
    .tree-node:hover { background: var(--vscode-list-hoverBackground); }
    .tree-node.selected { outline: 1px solid var(--vscode-focusBorder); background: var(--vscode-list-activeSelectionBackground, var(--vscode-list-hoverBackground)); }
    .tree-node.active .tree-node-label { font-weight: 600; }
    .tree-node:not(.active) { opacity: .78; }
    .tree-node.leaf { opacity: 1; }
    .tree-toggle { width: 16px; flex: 0 0 16px; color: var(--vscode-descriptionForeground); }
    .tree-node-label { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .tree-badges { display: inline-flex; gap: 4px; flex: 0 0 auto; }
    .tree-badge { padding: 0 5px; border-radius: 999px; font-size: 10px; line-height: 16px; color: var(--vscode-badge-foreground); background: var(--vscode-badge-background); }
    .tree-badge.label { color: var(--vscode-editor-foreground); background: color-mix(in srgb, var(--vscode-charts-yellow) 28%, transparent); }
    .tree-right { display: flex; flex-direction: column; min-height: 0; }
    .tree-details { flex: 1; min-height: 0; overflow: auto; padding: 10px; }
    .tree-details pre { white-space: pre-wrap; margin: 8px 0 0; font-family: var(--vscode-editor-font-family, monospace); font-size: 12px; line-height: 1.45; }
    .tree-meta, .muted { color: var(--vscode-descriptionForeground); font-size: 11px; }
    .error { color: var(--vscode-errorForeground); }
    .tree-actions { padding: 10px; border-top: 1px solid var(--vscode-panel-border); display: grid; gap: 8px; }
    .tree-label-row { display: flex; gap: 6px; }
    .tree-label-row input { flex: 1; }
    .tree-actions textarea { min-height: 62px; resize: vertical; }
    .tree-actions .primary { justify-self: end; }
    .picker-modal { width: min(980px, 100%); height: min(720px, 92vh); display: flex; flex-direction: column; }
    .picker-layout { display: grid; grid-template-columns: minmax(280px, 1fr) minmax(280px, 380px); gap: 12px; min-height: 0; padding: 12px; flex: 1; }
    .picker-list { min-height: 0; overflow: auto; padding: 6px; border: 1px solid var(--vscode-panel-border); border-radius: 6px; }
    .picker-search { margin: 12px 12px 0; }
    .picker-row { display: block; width: 100%; margin: 0 0 5px; padding: 8px 10px; text-align: left; border: 1px solid transparent; border-radius: 5px; }
    .picker-row:hover { background: var(--vscode-list-hoverBackground); }
    .picker-row.selected { border-color: var(--vscode-focusBorder); background: var(--vscode-list-activeSelectionBackground, var(--vscode-list-hoverBackground)); }
    .picker-row.unavailable { opacity: .55; }
    .picker-row .picker-title { display: block; font-weight: 600; color: var(--vscode-foreground); }
    .picker-row .picker-meta { display: block; margin-top: 2px; color: var(--vscode-descriptionForeground); font-size: 11px; }
    .picker-details { min-height: 0; overflow: auto; padding: 12px; border: 1px solid var(--vscode-panel-border); border-radius: 6px; }
    .picker-details h3 { margin: 0 0 6px; font-size: 14px; }
    .details-grid { display: grid; grid-template-columns: max-content 1fr; gap: 6px 10px; margin: 12px 0; font-size: 12px; }
    .details-grid dt { color: var(--vscode-descriptionForeground); }
    .thinking-modal { width: min(520px, 100%); }
    .auth-actions { display: grid; gap: 8px; padding: 12px; border-top: 1px solid var(--vscode-panel-border); }
    .auth-actions input { color: var(--vscode-input-foreground); background: var(--vscode-input-background); border: 1px solid var(--vscode-input-border, transparent); border-radius: 2px; padding: 6px 8px; font: inherit; }
    .auth-action-row { display: flex; justify-content: flex-end; gap: 8px; }
    .role-user { color: var(--vscode-textLink-foreground); }
    .role-assistant { color: var(--vscode-charts-green); }
    .role-tool-result, .role-tool { color: var(--vscode-charts-orange); }
    @media (max-width: 760px) { .tree-layout { grid-template-columns: 1fr; } }

    .hidden { display: none !important; }
  </style>
</head>
<body class="${uri ? "mode-editor" : "mode-home"}">
  <div id="toolbar">
    <button id="go-home" class="secondary icon ${uri ? "" : "hidden"}" title="Session history">
      <svg width="15" height="15" viewBox="0 0 16 16" aria-hidden="true"><path fill="currentColor" d="M8 2a6 6 0 1 1-5.2 3H1.5a.5.5 0 0 1 0-1H4v2.5a.5.5 0 0 1-1 0v-.72A5 5 0 1 0 8 3a4.98 4.98 0 0 0-3.54 1.46.5.5 0 0 1-.7-.71A5.98 5.98 0 0 1 8 2Zm.5 3.5v2.3l1.85 1.1a.5.5 0 1 1-.5.86l-2.1-1.25A.5.5 0 0 1 7.5 8V5.5a.5.5 0 0 1 1 0Z"/></svg>
    </button>
    <strong id="view-title">${uri ? "Pi" : "Sessions"}</strong>
    <span id="status">Loading…</span>
    <input id="session-name" title="Session name" value="Untitled" ${uri ? "" : "disabled"} />
    <button id="all-sessions" class="secondary ${uri ? "hidden" : ""}" title="Toggle workspace/all sessions">All</button>
    <button id="reload" class="secondary" title="Reload">↻</button>
  </div>
  
  <div id="container">
    <div id="home-view" class="${uri ? "" : "active"}">
      <div id="search-container">
        <input id="search" placeholder="Search sessions by content, path, or date…" autofocus />
      </div>
      <div id="session-list"></div>
    </div>

    <div id="editor-view" class="${uri ? "active" : ""}">
      <main id="session"></main>
      <section id="composer">
        <header>
          <strong>New message</strong><span class="hint">Alt+Enter to send</span><span class="compose-spacer"></span>
          <div class="compose-actions" aria-label="Conversation controls">
            <select id="mode-select" title="Prompt mode" ${uri ? "" : "disabled"}></select>
            <button id="model-button" type="button" title="Select model" ${uri ? "" : "disabled"}>Model</button>
            <button id="thinking-button" type="button" title="Thinking level" ${uri ? "" : "disabled"}>Think</button>
            <button id="auth-button" type="button" title="Login/logout providers" ${uri ? "" : "disabled"}>Auth</button>
            <button id="tree-button" type="button" title="Conversation tree" ${uri ? "" : "disabled"}>Tree</button>
            <button id="new-session" type="button" class="new-chat" title="New session">＋</button>
            <button id="send" type="button" class="primary-send" title="Send to Pi (Alt+Enter)">Send ↵</button>
          </div>
        </header>
        <div id="compose"></div>
      </section>
    </div>
  </div>

  <div id="mode-dialog" class="modal-backdrop hidden" role="dialog" aria-modal="true" aria-labelledby="mode-dialog-title">
    <section class="modal">
      <header><h2 id="mode-dialog-title">Add mode</h2></header>
      <form id="mode-form">
        <label>Key
          <input id="mode-key" name="key" placeholder="my-mode" pattern="[a-z][a-z0-9_-]*" required />
        </label>
        <label>Label
          <input id="mode-label" name="label" placeholder="My Mode" required />
        </label>
        <label>Opening system prompt line
          <textarea id="mode-opener" name="opener" placeholder="You are…" required></textarea>
        </label>
        <label>Optional appendix
          <textarea id="mode-appendix" name="appendix" placeholder="Additional instructions appended to the system prompt for this mode…"></textarea>
        </label>
        <label>Optional full system prompt override
          <textarea id="mode-system-prompt" name="systemPrompt" placeholder="If filled, this becomes the whole base system prompt for this mode before section removals and appendix."></textarea>
        </label>
        <p class="hint">Leave this empty to use pi's normal reconstructed prompt with this mode's opener.</p>
        <fieldset>
          <legend>Remove reconstructed sections</legend>
          <div class="check-grid">
            <label><input type="checkbox" data-mode-section="available_tools" /> Available tools</label>
            <label><input type="checkbox" data-mode-section="custom_tools_note" /> Custom tools note</label>
            <label><input type="checkbox" data-mode-section="guidelines" /> Guidelines</label>
            <label><input type="checkbox" data-mode-section="pi_docs" /> Pi docs block</label>
            <label><input type="checkbox" data-mode-section="append_prompt" /> Appended prompts</label>
            <label><input type="checkbox" data-mode-section="project_context" /> Project context</label>
            <label><input type="checkbox" data-mode-section="skills" /> Skills</label>
            <label><input type="checkbox" data-mode-section="date" /> Current date</label>
            <label><input type="checkbox" data-mode-section="cwd" /> Current working directory</label>
          </div>
        </fieldset>
        <div class="actions">
          <button id="mode-cancel" type="button" class="secondary">Cancel</button>
          <button type="submit" class="primary">Save mode</button>
        </div>
      </form>
    </section>
  </div>

  <div id="tree-dialog" class="modal-backdrop hidden" role="dialog" aria-modal="true" aria-labelledby="tree-dialog-title">
    <section class="modal tree-modal">
      <header>
        <h2 id="tree-dialog-title">Conversation tree</h2>
        <button id="tree-close" type="button" class="secondary">Close</button>
      </header>
      <div class="tree-layout">
        <section class="tree-panel tree-left">
          <div class="tree-controls">
            <input id="tree-search" placeholder="Search entries, labels, tools…" />
            <select id="tree-filter" title="Filter tree entries">
              <option value="default">Default</option>
              <option value="no-tools">No tools</option>
              <option value="user-only">User only</option>
              <option value="labeled-only">Labeled only</option>
              <option value="all">All</option>
            </select>
          </div>
          <div id="tree-list" class="tree-list"></div>
        </section>
        <section class="tree-panel tree-right">
          <div id="tree-details" class="tree-details"><p class="muted">Select an entry.</p></div>
          <div class="tree-actions">
            <div class="tree-label-row">
              <input id="tree-label" placeholder="Label selected entry…" />
              <button id="tree-save-label" type="button">Save label</button>
              <button id="tree-clear-label" type="button" class="secondary">Clear</button>
            </div>
            <select id="tree-summary" title="Branch summary option">
              <option value="none">No branch summary</option>
              <option value="default">Summarize abandoned branch</option>
              <option value="custom">Summarize with custom instructions</option>
            </select>
            <textarea id="tree-custom-summary" placeholder="Custom summarization focus/instructions…" disabled></textarea>
            <button id="tree-continue" type="button" class="primary">Continue from here</button>
          </div>
        </section>
      </div>
    </section>
  </div>

  <div id="model-dialog" class="modal-backdrop hidden" role="dialog" aria-modal="true" aria-labelledby="model-dialog-title">
    <section class="modal picker-modal">
      <header><h2 id="model-dialog-title">Select model</h2><button id="model-close" type="button" class="secondary">Close</button></header>
      <input id="model-search" class="picker-search" placeholder="Search models/providers…" />
      <div class="picker-layout">
        <div id="model-list" class="picker-list"></div>
        <div id="model-details" class="picker-details"></div>
      </div>
    </section>
  </div>

  <div id="thinking-dialog" class="modal-backdrop hidden" role="dialog" aria-modal="true" aria-labelledby="thinking-dialog-title">
    <section class="modal thinking-modal">
      <header><h2 id="thinking-dialog-title">Thinking level</h2><button id="thinking-close" type="button" class="secondary">Close</button></header>
      <div id="thinking-list" class="picker-list" style="margin:12px"></div>
    </section>
  </div>

  <div id="auth-dialog" class="modal-backdrop hidden" role="dialog" aria-modal="true" aria-labelledby="auth-dialog-title">
    <section class="modal picker-modal">
      <header><h2 id="auth-dialog-title">Provider authentication</h2><button id="auth-close" type="button" class="secondary">Close</button></header>
      <input id="auth-search" class="picker-search" placeholder="Search providers…" />
      <div class="picker-layout">
        <div id="auth-list" class="picker-list"></div>
        <div id="auth-details" class="picker-details"></div>
      </div>
      <div class="auth-actions">
        <input id="auth-api-key" type="password" placeholder="API key for selected API-key provider…" />
        <div id="auth-prompt-box" class="hidden">
          <p id="auth-prompt-text" class="muted"></p>
          <input id="auth-prompt-input" placeholder="Login response…" />
        </div>
        <div class="auth-action-row">
          <button id="auth-prompt-submit" type="button" class="secondary">Submit prompt</button>
          <button id="auth-save-key" type="button">Save API key</button>
          <button id="auth-login-oauth" type="button">Login subscription</button>
          <button id="auth-logout" type="button" class="secondary">Logout</button>
        </div>
      </div>
    </section>
  </div>

  <script nonce="${nonce}" src="${mrmdUri}"></script>
  <script nonce="${nonce}" src="${scriptUri}"></script>
</body>
</html>`;
  }
}

function promptModesDir(): string {
  return path.join(getAgentDirFast(), "modes");
}

function promptModeFile(key: string): string {
  return path.join(promptModesDir(), `${key}.json`);
}

function normalizePromptModeDefinition(value: unknown): PromptModeDefinition | null {
  if (!value || typeof value !== "object") return null;
  const raw = value as Record<string, unknown>;
  const key = String(raw.key ?? "").trim().toLowerCase();
  const label = String(raw.label ?? "").trim();
  const opener = String(raw.opener ?? "").trim();
  const appendix = typeof raw.appendix === "string" ? raw.appendix : "";
  const systemPrompt = typeof raw.systemPrompt === "string" ? raw.systemPrompt : "";
  const removeSections = Array.isArray(raw.removeSections)
    ? raw.removeSections.filter((section): section is string => typeof section === "string" && SYSTEM_PROMPT_SECTIONS.has(section))
    : [];
  if (!/^[a-z][a-z0-9_-]*$/.test(key) || !label || (!opener && !systemPrompt)) return null;
  return {
    key,
    label,
    opener,
    ...(appendix.trim() ? { appendix } : {}),
    ...(systemPrompt.trim() ? { systemPrompt } : {}),
    ...(removeSections.length ? { removeSections } : {}),
  };
}

async function savePromptModeDefinition(definition: PromptModeDefinition): Promise<void> {
  await fs.promises.mkdir(promptModesDir(), { recursive: true });
  await fs.promises.writeFile(promptModeFile(definition.key), `${JSON.stringify(definition, null, 2)}\n`, "utf8");
}

async function loadPromptModeDefinition(sessionUri: vscode.Uri, mode: string): Promise<PromptModeDefinition> {
  const key = mode.trim().toLowerCase();
  if (key && /^[a-z][a-z0-9_-]*$/.test(key)) {
    const custom = await fs.promises.readFile(promptModeFile(key), "utf8")
      .then((text) => normalizePromptModeDefinition(JSON.parse(text)))
      .catch(() => null);
    if (custom) return custom;
  }
  const cwd = await sessionCwd(sessionUri);
  const option = (await loadPromptModeOptions(cwd)).find((candidate) => candidate.value === key);
  return {
    key,
    label: option?.label ?? key,
    opener: option?.opener ?? "",
    appendix: option?.appendix ?? "",
    systemPrompt: option?.systemPrompt ?? "",
    removeSections: option?.removeSections ?? [],
  };
}

async function loadPromptModeOptions(cwd: string): Promise<PromptModeOption[]> {
  const { discoverAndLoadExtensions, getAgentDir } = await loadPiSdk();
  const agentDir = getAgentDir();
  const modeExtensionPath = path.resolve(agentDir, "extensions", "modes.ts");
  const loaded = await discoverAndLoadExtensions([], cwd, agentDir).catch(() => null);
  const modeExtensions = loaded?.extensions.filter((extension: any) => path.resolve(extension.resolvedPath) === modeExtensionPath) ?? [];
  const command = (modeExtensions.length ? modeExtensions : loaded?.extensions ?? [])
    .flatMap((extension: any) => Array.from(extension.commands?.values?.() ?? []))
    .find((candidate: any) => candidate?.name === MODE_COMMAND || candidate?.invocationName === MODE_COMMAND) as any;
  let completions: unknown = [];
  if (typeof command?.getArgumentCompletions === "function") {
    completions = await Promise.resolve(command.getArgumentCompletions("")).catch(() => []);
  }
  return Array.isArray(completions)
    ? completions
      .map((item: any) => ({
        value: String(item?.value ?? item?.label ?? ""),
        label: String(item?.label ?? item?.value ?? ""),
        opener: typeof item?.opener === "string" ? item.opener : undefined,
        appendix: typeof item?.appendix === "string" ? item.appendix : undefined,
        systemPrompt: typeof item?.systemPrompt === "string" ? item.systemPrompt : undefined,
        removeSections: Array.isArray(item?.removeSections) ? item.removeSections.filter((section: unknown) => typeof section === "string") : undefined,
      }))
      .filter((item) => item.value.length > 0)
    : [];
}

async function parsePromptModeForSession(sessionUri: vscode.Uri, value: unknown): Promise<string | null> {
  if (typeof value !== "string" || !value.trim()) return null;
  const parsed = await readPromptModeOptionsForSession(sessionUri);
  return parsed.some((option) => option.value === value) ? value : null;
}

async function readPromptModeOptionsForSession(sessionUri: vscode.Uri): Promise<PromptModeOption[]> {
  const cwd = await sessionCwd(sessionUri);
  return loadPromptModeOptions(cwd);
}

async function sessionCwd(sessionUri: vscode.Uri): Promise<string> {
  const { SessionManager } = await loadPiSdk();
  const manager = SessionManager.open(sessionUri.fsPath);
  return manager.getCwd() || path.dirname(sessionUri.fsPath);
}

function modeFromRawEntries(entries: Array<Record<string, any> | SessionEntry>): string {
  let mode = DEFAULT_PROMPT_MODE;
  for (const entry of entries) {
    if (entry?.type !== "custom" || (entry as any).customType !== MODE_ENTRY_TYPE) continue;
    const value = (entry as any).data?.mode;
    if (typeof value === "string" && value.trim()) mode = value;
  }
  return mode;
}

function sessionNameFromRawEntries(entries: Array<Record<string, any> | SessionEntry>): string {
  let name = "";
  for (const entry of entries) {
    if (entry?.type !== "session_info") continue;
    const value = (entry as any).name;
    if (typeof value === "string") name = value;
  }
  return name;
}

async function reconstructSystemPrompt(sessionUri: vscode.Uri, prompt: string): Promise<string> {
  const { createAgentSession, getAgentDir, SessionManager } = await loadPiSdk();
  const tmpDir = await fs.promises.mkdtemp(path.join(os.tmpdir(), "pirat-system-prompt-"));
  const tmpFile = path.join(tmpDir, path.basename(sessionUri.fsPath) || "session.jsonl");
  try {
    await fs.promises.copyFile(sessionUri.fsPath, tmpFile).catch(async (err: NodeJS.ErrnoException) => {
      if (err.code === "ENOENT") await fs.promises.writeFile(tmpFile, "", "utf8");
      else throw err;
    });
    const manager = SessionManager.open(tmpFile);
    const cwd = manager.getCwd() || path.dirname(sessionUri.fsPath);
    const created = await createAgentSession({ cwd, agentDir: getAgentDir(), sessionManager: manager });
    const session = created.session;
    try {
      await bindPiWebviewExtensions(session, { webview: { postMessage: async () => true } as any });
      const basePrompt = String((session as any)._baseSystemPrompt ?? session.systemPrompt ?? "");
      const options = (session as any)._baseSystemPromptOptions;
      const runner = (session as any)._extensionRunner;
      const result = runner?.emitBeforeAgentStart
        ? await runner.emitBeforeAgentStart(prompt, undefined, basePrompt, options)
        : undefined;
      return String(result?.systemPrompt ?? basePrompt);
    } finally {
      session.dispose();
    }
  } finally {
    await fs.promises.rm(tmpDir, { recursive: true, force: true }).catch(() => undefined);
  }
}

async function bindPiWebviewExtensions(session: AgentSession, panel: WebviewHost): Promise<void> {
  await session.bindExtensions({
    uiContext: createPiWebviewExtensionUi(panel),
    onError: (error: any) => {
      void panel.webview.postMessage({ type: "showError", message: error?.error ?? String(error) });
    },
  } as any);
}

function createPiWebviewExtensionUi(panel: WebviewHost): any {
  const postStatus = (text: string | undefined) => {
    void panel.webview.postMessage({ type: "piStatus", text: text || "" });
  };
  return {
    select: async (_title: string, options: string[]) => options[0],
    confirm: async () => false,
    input: async () => undefined,
    notify: (message: string, type?: "info" | "warning" | "error") => {
      void panel.webview.postMessage(type === "error" ? { type: "showError", message } : { type: "piStatus", text: message });
    },
    onTerminalInput: () => () => undefined,
    setStatus: (_key: string, text: string | undefined) => postStatus(text),
    setWorkingMessage: postStatus,
    setWorkingVisible: () => undefined,
    setWorkingIndicator: () => undefined,
    setHiddenThinkingLabel: () => undefined,
    setWidget: () => undefined,
    setFooter: () => undefined,
    setHeader: () => undefined,
    setTitle: () => undefined,
    custom: async () => undefined,
    pasteToEditor: () => undefined,
    setEditorText: () => undefined,
    getEditorText: () => "",
    editor: async () => undefined,
    addAutocompleteProvider: () => undefined,
    setEditorComponent: () => undefined,
    getEditorComponent: () => undefined,
    theme: undefined,
    getAllThemes: () => [],
    getTheme: () => undefined,
    setTheme: () => ({ success: false, error: "UI not available" }),
    getToolsExpanded: () => false,
    setToolsExpanded: () => undefined,
  };
}

function modelDto(model: any, registry?: any): any {
  return {
    provider: model.provider,
    providerName: registry?.getProviderDisplayName?.(model.provider) ?? model.provider,
    id: model.id,
    name: model.name || model.id,
    reasoning: !!model.reasoning,
    input: model.input || ["text"],
    contextWindow: model.contextWindow,
    maxTokens: model.maxTokens,
  };
}

function authProvidersDto(session: AgentSession): any {
  const authStorage = session.modelRegistry.authStorage;
  const oauthProviders = authStorage.getOAuthProviders?.() ?? [];
  const providers = new Map<string, any>();
  for (const provider of oauthProviders) {
    const credential = authStorage.get(provider.id);
    providers.set(`${provider.id}:oauth`, {
      key: `${provider.id}:oauth`,
      id: provider.id,
      name: provider.name || session.modelRegistry.getProviderDisplayName(provider.id),
      authType: "oauth",
      configured: credential?.type === "oauth",
      storedType: credential?.type,
      status: authStorage.getAuthStatus(provider.id),
    });
  }
  for (const model of session.modelRegistry.getAll()) {
    if (providers.has(`${model.provider}:api_key`)) continue;
    const credential = authStorage.get(model.provider);
    providers.set(`${model.provider}:api_key`, {
      key: `${model.provider}:api_key`,
      id: model.provider,
      name: session.modelRegistry.getProviderDisplayName(model.provider),
      authType: "api_key",
      configured: !!credential || !!session.modelRegistry.getProviderAuthStatus(model.provider)?.configured,
      storedType: credential?.type,
      status: session.modelRegistry.getProviderAuthStatus(model.provider),
    });
  }
  return { providers: Array.from(providers.values()).sort((a, b) => a.name.localeCompare(b.name)) };
}

function buildTreeDto(manager: any): SessionTreeDto {
  const leafId = manager.getLeafId?.() ?? null;
  const branch = manager.getBranch?.() ?? [];
  const activePathIds = branch.map((entry: any) => entry.id).filter((id: unknown): id is string => typeof id === "string");
  const toNode = (node: any): TreeNodeDto => {
    const entry = node.entry;
    const role = treeEntryRole(entry);
    const markdown = treeEntryMarkdown(entry);
    return {
      id: entry.id,
      parentId: entry.parentId ?? null,
      type: entry.type,
      role,
      timestamp: entry.timestamp,
      ...(node.label ? { label: node.label } : {}),
      ...(node.labelTimestamp ? { labelTimestamp: node.labelTimestamp } : {}),
      preview: treeEntryPreview(entry, markdown),
      markdown,
      children: (node.children || []).map(toNode),
    };
  };
  return { roots: (manager.getTree?.() ?? []).map(toNode), leafId, activePathIds };
}

function treeEntryRole(entry: any): string {
  if (entry?.type === "message") {
    const role = entry.message?.role;
    if (role === "toolResult") return `tool:${entry.message?.toolName ?? "result"}`;
    return role || "message";
  }
  if (entry?.type === "custom_message") return `custom:${entry.customType ?? "message"}`;
  return entry?.type || "entry";
}

function treeEntryMarkdown(entry: any): string {
  if (entry?.type === "message") return messageContentToMarkdown(entry.message);
  if (entry?.type === "custom_message") return customContentToMarkdown(entry.content);
  if (entry?.type === "branch_summary") return entry.summary || "";
  if (entry?.type === "compaction") return entry.summary || "";
  if (entry?.type === "model_change") return `Model: ${entry.provider ?? ""}/${entry.modelId ?? ""}`;
  if (entry?.type === "thinking_level_change") return `Thinking level: ${entry.thinkingLevel ?? ""}`;
  if (entry?.type === "session_info") return `Session name: ${entry.name ?? ""}`;
  if (entry?.type === "label") return `Label ${entry.label ? `\"${entry.label}\"` : "cleared"} on ${entry.targetId ?? ""}`;
  if (entry?.type === "custom") return codeFence(JSON.stringify(entry.data ?? {}, null, 2), "json");
  return codeFence(JSON.stringify(entry ?? {}, null, 2), "json");
}

function treeEntryPreview(entry: any, markdown: string): string {
  const normalize = (s: string) => s.replace(/[\n\t]+/g, " ").replace(/\s+/g, " ").trim();
  if (entry?.type === "message") {
    const role = entry.message?.role || "message";
    if (role === "toolResult") return `[${entry.message?.toolName ?? "tool"}] ${normalize(markdown).slice(0, 180)}`;
    return `${role}: ${normalize(markdown).slice(0, 180)}`;
  }
  if (entry?.type === "custom_message") return `[${entry.customType ?? "custom"}]: ${normalize(markdown).slice(0, 180)}`;
  if (entry?.type === "compaction") return `[compaction] ${normalize(markdown).slice(0, 180)}`;
  if (entry?.type === "branch_summary") return `[branch summary] ${normalize(markdown).slice(0, 180)}`;
  if (entry?.type === "model_change") return `[model: ${entry.modelId ?? ""}]`;
  if (entry?.type === "thinking_level_change") return `[thinking: ${entry.thinkingLevel ?? ""}]`;
  if (entry?.type === "session_info") return `[title: ${entry.name ?? "empty"}]`;
  if (entry?.type === "label") return `[label: ${entry.label ?? "cleared"}]`;
  if (entry?.type === "custom") return `[custom: ${entry.customType ?? ""}]`;
  return `[${entry?.type ?? "entry"}]`;
}

function messageContentToMarkdown(message: any): string {
  if (message?.role === "toolResult") return toolResultMessageToMarkdown(message);
  return customContentToMarkdown(message?.content);
}

function customContentToMarkdown(content: any): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  const out: string[] = [];
  for (const block of content) {
    if (block?.type === "text" && typeof block.text === "string") out.push(block.text);
    else if (block?.type === "thinking" && typeof block.thinking === "string") out.push(`\n\n> Thinking\n\n${block.thinking}`);
    else if (block?.type === "toolCall") out.push(`\n\n${toolCallMarkdown(block)}`);
    else if (block?.type === "image") out.push(`\n\n![image](data:${block.mimeType};base64,${block.data})`);
  }
  return out.join("\n\n").trim();
}

function markdownToMessageContent(previous: any, text: string): any {
  if (typeof previous === "string") return text;
  return [{ type: "text", text }];
}

function toolResultMessageToMarkdown(message: any): string {
  return toolEventMarkdown(message?.toolName ?? "tool", {
    content: message?.content,
    details: message?.details,
  }, !!message?.isError);
}

function toolEventMarkdown(toolName: string, result: any, isError: boolean): string {
  const title = `${isError ? "**Error:** " : ""}**${toolName}**`;
  const content = toolResultContentMarkdown(result?.content).trim();
  const details = result?.details === undefined ? "" : `\n\n<details><summary>details</summary>\n\n${codeFence(JSON.stringify(result.details, null, 2), "json")}\n</details>`;
  if (content) return `${title}\n\n${content}${details}`;
  if (result) return `${title}\n\n${limitedCodeFence(JSON.stringify(result, null, 2), "json", "tail", "Show full result")}`;
  return `${title}\n\nWorking…`;
}

function toolResultContentMarkdown(content: any): string {
  if (typeof content === "string") return terminalOutputFence(content);
  if (!Array.isArray(content)) return "";
  const out: string[] = [];
  for (const block of content) {
    if (block?.type === "text" && typeof block.text === "string") out.push(terminalOutputFence(block.text));
    else if (block?.type === "image") out.push(`![image](data:${block.mimeType};base64,${block.data})`);
    else if (block !== undefined) out.push(codeFence(JSON.stringify(block, null, 2), "json"));
  }
  return out.filter(Boolean).join("\n\n");
}

function toolCallMarkdown(block: any): string {
  return limitedCodeFence(JSON.stringify(block, null, 2), "json", "head", "Show full tool call");
}

function terminalOutputFence(text: string): string {
  const cleaned = cleanTerminalText(text).replace(/[\t ]+$/gm, "").replace(/\s+$/, "");
  return cleaned ? limitedCodeFence(cleaned, "output", "tail", "Show full result") : "";
}

const TOOL_PREVIEW_LINES = 10;
const TOOL_FULL_DETAILS_CHAR_LIMIT = 80_000;

function limitedCodeFence(text: string, language: string, mode: "head" | "tail", summary: string): string {
  const value = String(text ?? "").replace(/\s+$/, "");
  const lines = value.split(/\r?\n/);
  if (lines.length <= TOOL_PREVIEW_LINES && value.length <= TOOL_FULL_DETAILS_CHAR_LIMIT) return codeFence(value, language);
  const preview = mode === "head" ? lines.slice(0, TOOL_PREVIEW_LINES) : lines.slice(-TOOL_PREVIEW_LINES);
  if (value.length > TOOL_FULL_DETAILS_CHAR_LIMIT) {
    return `${codeFence(preview.join("\n"), language)}\n\n_Full output omitted from the session view for responsiveness (${value.length.toLocaleString()} characters)._`;
  }
  return `${codeFence(preview.join("\n"), language)}\n\n<details class="rat-collapsible-result"><summary>${summary}</summary>\n\n${codeFence(value, language)}\n</details>`;
}

function codeFence(text: string, language = ""): string {
  const value = String(text ?? "");
  const ticks = value.match(/`+/g)?.reduce((max, run) => Math.max(max, run.length), 2) ?? 2;
  const fence = "`".repeat(Math.max(3, ticks + 1));
  return `${fence}${language}\n${value}\n${fence}`;
}

// eslint-disable-next-line no-control-regex
const TERMINAL_ANSI_RE = /\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[()][A-Z0-9]|[0-9A-Za-z=<>])/g;

function cleanTerminalText(text: string): string {
  let value = String(text || "").replace(TERMINAL_ANSI_RE, "");
  if (!value.includes("\r")) return value;
  return value
    .split("\n")
    .map((line) => {
      if (!line.includes("\r")) return line;
      const parts = line.split("\r");
      for (let i = parts.length - 1; i >= 0; i--) {
        if (parts[i]) return parts[i];
      }
      return "";
    })
    .join("\n");
}

function markdownToCustomContent(previous: any, text: string): any {
  if (typeof previous === "string") return text;
  return [{ type: "text", text }];
}

function localPathFromAssetUrl(url: string): string {
  if (!url) return "";
  if (/^file:/i.test(url)) return fileURLToPath(url);
  return url;
}

function uniquePath(candidate: string): string {
  if (!fs.existsSync(candidate)) return candidate;
  const dir = path.dirname(candidate);
  const ext = path.extname(candidate);
  const base = path.basename(candidate, ext);
  for (let i = 1; i < 10_000; i++) {
    const next = path.join(dir, `${base}-${i}${ext}`);
    if (!fs.existsSync(next)) return next;
  }
  return path.join(dir, `${base}-${Date.now()}${ext}`);
}

function getNonce(): string {
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  let nonce = "";
  for (let i = 0; i < 32; i++) nonce += chars.charAt(Math.floor(Math.random() * chars.length));
  return nonce;
}

function assistantMarkdownFromEvent(event: any): string | null {
  if (event?.type === "message_update" || event?.type === "message_end") {
    const message = event.message;
    if (message?.role === "assistant") return messageContentToMarkdown(message);
  }
  if (event?.type === "text" && typeof event.text === "string") return event.text;
  if (event?.type === "content_block_delta" && event?.delta?.type === "text") return event.delta.text;
  return null;
}

async function listPiSessions(limit: number, scope: "workspace" | "all"): Promise<SessionListItem[]> {
  const workspaceCwd = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? process.cwd();
  const roots = scope === "all" ? await sessionProjectDirs() : [getDefaultSessionDirFast(workspaceCwd)];
  const files = (await Promise.all(roots.map((root) => findJsonlFiles(root, limit))))
    .flat()
    .slice(0, limit * 2);
  const sessions = await Promise.all(files.map(sessionListItemFromFile));
  return sessions
    .filter((item): item is SessionListItem => !!item)
    .sort((a, b) => b.mtime - a.mtime)
    .slice(0, limit);
}

async function sessionProjectDirs(): Promise<string[]> {
  const root = getSessionsRootFast();
  const entries = await fs.promises.readdir(root, { withFileTypes: true }).catch(() => []);
  return entries.filter((entry) => entry.isDirectory()).map((entry) => path.join(root, entry.name));
}

async function findJsonlFiles(dir: string, limit: number): Promise<string[]> {
  const entries = await fs.promises.readdir(dir, { withFileTypes: true }).catch(() => []);
  const candidates = entries
    .filter((entry) => entry.isFile() && entry.name.endsWith(".jsonl"))
    .map((entry) => path.join(dir, entry.name));
  const files = await Promise.all(candidates.map(async (file) => {
    const stat = await fs.promises.stat(file).catch(() => null);
    return { file, mtime: stat?.mtimeMs ?? 0 };
  }));
  return files.sort((a, b) => b.mtime - a.mtime).slice(0, limit).map((item) => item.file);
}

async function sessionListItemFromFile(file: string): Promise<SessionListItem | null> {
  const stat = await fs.promises.stat(file).catch(() => null);
  if (!stat) return null;
  const info = await sessionInfoFast(file).catch(() => ({ label: path.basename(file), cwd: "" }));
  return {
    label: info.label,
    cwd: shortPath(info.cwd),
    path: file,
    time: stat.mtime.toLocaleString(),
    mtime: stat.mtimeMs,
  };
}

async function sessionInfoFast(file: string): Promise<{ label: string; cwd: string }> {
  const fd = await fs.promises.open(file, "r");
  try {
    const buffer = Buffer.alloc(64 * 1024);
    const { bytesRead } = await fd.read(buffer, 0, buffer.length, 0);
    const text = buffer.subarray(0, bytesRead).toString("utf8");
    let cwd = "";
    let label = "";
    for (const line of text.split(/\r?\n/)) {
      if (!line.trim()) continue;
      try {
        const entry = JSON.parse(line);
        if (entry.type === "session" && typeof entry.cwd === "string") cwd = entry.cwd;
        if (entry.type === "session_info" && entry.name) label = String(entry.name);
        if (!label && entry.type === "message" && entry.message?.role === "user") {
          const first = customContentToMarkdown(entry.message.content).replace(/\s+/g, " ").trim();
          if (first) label = first.length > 120 ? `${first.slice(0, 117)}…` : first;
        }
        if (cwd && label) break;
      } catch {
        // Ignore partial/corrupt lines in the preview window.
      }
    }
    return { label: label || path.basename(file), cwd };
  } finally {
    await fd.close();
  }
}

function getAgentDirFast(): string {
  return process.env.PI_CODING_AGENT_DIR ?? path.join(os.homedir(), ".pi", "agent");
}

function getSessionsRootFast(): string {
  return process.env.PI_CODING_AGENT_SESSION_DIR ?? path.join(getAgentDirFast(), "sessions");
}

function getDefaultSessionDirFast(cwd: string): string {
  const safePath = `--${cwd.replace(/^[\\/]/, "").replace(/[\\/:]/g, "-")}--`;
  return process.env.PI_CODING_AGENT_SESSION_DIR ?? path.join(getAgentDirFast(), "sessions", safePath);
}

function shortPath(value: string): string {
  const home = os.homedir();
  return home && value.startsWith(home) ? "~" + value.slice(home.length) : value;
}

function escapeHtml(text: string): string {
  return text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/\"/g, "&quot;");
}
