# RAPL — Read, Act, Perceive, Loop

A rat kernel is not a REPL. It's a **RAPL**.

A REPL (Read-Eval-Print-Loop) assumes a closed world: read text,
evaluate it in local memory, print the return value, wait for the
next input. The boundaries are the process namespace.

A RAPL (Read-Act-Perceive-Loop) assumes an open world: read intent,
act on external systems, perceive the result — and the loop advances
from humans, agents, or external events.

```
         REPL                          RAPL
         ────                          ────
Read     parse code                    parse intent
Eval     execute in namespace          Act on the world
Print    show return value             Perceive result
Loop     wait for next input           Loop — from human, agent, or event
```

---

## The four phases

### Read

The kernel receives input. This can be:

- Code from a human typing in the REPL (`rat r`)
- A prompt from an MCP client (`rat run pi 'refactor auth'`)
- A message from Claude via the MCP tool (`run(code="SELECT ...")`)
- A command from a notebook cell

The input is text, but the intent varies: execute code, send a
message, query a database, ask a question, trigger an action.

### Act

Instead of evaluating deterministic expressions in local memory,
the kernel **acts on the external world**:

| Runtime | Act means |
|---|---|
| Python | Execute code in a persistent namespace |
| Slack | Send a message to a channel |
| SQL | Execute a query against a database |
| Git | Run a git command in the repository |
| Pi | Send a prompt to an AI agent |
| Stripe | Process a refund, create a subscription |
| Email | Send a reply, forward a message |
| SSH | Run a command on a remote server |

This is the `run` operation in the kernel protocol. `run` = act.

### Perceive

The kernel observes the result and returns it to the shared
interface:

| Runtime | Perceive means |
|---|---|
| Python | Captured stdout, variable state |
| Slack | Message confirmation, channel history |
| SQL | Query results, affected rows |
| Git | Status output, diff |
| Pi | Agent's response text + token usage |
| Stripe | Payment status, customer data |
| Email | Inbox contents, search results |
| SSH | Command output |

This is the `look` operation (on-demand perception) and the `event`
operation (pushed perception). Together they make the kernel's
state observable.

### Loop

The RAPL loop advances when **anything** happens:

- The **human** types in the REPL
- An **agent** (Claude, Cursor, a script) calls via MCP
- The **world** pushes an event (new Slack message, email, webhook,
  price alert, CI notification)

This is what makes a RAPL fundamentally different from a REPL.
A REPL loop is driven by one user typing. A RAPL loop is driven
by any participant in the shared session — human, AI, or external
system.

The `event` mechanism in the kernel protocol is what enables the
third case. Without it, you have request/response. With it, the
kernel is a live connection to the world.

---

## The protocol maps directly to RAPL

```
Protocol operation    RAPL phase    Direction
──────────────────    ──────────    ─────────
run(code)             Act           client → kernel → world
look()                Perceive      client → kernel (on demand)
look(at=x)            Perceive      client → kernel (focused)
event                 Perceive      world → kernel → client (pushed)
ctl(op)               Control       client → kernel (meta)
```

The three MCP tools — `run`, `look`, `ctl` — are really
**act**, **perceive**, **control**.

---

## Why this matters

### REPLs are calculators. RAPLs are steering wheels.

A REPL computes. You give it an expression, it returns a value.
The world outside the process doesn't exist.

A RAPL steers. You give it intent, it acts on the world, and the
world talks back. The namespace isn't variables in memory — it's
the state of whatever system the kernel is connected to.

### REPLs are single-player. RAPLs are multiplayer.

A REPL has one user typing. A RAPL has a shared session where
humans, AI agents, and external events all participate. What Claude
runs, you see. What you type, Claude's context includes. What Slack
sends, both of you receive.

### REPLs evaluate. RAPLs interact.

The shift from Eval to Act is the shift from computation to agency.
An agent doesn't just evaluate expressions — it reads emails,
sends messages, queries databases, deploys code, buys things,
schedules meetings. Each of these is a RAPL kernel.

---

## Every rat kernel is a RAPL

```
rat py     → Read code,    Act (execute),   Perceive (output + vars)
rat sh     → Read command, Act (shell),     Perceive (stdout)
rat r      → Read R code,  Act (evaluate),  Perceive (results + vars)
rat pi     → Read prompt,  Act (LLM call),  Perceive (response)
rat slack  → Read text,    Act (send msg),  Perceive (confirmation + history)
rat sql    → Read query,   Act (execute),   Perceive (results + schema)
rat git    → Read command, Act (git op),    Perceive (status + diff)
rat email  → Read text,    Act (send/search), Perceive (inbox + threads)
rat shop   → Read command, Act (search/add), Perceive (cart + orders)
```

The kernel protocol — `run`/`look`/`ctl` + `event` — is the
universal RAPL interface.

```
rat = Run AnyThing
kernel protocol = RAPL (Read, Act, Perceive, Loop)
```
