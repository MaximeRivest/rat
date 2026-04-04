# Roadmap

Status: **v0.1** — core works (py, sh, r, pi). Extension system proven.

Each milestone is 1–2 weeks. Ship each one before starting the next.

Guiding principles:
- **Don't build what the CLI already does well.** Git, docker, kubectl
  are great CLIs. Let the community build RAPLs for them if there's
  real demand.
- **Pi is the agent pattern POC.** Other agents (Claude Code, Codex,
  custom) come from the ecosystem, not from us hardcoding them.
- **Communication is the killer feature.** Email, Slack, SMS, Discord
  — making the agent social is what no one else does.
- **The network is the platform.** runanything.dev turns rat from a
  local tool into infrastructure.

---

## M0: Harden what exists

Ship-quality polish. No new features — just make existing ones solid.

- [ ] Tests for generic JSON kernel I/O, event dispatch, background
      reader. Test with mock kernel scripts.
- [ ] Tests for generic tmux kernel: session lifecycle, send-keys,
      control file protocol.
- [ ] Pi bridge: test long conversations, tool calls, errors, cancel,
      session continuity across restart.
- [ ] R frontend: test multiline edge cases, completion quality.
- [ ] Slack kernel: test with a real workspace. Fix issues.
- [ ] Rewrite custom-runtimes.md: current runtime.yaml schema, both
      kernel types, frontend types, template variables, bridge
      contract, event protocol.
- [ ] README: what rat is, install, quick start (py/sh/r/pi).
- [ ] Audit every error message. User always knows what to do next.
- [ ] `rat doctor`: basic diagnostics.

**Done when**: new user can install, run `rat py`/`sh`/`r`/`pi`,
everything works. Docs are accurate. `rat doctor` catches issues.

---

## M1: Complete the coding REPLs

rat must be a credible multi-language REPL before anything else.
If `rat jl` and `rat js` don't work, nobody trusts `rat sql`.

Each is a JSON kernel (~200 lines) + runtime.yaml + frontend.
Same pattern as R.

### JavaScript / Node.js

- [ ] kernel-js.js: Node's `vm` module for persistent namespace.
      `run` = evaluate. `look` = list variables. `look(at="x")` =
      inspect. Completions via Node's REPL completer.
- [ ] Frontend: prompt_toolkit or Node readline-based, connected
      to MCP. Syntax highlighting.
- [ ] Test: npm packages, async/await, require().

### TypeScript / Deno

- [ ] kernel-ts.ts: Deno eval with persistent namespace. Type
      checking. `run`, `look`, completions.
- [ ] Alternative: ts-node or tsx for Node-based TypeScript.
- [ ] Test: type annotations, imports, async.

### Julia

- [ ] kernel-jl.jl: Julia eval in persistent Module. `run` = eval.
      `look` = list variables. Completions via Julia's REPL backend.
- [ ] Frontend: prompt_toolkit-based with Julia syntax highlighting,
      or Julia's native REPL hooked to MCP.
- [ ] Test: packages, Plots, DataFrames.

### Ruby

- [ ] kernel-rb.rb: `Binding` for persistent namespace. `run` = eval.
      `look` = list local/instance variables. Completions via
      `IRB::InputCompletor` or bond gem.
- [ ] Frontend: pry or irb hooked to MCP, or prompt_toolkit with
      Ruby syntax highlighting.
- [ ] Test: gems, classes, blocks.

**Done when**: `rat py`, `rat sh`, `rat r`, `rat jl`, `rat js`,
`rat ts`, `rat rb` all work. Every major language a developer
reaches for has a shared RAPL. The extension system is proven
across 7+ languages.

---

## M2: SQL + Communication

The runtimes that make rat immediately, daily useful. SQL for data
work. Communication for making the agent social.

### SQL

- [ ] kernel-sql.py: Postgres + SQLite. `run` = query. `look` =
      tables/schema. `look(at="users")` = describe. Completions
      for tables, columns.
- [ ] Setup: `rat add pg-work --lang sql --env DATABASE_URL=...`
- [ ] Test with real databases.

### Email

- [ ] kernel-email.py: IMAP read, SMTP send. `run('/reply ...')`,
      `run('/compose ...')`. `look` = inbox. `look(at="unread")`.
      Events for new mail (IMAP IDLE or polling).
- [ ] Test with real email.

### Slack hardening

- [ ] Real-world test pass.
- [ ] WebSocket mode (Socket Mode) for real-time events — new
      messages emit `event` ops, appear in REPL live.
- [ ] Thread support.

### SMS

- [ ] kernel-sms.py: Twilio API. `run('text')` = send.
      `look` = recent messages. Events for incoming.
- [ ] Test with real phone number.

### Discord

- [ ] kernel-discord.py: Bot token. Channels, messages, reactions.
      Events for new messages.
- [ ] Test with real server.

**Done when**: `rat sql`, `rat email`, `rat slack`, `rat sms`,
`rat discord` all work against real services. New messages appear
live in the REPL via events. Claude can read your inbox, query
your database, and message your team.

---

## M3: Real-time event forwarding

Currently events only reach the REPL via activity log polling.
This milestone pushes them to MCP clients in real-time.

- [ ] Forward kernel events as MCP notifications to all connected
      clients (Claude Desktop, Cursor, notebooks).
- [ ] Streaming display in `rat run`: progress events shown inline
      during long operations.
- [ ] Claude receives Slack messages, email notifications, SMS
      as they arrive — can decide to respond.

**Done when**: Claude Desktop shows a Slack message notification
and can reply to it. `rat run` shows progress bars for long ops.

---

## M4: runanything.dev

The network layer. Kernels accessible across machines.

- [ ] `rat login`: authenticate with runanything.dev.
- [ ] `rat agent`: persistent process that tunnels local kernels.
- [ ] runanything.dev proxy: auth, routing, tunnel. Minimal viable:
      single-user.
- [ ] Remote resolution: `rat status` shows local + remote.
      `rat run py@gpu 'code'` routes transparently.

**Done when**: `rat run py@gpu-server 'import torch'` from your
laptop executes on the GPU server. `rat status` shows all machines.

---

## M5: Sharing + marketplace

- [ ] `rat share`: share a kernel with another user.
- [ ] `rat install <runtime>`: install community runtimes from
      registry (git-based or npm-style).
- [ ] Runtime packaging standard for distributing runtime.yaml +
      kernel scripts.
- [ ] Team namespaces.

**Done when**: `rat install sql` gets the SQL kernel.
`rat share py@ml --with bob` lets Bob use your GPU kernel.

---

## What we don't build

These are left for the community / ecosystem:

- **Git, Docker, K8s, AWS, Make** — great CLIs already. If someone
  wants a RAPL for them, the extension system supports it (runtime.yaml
  + kernel script, no Go code). We don't force it.
- **Other AI agents** — pi is the POC. Claude Code, Codex, Gemini,
  custom agents — anyone can write a tmux bridge for their agent.
  The pattern is proven.
- **Jupyter bridge, VS Code, web UI** — comes after the core
  platform is solid. May be community-driven.
- **Shopping, banking, calendar** — fun demos but not core. Community
  builds these when the ecosystem exists.

---

## Prioritization

```
Milestone  What                     Why
─────────  ────                     ───
M0         Harden                   Nothing works if the base is shaky
M1         Coding REPLs             Credibility. Every language works.
M2         SQL + communication      Daily value. The killer feature.
M3         Real-time events → MCP   Reactive agents. The magic moment.
M4         runanything.dev           Multiplies everything by N machines
M5         Sharing + marketplace    Network effects. Ecosystem.
```

---

## How to work

1. One milestone at a time. Don't start M1 until M0 ships.
2. Each runtime is independent — can be built in a day.
3. Test with real systems, not mocks.
4. Ship continuously — each runtime = a new `rat` build.
5. Keep kernel scripts dependency-free (stdlib when possible).
6. Check boxes here as things ship. Git tag per milestone.
