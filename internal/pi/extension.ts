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

  function getCurrentId(): string | null {
    if (!existsSync(currentIdFile)) return null;
    const id = readFileSync(currentIdFile, "utf8").trim();
    return id || null;
  }

  // Signal that pi is up and ready for input.
  pi.on("session_start", async () => {
    writeFileSync(readyFile, `${Date.now()}\n`);
  });

  pi.on("message_start", async () => {
    const id = getCurrentId();
    if (!id) return;
    try {
      writeFileSync(join(controlDir, `${id}.stream`), "", "utf8");
    } catch {}
  });

  pi.on("message_update", async (event) => {
    const id = getCurrentId();
    if (!id) return;
    const delta = event.assistantMessageEvent;
    if (!delta || delta.type !== "text_delta" || !delta.delta) return;
    try {
      appendFileSync(join(controlDir, `${id}.stream`), delta.delta, "utf8");
    } catch {}
  });

  // After each agent turn, check if rat is waiting for a result.
  pi.on("agent_end", async (event) => {
    const id = getCurrentId();
    if (!id) return;

    // Extract assistant text + usage from the turn's messages.
    const texts: string[] = [];
    let model = "";
    let inputTokens = 0;
    let outputTokens = 0;
    let cost = 0;

    for (const msg of event.messages || []) {
      if (msg.role === "assistant") {
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
    }

    const resultText = texts.join("\n");

    // Write structured result.
    const result = {
      text: resultText,
      model,
      inputTokens,
      outputTokens,
      cost,
      timestamp: Date.now(),
    };
    writeFileSync(
      join(controlDir, `${id}.result`),
      JSON.stringify(result),
      "utf8"
    );

    // Signal completion.
    appendFileSync(controlLog, `${id}\t0\n`);

    // Consume the request.
    try {
      unlinkSync(currentIdFile);
    } catch {}
  });
}
