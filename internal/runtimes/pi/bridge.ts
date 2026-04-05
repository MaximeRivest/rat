// rat-bridge — Pi extension that bridges pi ↔ rat kernel.
//
// When pi runs inside rat's tmux session, this extension signals
// completion back to the Go kernel so `rat run pi` gets structured
// results. Without RAT_CONTROL_DIR set, this extension is a no-op.
//
// Flow:
//   1. Go kernel writes a request ID to current-id
//   2. Go kernel sends the prompt via tmux send-keys
//   3. Pi processes the prompt (human sees it live in TUI)
//   4. On agent_end, this extension writes the result to <id>.result
//      and appends <id>\t0 to control.log
//   5. Go kernel reads the result and returns it via MCP

import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import {
  writeFileSync,
  readFileSync,
  appendFileSync,
  existsSync,
  unlinkSync,
  mkdirSync,
} from "node:fs";
import { join } from "node:path";

export default function (pi: ExtensionAPI) {
  const controlDir = process.env.RAT_CONTROL_DIR;
  if (!controlDir) return; // Not running under rat

  mkdirSync(controlDir, { recursive: true });

  const controlLog = join(controlDir, "control.log");
  const currentIdFile = join(controlDir, "current-id");
  const readyFile = join(controlDir, "ready");
  const activityLog = join(controlDir, "activity.jsonl");
  const debugLog = join(controlDir, "bridge-debug.log");

  function debug(msg: string) {
    appendFileSync(debugLog, `${new Date().toISOString()} ${msg}\n`);
  }

  let lastInteractiveInput = "";
  let activityCount = 0;

  function scanActivityCount() {
    if (!existsSync(activityLog)) return 0;
    try {
      const lines = readFileSync(activityLog, "utf8").split("\n");
      let maxN = 0;
      for (const line of lines) {
        if (!line.trim()) continue;
        try {
          const entry = JSON.parse(line);
          if (typeof entry.n === "number" && entry.n > maxN) maxN = entry.n;
        } catch {}
      }
      return maxN;
    } catch {
      return 0;
    }
  }

  function extractAssistantResult(event: any) {
    const texts: string[] = [];
    let model = "";
    let inputTokens = 0;
    let outputTokens = 0;
    let cost = 0;

    for (const msg of event.messages || []) {
      if (msg.role !== "assistant") continue;
      if (Array.isArray(msg.content)) {
        for (const c of msg.content) {
          if (c.type === "text" && c.text) texts.push(c.text);
        }
      }
      if (msg.model) model = msg.model;
      if (msg.usage) {
        inputTokens += msg.usage.input || 0;
        outputTokens += msg.usage.output || 0;
        if (msg.usage.cost?.total) cost += msg.usage.cost.total;
      }
    }

    return {
      text: texts.join("\n"),
      model,
      inputTokens,
      outputTokens,
      cost,
      timestamp: Date.now(),
    };
  }

  function appendActivity(code: string, output: string) {
    const trimmedCode = code.trim();
    if (!trimmedCode) return;
    activityCount += 1;
    const entry = {
      n: activityCount,
      code: trimmedCode.slice(0, 500),
      output: output.slice(0, 500),
      ok: true,
      t: Math.floor(Date.now() / 1000),
      client: "rat-pi-repl",
    };
    appendFileSync(activityLog, `${JSON.stringify(entry)}\n`);
  }

  // Signal that pi is up and ready for input.
  pi.on("session_start", async () => {
    activityCount = scanActivityCount();
    lastInteractiveInput = "";
    writeFileSync(readyFile, `${Date.now()}\n`);
    debug(`session_start activityCount=${activityCount}`);
  });

  // Track interactive user prompts so they can appear in rat tail.
  pi.on("input", async (event) => {
    debug(`input event.source=${event.source} event.text=${JSON.stringify(event.text)?.slice(0, 100)} keys=${Object.keys(event).join(",")}`);
    const text = typeof event.text === "string" ? event.text.trim() : "";
    if (event.source === "interactive" && text && !text.startsWith("/")) {
      lastInteractiveInput = text;
      debug(`captured interactive input: ${text.slice(0, 80)}`);
    } else {
      lastInteractiveInput = "";
      debug(`skipped input: source=${event.source} text=${text.slice(0, 40)}`);
    }
    return { action: "continue" };
  });

  // After each agent turn, either complete an MCP request or log human activity.
  pi.on("agent_end", async (event) => {
    debug(`agent_end lastInteractiveInput=${JSON.stringify(lastInteractiveInput)?.slice(0, 80)} currentIdExists=${existsSync(currentIdFile)} msgCount=${event.messages?.length ?? 0}`);
    const result = extractAssistantResult(event);
    debug(`agent_end result.text=${result.text.slice(0, 80)}`);

    if (existsSync(currentIdFile)) {
      const id = readFileSync(currentIdFile, "utf8").trim();
      if (id) {
        writeFileSync(
          join(controlDir, `${id}.result`),
          JSON.stringify(result),
          "utf8"
        );

        appendFileSync(controlLog, `${id}\t0\n`);

        try {
          unlinkSync(currentIdFile);
        } catch {}
      }
      // Clear stale interactive input — this was an MCP-driven turn.
      lastInteractiveInput = "";
      debug(`agent_end: MCP path taken, cleared lastInteractiveInput`);
      return;
    }

    if (lastInteractiveInput) {
      appendActivity(lastInteractiveInput, result.text);
      lastInteractiveInput = "";
    }
  });
}
