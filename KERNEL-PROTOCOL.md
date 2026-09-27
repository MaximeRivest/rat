# rat kernel protocol

A rat kernel is a **RAPL** — Read, Act, Perceive, Loop. Unlike a
traditional REPL that evaluates expressions in local memory, a RAPL
acts on the external world (APIs, shells, models, people) and
perceives the results. The loop advances from humans, agents, or
external events. See [docs/rapl.md](docs/rapl.md) for the full concept.

A rat kernel communicates with the Go server via **JSON lines over
stdin/stdout** — one JSON object per line, in each direction.

Any language can implement a kernel. The protocol is the same for
Python, R, Slack, or your own runtime. If you can read stdin and
write stdout in JSON, you can be a rat kernel.

This protocol is used in two ways:
- by built-in kernels implemented in Go-backed packages like `internal/python`
- by generic runtimes loaded from `runtime.yaml` via `internal/generic/runtime.go`

In both cases, `rat serve` is the host: it starts the kernel subprocess,
wraps it as a `kernel.Kernel`, and exposes it through MCP.

---

## Transport

Two transports; `kernel.transport` in `runtime.yaml` picks one.

**`stdio`** (default) — the simplest kernel:

- **stdin →** kernel receives requests (one JSON object per line)
- **stdout ←** kernel sends responses (one JSON object per line)
- **stderr** is captured by the Go server for diagnostics only

The kernel must keep whatever the user's code prints off stdout (capture
it and return it in `output`), and the user's code cannot read stdin.

**`socket`** — for language kernels (R): requests and responses travel on
a private TCP connection. The kernel connects to `RAT_PROTOCOL_TCP_ADDR`
(`host:port`) and first sends `{"op": "protocol_hello", "token": "…"}`
with the value of `RAT_PROTOCOL_TOKEN`. The kernel's **stdout and stderr
are the user's output**: whatever the code prints — including programs
it starts and C code — reaches clients live, in order, with nothing to
capture. The kernel's stdin is empty; prompts go through `input_request`.
This is how rat's Python kernel runs.

Lines are delimited by `\n`. Each line is a complete JSON object.

Requests are answered **strictly in order**; there are no request ids.
A host that stops waiting for a reply (a `look` that took too long)
still expects it and drops it when it comes, so every request must get
exactly one reply — including a request interrupted by `cancel`.

## Interrupts

`kernel.interrupt` in `runtime.yaml` says what `cancel` does while a
request waits for its reply:

- **`kill`** (default): the kernel process is stopped; the next request
  starts a fresh one. Every variable is lost.
- **`signal`**: the kernel runs in its own process group and `cancel`
  sends it SIGINT, as Ctrl-C in a terminal would (programs the code is
  waiting on get it too). The kernel stops the running code, replies
  (`{"success": false, "error": "Interrupted"}` for a run) and keeps its
  state. A SIGINT that arrives between requests must be ignored. Some
  code cannot be interrupted (a Julia loop that never allocates, R deep
  in C code): a second `cancel`, 3 s or more after the first while the
  same request still waits, stops the kernel instead — as Ctrl-C again
  in a terminal — and says that the variables are lost. rat
  starts the kernel with SIGINT at its default even when rat itself runs
  with it ignored (a background job does), so the language's own
  handler is installed.
- **`message`**: `cancel` sends `{"op": "interrupt"}` on the protocol
  connection. The kernel must read it while code runs (a reader
  thread) and interrupt that code — rat's Python kernel calls
  `_thread.interrupt_main()`. The same escalation applies.

`kernel.interrupt_windows` says the same for Windows, which has no
signals: `message`, or `kill` (the default). A kernel that cannot read
its connection while code runs (R, Julia) is stopped there.

A `run` has no time limit: it lasts until the code ends or is cancelled.

## Request ids

Every request that gets a reply carries an `"id"` (an integer). A kernel
should echo it in its reply (`{"id": 7, "success": true, …}`); rat then
matches replies to requests by id and drops a late reply to a request
that stopped waiting. A kernel that does not echo ids still works:
replies are matched by order, as before.

## Displays

A value with a richer form than text — a plot, an image, an interactive
page (plotly, htmlwidgets) — is announced in the output, in its place
among the printed lines, by a line of its own:

    __RAT_PLOT__:/home/me/.cache/rat/plots/fig-….png      a PNG image
    __RAT_DISPLAY__:/home/me/.cache/rat/plots/….json      a display bundle

The file sits in rat's plot folder (`$XDG_CACHE_HOME/rat/plots`), and a
bundle is Jupyter's `display_data` content: `{"data": {mime: content},
"metadata": {…}}`, binary data in base64. Clients choose what to show
and keep: an interactive page (HTML with scripts, or HTML without a
useful text form) always sandboxed, an image, or the `text/plain`.
rat's kernels print static HTML that has a real text form (a data frame's
table) as text, and a lone PNG as `__RAT_PLOT__`, which clients older
than displays understand.

## Jupyter kernels

`kernel.type: jupyter` with `kernel.kernelspec: <name>` runs any
installed Jupyter kernel (ark, ir, julia-1.x, python3, …) as a rat
kernel: rat speaks Jupyter's wire protocol over ZeroMQ itself. Output
streams, displays become the lines above, input prompts, completion
(with `cursor_start`), inspection and interrupts (the kernelspec's
`interrupt_mode`) behave as for rat's own kernels. `kernel.overview` is
code whose printed output is the variable overview (Jupyter has no
request for it). An example, Posit's Ark kernel for R:

```yaml
name: ark
display: R (Ark)
detect:
  commands: [ark]
kernel:
  type: jupyter
  kernelspec: ark
frontend:
  type: repl
  prompt: "ark> "
```

## Packages

A runtime may manage the packages notebooks declare for it
(`rat.<key>.dependencies`) with a script of its own language:

```yaml
packages:
  key: r              # rat.r.dependencies
  script: packages.R  # run as: <binary> <args> <script> check|install|lock <project> [--force] [--update] -- <line>...
  args: [--no-save]
```

`check` prints facts, one per line, tab-separated (`requirement`,
`missing`, `problem`, `restart`, `relock`, `detail`, `summary`, `info`,
…); `install` and `lock` print their log and exit 0 when done. rat
itself knows no language here: the contract is in
`internal/notebook/runtimeenv.go`, and `internal/runtimes/r/packages.R`
and `internal/runtimes/jl/packages.jl` implement it (with lock files).

---

## Lifecycle

1. Go spawns the kernel script as a subprocess
2. Go sends `{"op": "ping"}` to verify the kernel is alive
3. Kernel responds `{"ok": true}`
4. Normal request/response loop begins
5. On shutdown, Go sends `{"op": "shutdown"}` — kernel exits

---

## Requests (Go → kernel)

### `ping`

Health check. Sent once at startup.

```json
{"op": "ping"}
```

Response:

```json
{"ok": true}
```

### `run`

Execute code. The kernel must maintain state between calls —
variables persist across `run` requests.

```json
{"op": "run", "code": "x = 42\nprint(x)", "allow_stdin": true}
```

Response:

```json
{
  "success": true,
  "output": "42",
  "error": "",
  "vars": 1
}
```

On error:

```json
{
  "success": false,
  "output": "",
  "error": "NameError: name 'y' is not defined",
  "vars": 1
}
```

Fields:
- `success` (bool) — did the code execute without error?
- `output` (string) — captured stdout
- `error` (string) — error message or traceback if `success` is false
- `vars` (int, optional) — number of user-visible variables (for the status hint)

If the last statement is an expression, the kernel should display its
repr in `output` (like a REPL would).

### `look_overview`

List all user-visible variables. Used by `rat look py` and the
`look()` MCP tool with no arguments.

```json
{"op": "look_overview"}
```

Response:

```json
{"text": "python idle | 2 vars\n\ndf    DataFrame  (1000 rows × 5 cols)\nx     int         42"}
```

The `text` field is pre-formatted for display. The first line should
be a status summary. Variables follow as aligned columns:
`name  type  preview`.

If there are no variables:

```json
{"text": "python idle | 0 vars"}
```

### `look_at`

Inspect a specific symbol. Used by `rat look py --at x` and
`look(at="x")`.

```json
{"op": "look_at", "at": "df"}
```

Response:

```json
{"text": "df: DataFrame (1000 rows × 5 columns)\n  = ...\n\n  ▸ columns  Index  ['region', 'quarter', 'revenue']"}
```

Return whatever is useful: type, value preview, children,
docstring, source location. Format is free-form text.

If the symbol doesn't exist:

```json
{"text": "df: not found"}
```

### `complete`

Code completions at a cursor position. Used by the REPL frontend
and `look(code="df.hea", cursor=6)`.

```json
{"op": "complete", "code": "df.hea", "cursor": 6}
```

Response:

```json
{"text": "head                 function\nheadline             variable"}
```

Each line: `label  kind`. Kinds: `function`, `variable`, `module`,
`keyword`, `value`. Return up to 50 completions.

A kernel that knows which text its completions replace says so — this
is what makes completion right for names with dots (`data.frame`), `$`,
`::` or non-ASCII letters, without clients guessing each language's
rules:

```json
{"text": "df$col1              variable",
 "start": 5,
 "matches": [{"label": "df$col1", "kind": "variable"}]}
```

- `start` — where the replaced text begins, in characters of `code`; it
  ends at the cursor. Each `label` replaces it whole.
- `matches` — the completions; a label may contain spaces or punctuation.

The host passes these on as the MCP result's structured content, and
`rat look <runtime> --code … --json` prints `{"start", "matches"}`
(`start` is `null` for a kernel that only sends `text`).

If none:

```json
{"text": "No completions."}
```

### `status`

Report kernel state. Used by health checks and `rat status -v`.

```json
{"op": "status"}
```

Response — one of:

```json
{"text": "idle"}
{"text": "busy"}
{"text": "waiting_for_input"}
```

The kernel may append extra `key: value` lines. The Go server adds
its own (idle time, memory, clients) before returning to the CLI.

```json
{"text": "idle\nruntime_version: Python 3.12.1"}
```

### `input`

Deliver text to a kernel that is waiting for user input (e.g.
Python's `input()`). This is sent while a `run` is in progress.

```json
{"op": "input", "text": "Alice\n"}
```

No reply of its own: the `run` in progress announces
`{"op": "input_delivered"}` once the code has the answer. (Older kernels
replied `{"ok": true}`; during a run the host takes only a message with
a `success` field as the run's reply, so such an acknowledgement is
harmless.) An `input` that arrives when nothing waits is ignored.

### `shutdown`

Clean exit.

```json
{"op": "shutdown"}
```

No response expected. The kernel should exit promptly.

---

## Streaming (optional)

During a `run`, the kernel may send intermediate messages before
the final response:

### `output_chunk`

Stream partial stdout while code is still running. Useful for
long-running code, progress bars, etc.

(A `socket` kernel does not need it: its stdout already streams.)

```json
{"op": "output_chunk", "text": "Processing row 500/1000\n"}
```

### `input_request`

Signal that the code is blocked waiting for stdin.

```json
{"op": "input_request", "prompt": "Enter your name: "}
{"op": "input_request", "prompt": "Password: ", "secret": true}
```

`secret` marks a password-style read (Python's `getpass`): clients use a
field that does not echo, and never write the answer into output or a
document.

The Go server numbers each request and relays it to every client of the
run as an MCP notification, `rat/input_request` with `{prompt, secret}`
(and `rat/input_done` when the program stops waiting). Whoever holds the
answer sends it with `run(input=...)`, which reaches the kernel as an
`input` message. Only a client that declared the MCP elicitation
capability is also asked through elicitation. `rat run` answers from its
own stdin; `rat run --events` hands the request to the program hosting
the run (see `rat run --help`).

### Following a kernel (events)

Every run is also published on the kernel's event bus, with the caller's
name (the MCP client name — `rat` sets it from `RAT_CALLER`), a sequence
number and a run id: `run_started` (with the code), `run_output` (live
chunks, every 50 ms — a quick run has none), `run_waiting` (prompt,
secret), `run_input_done`, `run_ended` (the whole output and error — it
also ends any wait), `look_called`, `ctl_called`. Connected sessions
receive them as `rat/event` notifications. A follower that must not miss
any polls the `tail` tool instead: `tail(format="events", since=N,
active=true)` returns every event after `N`, the runs in progress (with
their output so far and whether they wait), the server's `boot` id (a new
process starts over at seq 1) and `gap: true` when events after `N` have
already left the buffer. `rat events <runtime> --json` does exactly this
and prints one event per line; it never starts a kernel and follows it
across stops and restarts.

### `input_delivered`

Confirm that the input was received and execution resumed.

```json
{"op": "input_delivered"}
```

---

## Summary

| Direction | Op | When |
|---|---|---|
| Go → kernel | `ping` | Startup |
| Go → kernel | `run` | Execute code |
| Go → kernel | `look_overview` | Variable list |
| Go → kernel | `look_at` | Inspect symbol |
| Go → kernel | `complete` | Code completions |
| Go → kernel | `status` | Health check |
| Go → kernel | `input` | Deliver stdin text |
| Go → kernel | `interrupt` | Interrupt the running code (`interrupt: message`) |
| Go → kernel | `shutdown` | Clean exit |
| kernel → Go | `output_chunk` | Streaming stdout |
| kernel → Go | `input_request` | Blocked on stdin |
| kernel → Go | `input_delivered` | Stdin received |
| kernel → Go | `event` | Pushed notification (anytime) |

---

## Events (pushed notifications)

The operations above are request/response — the kernel only speaks
when spoken to. Events are the opposite: the kernel pushes
information to the host **at any time**, without being asked.

This is essential for runtimes that receive external input:
- Slack: a new message arrives in the channel
- Email: new mail in inbox
- Price watcher: price dropped below threshold
- Long-running task: progress update
- Webhook: external system triggered a callback

### `event`

Kernel sends this on stdout at any time — during a `run`, between
requests, or while completely idle.

```json
{"op": "event", "type": "message", "data": {"from": "@alice", "text": "hey!", "channel": "#general"}}
```

Fields:
- `op` (string) — always `"event"`
- `type` (string) — event category. Common types below.
- `data` (object) — event payload. Structure depends on type.

The host:
1. Writes the event to the activity log (so REPL frontends see it)
2. Can forward to MCP clients as notifications (future)

### Common event types

These are conventions, not requirements. Kernels can define any type.

#### `message`

A new message was received. Used by communication runtimes (Slack,
email, WhatsApp, Discord).

```json
{"op": "event", "type": "message", "data": {
  "from": "@alice",
  "text": "hey team, standup in 5",
  "channel": "#general",
  "ts": "1712345678.123456"
}}
```

#### `progress`

Progress update for a long-running operation. Can be emitted during
a `run` (alongside `output_chunk`) or from a background task.

```json
{"op": "event", "type": "progress", "data": {
  "pct": 45,
  "msg": "Training epoch 45/100",
  "eta": "2m30s"
}}
```

#### `alert`

Something notable happened that the user/agent should know about.

```json
{"op": "event", "type": "alert", "data": {
  "level": "info",
  "msg": "RTX 5090 price dropped to $780!"
}}
```

#### `error`

An error occurred outside of a `run` (e.g., connection lost).

```json
{"op": "event", "type": "error", "data": {
  "msg": "Slack connection lost, reconnecting..."
}}
```

### Event timing

Events can arrive:
- **During a `run`**: the host reads them alongside `output_chunk`
  and other streaming messages, dispatches to the event handler,
  and continues waiting for the final response.
- **Between requests**: the host's background reader picks them up
  and dispatches immediately.
- **While idle**: same as between requests.

The host never blocks on events. They are fire-and-forget from the
kernel's perspective.

### Activity log format for events

Events are written to the same activity.jsonl file as execution
records, with a different shape:

```json
{"event": "message", "data": {"from": "@alice", "text": "hey!"}, "t": 1712345678}
```

REPL frontends can distinguish events from executions by checking
for the `event` field (executions have `n`, `code`, `output`).

---

## REPL frontend pattern

The kernel protocol defines the **runtime side**. This section defines the
matching **frontend side** used by `rat <lang>`.

**Rule:** use the language's native REPL UI if possible. Redirect only the
smallest possible surface area to the shared kernel.

In other words:
- keep the native prompt, editing, history UI, syntax highlighting, and modes
- redirect execution, completion, inspection, and control to the kernel
- treat the kernel as the single source of truth for runtime state

### Frontend contract

A rat REPL frontend should implement these logical operations, using whatever
hooks the host REPL exposes:

- `execute(code)` → host `run` → kernel `run`
- `complete(code, cursor)` → host `look(code, cursor)` → kernel `complete`
- `inspect(at)` → host `look(at)` → kernel `look_at`
- `status()` → host `ctl(status)` → kernel `status`
- `interrupt()` → host cancel path (`ctl(cancel)` / process signal)
- `exit()` → exit the frontend only; do not kill the kernel

### Source of truth

The **kernel** owns:
- namespace / variables
- execution count
- completion context
- inspect results
- busy / waiting state

The **host** (`rat serve` + Go wrapper) owns:
- subprocess lifecycle
- restart / shutdown policy
- MCP exposure
- activity tracking
- transport adaptation between kernel protocol and `kernel.Kernel`

The **frontend** owns:
- terminal rendering
- keybindings
- prompt editing
- local redraw / screen state

If a frontend keeps its own execution namespace, it will drift from the shared
kernel. Avoid that.

### Frontend lifecycle

1. Resolve the runtime name and ensure the kernel is running
2. Launch the native REPL frontend for that language
3. Patch or hook its eval / completion / inspect entry points
4. Translate frontend actions into host calls, which become kernel protocol messages
5. Render returned text naturally inside the native REPL
6. On exit, close the frontend only

### Choosing a frontend pattern

```
Does the REPL expose eval/completion hooks?
  │
  ├─ YES → Pattern B: Hook native eval
  │        Use the real REPL, redirect eval to the kernel.
  │        MCP clients get clean JSON. Humans get the real UX.
  │
  └─ NO
      │
      Can you run the REPL inside tmux?
        │
        ├─ YES → Pattern A: Attach to real session
        │        Humans get tmux attach. MCP injects via send-keys
        │        and scrapes output. Simple but output is unstructured.
        │
        └─ NO  → Pattern C: Thin wrapper
                 Minimal custom REPL. Last resort.
```

#### Pattern A: Attach to real session (bash)

The REPL runs inside tmux. `rat sh` is just `tmux attach`.
MCP commands inject code via `tmux send-keys` and capture output
via `tmux pipe-pane`.

- **Pro:** zero frontend code, humans get the completely real shell
- **Con:** MCP output is scraped terminal text — fragile, needs
  cleanup (stripping prompts, escape codes, noise)
- **Use when:** the REPL has no programmatic hooks (bash, zsh, fish)

#### Pattern B: Hook native eval (Python, Node, Julia, R)

The REPL runs natively. A small adapter hooks its eval and
completion entry points to route through the kernel over JSON.

- **Pro:** clean structured MCP responses, proper errors, streaming,
  real REPL UX for humans — best of both worlds
- **Con:** requires the REPL to expose hooks (~200 lines of adapter)
- **Use when:** the REPL has eval hooks (IPython `run_cell`, Node
  `vm.runInContext`, Julia `Base.eval`, R `evaluate`)

rat's Python frontend does exactly this: full IPython UX, but
`run_cell` POSTs to the MCP kernel, completions call `look`,
and the user never knows execution happens remotely. For generic
runtimes, the same pattern applies: the frontend talks to
`rat serve`, not directly to the kernel subprocess.

#### Pattern C: Thin wrapper (last resort)

A minimal custom REPL that reads input, sends it to the kernel,
prints the response. No syntax highlighting, no multiline editing,
no history beyond what readline provides.

- **Pro:** works for anything
- **Con:** poor human UX
- **Use when:** no hooks AND no tmux option (rare)

#### Why not always use tmux?

Tempting — one pattern for everything. But tmux means MCP clients
get scraped terminal text instead of structured JSON. For a language
like Python, Claude would receive:

```
In [3]: df.head()
Out[3]:
   region  quarter  revenue
0  East    Q1       42000

In [4]:
```

Instead of:

```json
{"success": true, "output": "   region  quarter  revenue\n0  East    Q1       42000", "vars": 3}
```

The first needs heuristic cleanup. The second is machine-readable.
If the language can give you structured output, take it.

### Local vs remote behavior

Every frontend feature should be classified explicitly.

**Usually local:**
- prompt rendering
- line editing
- terminal keybindings
- frontend-only help/UI commands

**Usually remote:**
- normal code execution
- object inspection
- runtime-aware completion
- interrupt / cancel
- shared-session state

If a command changes runtime state, it should generally be handled by the
kernel, not the frontend.

### Shared session awareness

Because multiple clients share one kernel, the frontend should consider
showing activity from other clients. When Claude runs code while you're
in the REPL, you should see it — otherwise the namespace changes
silently and you're confused.

The Python frontend uses an activity log (JSONL file written by the Go host,
polled by the frontend) to display other clients' executions inline.
This is optional but strongly recommended for interactive frontends.

### Design target

For every language, aim for:

> native REPL feel, shared kernel truth, minimal adapter surface.

That is the rat REPL pattern.

---

## Writing a kernel

A minimal kernel in any language:

1. Read lines from stdin
2. Parse each line as JSON
3. Dispatch on `op`
4. Write a JSON response line to stdout
5. Keep state between calls

If you are building a custom language runtime for rat, the usual shape is:

- `runtime.yaml` declares how to detect the language binary, where the kernel
  script lives, and what frontend command rat should launch
- `rat serve` loads that config
- `internal/generic/runtime.go` starts your kernel script and speaks this protocol
- your frontend talks to `rat serve` / MCP, not directly to the script

The Python reference implementation is `internal/python/kernel.py`.
A generic external runtime should aim to match the same semantics.
A minimal kernel that only supports `run` and `ping` can be under 50 lines.

### Minimal example (Python)

```python
import json, sys, traceback

namespace = {}

for line in sys.stdin:
    req = json.loads(line)
    op = req.get("op")

    if op == "ping":
        print(json.dumps({"ok": True}), flush=True)

    elif op == "run":
        try:
            exec(req["code"], namespace)
            print(json.dumps({"success": True, "output": "", "error": ""}), flush=True)
        except Exception:
            print(json.dumps({"success": False, "output": "", "error": traceback.format_exc()}), flush=True)

    elif op == "look_overview":
        print(json.dumps({"text": "no inspection support"}), flush=True)

    elif op == "look_at":
        print(json.dumps({"text": "no inspection support"}), flush=True)

    elif op == "complete":
        print(json.dumps({"text": "No completions."}), flush=True)

    elif op == "status":
        print(json.dumps({"text": "idle"}), flush=True)

    elif op == "shutdown":
        break
```

That's a working rat kernel. 20 lines.
