# Network vision — runanything.dev

rat already speaks HTTP MCP. A tunnel just makes `127.0.0.1:8717`
reachable from anywhere. The protocol doesn't change. The transport does.

---

## The idea

You have rat running on multiple machines — laptop, GPU server, home
server, cloud instance. Each runs kernels: Python on the GPU box,
Slack on the always-on home server, Postgres on the DB server.

`runanything.dev` is a tunnel service that connects them. From your
laptop, `rat py@gpu` connects to the GPU server's Python kernel.
Claude Desktop, configured with a single URL, can reach any kernel
on any machine.

```
Your laptop                    runanything.dev                   GPU server
──────────                     ──────────────                    ──────────
rat run py@gpu 'torch...'  →   tunnel (auth)   →   rat serve py --http :8717
                            ←   tunnel          ←   result with CUDA tensors

rat slack-eng 'done' ──────→   tunnel ─────────→   rat serve slack --http :8718
                                                    (on your always-on box)
```

---

## User experience

### Login

```bash
rat login
→ Opening runanything.dev/auth...
→ Logged in as maxime. 3 machines registered.
```

Authenticates with runanything.dev (OAuth, GitHub, passkey).
Stores token in `~/.config/rat/auth.yaml`.

### Status — local AND remote

```bash
rat status
  NAME              STATUS   MACHINE        CWD
  py@myproject      running  laptop         ~/Projects/myapp
  sh@myproject      running  laptop         ~/Projects/myapp
  py@ml             running  gpu-server     ~/ml-experiments      ← remote
  slack-eng         running  home-server    ~/                    ← remote
  pg@analytics      running  db-server      ~/                    ← remote
  email@work        running  home-server    ~/                    ← remote
```

### Transparent remote access

Same syntax. rat resolves locally first, then checks the remote
directory.

```bash
# Run on GPU server — transparent
rat run py@ml 'model.train(epochs=10)'
→ Training... 100%
→ Loss: 0.0023, Accuracy: 99.1%
→ ✓ 45.2s (via gpu-server)

# Drop into remote REPL
rat py@ml
→ Connected to py@ml on gpu-server via runanything.dev
→ Shared namespace — 14 vars from training run.

# Claude can use remote kernels too — same MCP
# In Claude Desktop config:
#   "rat-gpu": { "url": "https://gpu-server.maxime.runanything.dev/mcp" }
```

### Agent on each machine

```bash
# On your GPU server
rat agent --name gpu-server
→ Registered gpu-server with runanything.dev
→ Tunneling 2 kernels: py@ml, jl@sim
→ Listening for remote connections...
```

`rat agent` is rat + a persistent tunnel. Each kernel's MCP endpoint
becomes reachable via `https://<machine>.<user>.runanything.dev/mcp/<kernel>`.

---

## Architecture

```
┌─────────────┐     ┌──────────────────┐     ┌─────────────┐
│   Laptop    │     │  runanything.dev  │     │  GPU Server  │
│             │     │                  │     │             │
│  rat cli ───┼────►│  auth + routing  │◄────┼── rat agent │
│             │     │                  │     │             │
│  kernels:   │     │  maxime/         │     │  kernels:   │
│   py@proj   │     │   laptop/        │     │   py@ml     │
│   sh@proj   │     │   gpu-server/    │     │   jl@sim    │
│             │     │   home-server/   │     │             │
└─────────────┘     │   db-server/     │     └─────────────┘
                    │                  │
                    └──────────────────┘     ┌─────────────┐
                           ▲                │ Home Server  │
                           │                │             │
                           └────────────────┼── rat agent │
                                            │             │
                                            │  kernels:   │
                                            │   slack-eng │
                                            │   email@work│
                                            │   pg@data   │
                                            └─────────────┘
```

---

## Why zero protocol changes

rat kernels already serve HTTP MCP. The tunnel is a reverse proxy
with auth. From the client's perspective, connecting to
`gpu-server.maxime.runanything.dev/mcp` is identical to connecting
to `127.0.0.1:8717/mcp`. Same JSON-RPC, same tools, same protocol.

This means:

- **Every kernel works remotely** — py, r, sh, pi, slack, sql, email — all of them
- **Every MCP client works** — Claude Desktop, Cursor, notebooks, other agents
- **Every frontend works** — REPL over the tunnel, tmux via SSH

---

## Components to build

```
Component              Complexity   What it does
─────────              ──────────   ─────────────────────────────────
rat login              Small        Auth + store token
rat agent              Medium       Persistent tunnel + kernel registry
runanything.dev proxy  Medium       Auth, routing, WebSocket/HTTP tunnel
Remote resolution      Small        rat status/run checks remote directory
                                    before local resolution
```

The protocol layer is already done. The kernel protocol, MCP server,
frontends — all work unchanged. It's pure transport infrastructure.

---

## Killer scenarios

### Agent on GPU, human on laptop

```bash
# Claude trains on GPU server
rat run py@ml 'model = train(data, epochs=50)'    # GPU
rat run py@ml 'plot_loss(model)'                   # remote plot
rat look py@ml --at model                          # inspect from laptop
```

### Always-on communication hub

Home server runs slack, email, whatsapp 24/7.

```bash
# Claude, from any machine:
rat run slack-eng 'standup summary: all PRs merged, deploy at 3pm'
rat run email@work '/reply sounds good, scheduling the call'
rat look slack-eng --at @alice
```

### Database accessible from anywhere

```bash
# DB server runs postgres kernel
rat run pg@analytics 'SELECT count(*) FROM events WHERE date > now() - interval 7 day'
# Claude, from Claude Desktop, queries your production DB
# You see the query in rat look pg@analytics
```

### Distributed agent orchestration

```bash
# Agent on laptop orchestrates GPU + DB
rat run pi@laptop 'Train the model on GPU, then query results DB for comparison'
# pi calls:
#   rat run py@gpu-server 'train(...)'
#   rat run pg@db-server 'SELECT ...'
# All visible to the human across all machines
```

### Team sharing

```bash
# Alice shares her GPU kernel with Bob
rat share py@ml --with bob@runanything.dev --readonly

# Bob can now:
rat run py@ml-alice 'model.evaluate(test_data)'
```

---

## The security model

- **Authentication** — runanything.dev verifies identity (OAuth/passkey)
- **Authorization** — each kernel has an ACL (owner, shared-with, public)
- **Encryption** — tunnel is TLS end-to-end
- **Audit** — all remote calls logged (who, what, when)
- **Gates** — kernels can require human approval for destructive ops
  (e.g., `/checkout` in shop kernel, `DROP TABLE` in SQL kernel)

### Default: private

Every kernel is private by default. Only the owner can access it.
Sharing is explicit and revocable.

### Read-only sharing

```bash
rat share py@ml --with bob --readonly
# Bob can rat look and rat run (non-mutating)
# But kernel can enforce: no file writes, no pip install, etc.
```

### Team namespaces

```bash
# Organization: acme.runanything.dev
rat login --org acme
rat status
  NAME              MACHINE        OWNER
  py@ml             gpu-cluster    alice
  pg@analytics      db-server      bob
  slack-eng         shared-infra   team
```

---

## The endgame

```
Local rat          = personal tool (today)
rat + tunnel       = personal cloud (next)
rat + tunnel + sharing = team platform (future)
rat + tunnel + sharing + marketplace = ecosystem

  "There's a rat kernel for that"
  — install a kernel, get a REPL + CLI + MCP server
  — for any API, service, tool, or platform
  — accessible from any machine, any client, any agent
```

The foundation — `run`/`look`/`ctl` for everything, json and tmux
kernels, extensible runtime.yaml — is the platform.
The tunnel makes it network-wide.
Sharing makes it social.
The marketplace makes it an ecosystem.

```
rat = Run AnyThing, Anywhere
```
