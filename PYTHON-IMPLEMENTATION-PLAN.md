# Python implementation plan for rat

This document turns the current rat architecture into a concrete plan for `py`.

It is based on what we learned shipping `sh`:
- use the real frontend when the language already has a great REPL
- keep Go as the lifecycle + MCP control plane
- keep the language kernel thin and explicit
- design stdin, completion, and inspection up front
- avoid reimplementing terminal UX in Go

---

## Goal

Make Python feel like this:

- `rat run py 'x = 42'` works
- `rat look py --at x` works
- `rat look py --code 'df.he' --cursor 5` works
- `rat py` feels like real IPython
- Claude / Cursor / scripts / terminal all share one Python namespace
- `input()` works from both MCP and the terminal frontend
- install is low-friction on macOS, Linux, and Windows

---

## Current implementation status

### Done now
- `rat run py 'x = 42'`
- `rat run py 'print(x)'`
- `rat look py`
- `rat look py --at x`
- `rat look py --code 'impor'`
- `rat look py --code 'math.sq'`
- `rat reset py`
- `rat cancel py`
- `input()` round-trips via MCP `run(input=...)`
- cancelling while blocked in `input()` produces `KeyboardInterrupt`

### Implemented files
- `internal/python/python.go`
- `internal/python/kernel.py`
- `cmd/rat/commands/serve.go` now wires `py`

### Not done yet
- `rat py` native IPython frontend
- richer structured inspection for Python objects
- rich display / assets
- `rat install py`
- Python-specific `rat doctor`
- named-runtime / venv selection beyond the current interpreter detection

---

## Final architecture

```text
Human terminal
   │
   └── rat py
         │
         └── embedded rat_ipython_frontend.py
               │
               └── real IPython TerminalInteractiveShell
                     │
                     └── MCP HTTP
                           │
                           └── rat serve py (Go)
                                 │
                                 └── python kernel subprocess
                                       │
                                       └── persistent Python namespace
```

### Responsibilities

#### Go
- CLI
- daemon / auto-start / restart / stop
- state tracking
- MCP HTTP server
- multi-client serialization
- `run`, `look`, `ctl` tool surface
- install / doctor / runtime detection

#### Python kernel subprocess
- execute Python code in one persistent namespace
- capture stdout / stderr
- capture rich display output
- inspect variables
- compute completions
- support `input()` / stdin requests
- reset / cancel execution

#### IPython frontend script
- provide native terminal UX
- replace execution path with MCP calls
- replace completion path with MCP completion calls
- optionally replace inspect/help path with MCP inspection calls
- show subtle shared-kernel status hints

---

## Technology choices

## Use

### Frontend
- **IPython**
- `TerminalInteractiveShell`
- `prompt_toolkit` indirectly via IPython

### Python kernel internals
- **IPython execution engine** (`InteractiveShell` or close equivalent)
- **jedi** for completions
- stdlib `inspect`, `reprlib`, `traceback`, `signal`, `threading`, `queue`

### Internal transport
- **JSON over pipes** between Go and Python subprocess

### External protocol
- **MCP over HTTP** from clients to Go server

## Do not use initially
- custom Go readline REPL
- tmux for Python
- Jupyter kernel protocol / ZMQ
- notebook protocol as the internal primitive

---

## Why this is the right split

## Why not tmux?

tmux was right for `sh` because bash itself is the terminal REPL.

For Python, we want:
- many frontends
- one runtime
- each frontend with its own local editing/history UI

That is exactly what IPython-frontends + shared-kernel gives us.

## Why not Jupyter first?

Jupyter kernels add a lot of complexity:
- ZMQ channels
- message framing
- signing/session semantics
- notebook-oriented protocol assumptions

rat already has:
- Go lifecycle
- MCP external API
- a simpler three-tool model

So the simplest correct approach is:
- custom thin Python kernel
- custom thin IPython frontend
- plain JSON between Go and Python

---

## Core design rules

1. **The Python namespace lives only in the Python kernel subprocess.**
2. **Terminal frontends are disposable clients, not the source of truth.**
3. **Completion and inspection come from the shared kernel, not the local frontend.**
4. **stdin support is first-class from day one.**
5. **Execution is serialized across all clients.**
6. **The IPython frontend should feel native; shared behavior should be visible but subtle.**

---

## Kernel protocol between Go and Python

Use newline-delimited JSON or length-delimited JSON messages over stdio.

Recommended message shape:

```json
{
  "id": "123",
  "op": "run",
  "code": "x = 42"
}
```

Responses/events:

```json
{ "id": "123", "type": "stdout", "text": "hello\n" }
{ "id": "123", "type": "stderr", "text": "warning\n" }
{ "id": "123", "type": "display_data", "mime": "text/plain", "data": "42" }
{ "id": "123", "type": "result", "text": "42" }
{ "id": "123", "type": "stdin_request", "prompt": "Name: ", "password": false }
{ "id": "123", "type": "error", "ename": "ValueError", "evalue": "bad", "traceback": ["..."] }
{ "id": "123", "type": "done", "success": true }
```

## Required ops

### `run`
Request:
```json
{ "id": "1", "op": "run", "code": "x = 42" }
```

### `look_overview`
Request:
```json
{ "id": "2", "op": "look_overview" }
```

### `look_at`
Request:
```json
{ "id": "3", "op": "look_at", "at": "df" }
```

### `complete`
Request:
```json
{ "id": "4", "op": "complete", "code": "df.he", "cursor": 5 }
```

### `reset`
Request:
```json
{ "id": "5", "op": "reset" }
```

### `cancel`
Request:
```json
{ "id": "6", "op": "cancel" }
```

### `stdin_response`
Request:
```json
{ "id": "7", "op": "stdin_response", "text": "Bob\n" }
```

---

## Python kernel behavior

## 1. Execution model

The kernel keeps:
- one global namespace dict
- one `InteractiveShell` instance or equivalent execution context
- one currently-running execution slot

`run` should:
- execute code in the persistent namespace
- stream stdout/stderr as events
- capture final expression / displayhook output
- return structured success/error information

## 2. stdin model

When Python code calls `input()`:
- kernel emits `stdin_request`
- Go pauses the current `run`
- MCP clients can answer via `run(input=...)`
- `rat py` answers naturally through the IPython frontend

Need both:
- normal text input
- password mode later (`getpass`)

## 3. cancellation model

`ctl(cancel)` should:
- interrupt the running Python execution
- ideally via `SIGINT` to the subprocess
- or via IPython-compatible interrupt path

Need to test:
- long loops
- `time.sleep()`
- blocking `input()`

## 4. reset model

`ctl(reset)` should:
- create a fresh namespace
- reset execution count if relevant
- preserve the process if possible

If reset-in-place is messy, restart the subprocess and rebuild the shell state.

---

## Python `look` design

Python needs a richer `look` than shell.

## `look()` overview

Return a variable table with columns like:
- name
- type
- preview
- optional shape / length

Examples:
- `x` → `int 42`
- `name` → `str 'Alice'`
- `df` → `DataFrame (1000, 5)`
- `arr` → `ndarray (100, 20)`
- `items` → `list len=42`

## `look(at='x')`

Return richer inspection text:
- type
- truncated repr
- for common structures, useful metadata

Special-case support worth adding early:
- pandas DataFrame / Series
- numpy ndarray
- dict/list/set/tuple
- function/method/class/module

## `look(code, cursor)`

Use `jedi` with the live namespace when possible.

Completion output should include:
- label
- kind
- optional detail/type

The current rat `look` CLI can already print text completions, so the Python kernel can return either:
- structured completions internally
- formatted text at the Go MCP boundary

---

## IPython frontend design

## Launch model

`rat py` should:
1. ensure the `py` kernel is running
2. find the selected interpreter / venv
3. launch embedded `rat_ipython_frontend.py` with that interpreter
4. connect it to the kernel MCP URL

## Why same venv matters

The frontend must run in the same environment as the kernel so that:
- imports match
- syntax help matches
- local IPython niceties behave consistently

Even though execution is remote, the frontend still benefits from matching packages.

## What the frontend overrides

### Override execution
Map `run_cell()` to MCP `run(code=...)`.

### Override completion
Map completion requests to MCP `look(code=..., cursor=...)`.

### Override inspect/help if needed
For `x?`, `x??`, etc:
- either intercept and call MCP `look(at=...)`
- or support a minimal first version where basic help is enough and improve later

## What remains local to IPython
- history navigation
- prompt rendering
- multiline editing
- syntax highlighting
- `%magic` commands that are purely frontend concerns
- shell escapes like `!ls` (decide explicitly; probably local)

---

## Shared activity UX

We learned from `sh` that users need awareness when the session is shared.

For Python, do not use a tmux bar.

Instead use subtle IPython/prompt_toolkit UI:
- bottom toolbar text like `rat py | shared kernel :8717`
- transient notices like:
  - `Claude is running code...`
  - `Kernel waiting for input...`
  - `Kernel restarted`

This should be informative, not noisy.

---

## Install / doctor design for Python

## `rat doctor`

Add Python checks:
- detected interpreter(s)
- Python version
- venv path
- pip availability
- IPython installed?
- jedi installed?
- config/cache dirs writable
- Windows support status

## `rat install py`

### Step 1: interpreter detection
Order to try:
- active venv Python
- `python3`
- `python`
- Windows launcher `py -3`

### Step 2: environment detection
Check in cwd and parents for:
- `.venv`
- `venv`

Later optional support:
- poetry
- pdm
- uv
- conda

### Step 3: install dependencies
Install into chosen venv/interpreter:
- `ipython`
- `jedi`

Later maybe:
- `rich`
- `pandas` integration helpers (not required)

### Step 4: start kernel
Start background `py` kernel via daemon.

### Step 5: smoke tests
Run:
- `print('rat py ready')`
- `x = 42`
- `look(at='x')`
- completion on `impor`

### Step 6: print next steps
```text
Ready.
Try:
  rat py
  rat run py 'print(42)'
  rat look py --at x
```

---

## Cross-platform strategy

## Linux
- first-class support
- easiest development target

## macOS
- first-class support
- should be close to Linux

## Windows
- should support Python natively
- unlike `sh`, should not require WSL

Need explicit testing for:
- subprocess signaling / cancel
- path quoting
- venv discovery
- console behavior of `rat py`

---

## Concrete implementation phases

## Phase 1 — Python kernel MVP

### Status
**Done.**

### Goal
Make these work:
- `rat run py 'print(42)'`
- `rat look py`
- `rat look py --at x`
- `rat look py --code 'impor'`
- `rat reset py`
- `rat cancel py`

### Files added
- `internal/python/python.go`
- `internal/python/kernel.py`

### Kernel MVP features
- persistent namespace
- execution
- stdout/stderr capture
- basic error handling
- variable overview
- inspect-by-name / expression
- jedi completions with fallback completion logic
- reset
- cancel via subprocess interrupt

### Out of scope for MVP
- rich media
- full IPython parity
- frontend `rat py`

## Exit criteria
- `rat run/look/reset/cancel py` all work from CLI and MCP
- completed

---

## Phase 2 — stdin and richer inspection

### Status
**Partially done.**

Implemented already:
- `input()` support
- stdin request/response plumbing
- cancellation during input waits

Still to do in this phase:
- better inspection for common Python objects
- richer structured metadata for functions/classes/modules
- snapshot/busy-time introspection if needed

### Goal
Support interactive Python cleanly.

### Add
- `input()` support
- stdin request/response plumbing
- better inspection for common Python objects
- stable cancellation during input waits

### Tests
- `name = input('Name: ')` ✅
- long-running loop + cancel ✅
- cancel while blocked in `input()` ✅
- inspect dataframe-like objects when available ⏳

## Exit criteria
- MCP-driven interactive Python works
- partially completed

---

## Phase 3 — `rat install py` and `rat doctor`

### Goal
Low-friction Python onboarding.

### Add
- interpreter detection
- venv selection
- dependency installation
- smoke tests
- clear fix hints on missing Python / pip / broken venv

## Exit criteria
- fresh user can run `rat install py` and get a working kernel

---

## Phase 4 — `rat py` native frontend

### Goal
Make `rat py` feel like IPython.

### Files to add
- `internal/python/frontend.py`
- embed in Go via `go:embed`

### Frontend features
- real IPython shell
- MCP-backed execution
- MCP-backed completion
- basic shared-kernel toolbar/status

## Exit criteria
- user can barely tell the difference from running `ipython` directly
- except that Claude shares the same namespace

---

## Phase 5 — polish

### Nice-to-haves
- rich display data in terminal-aware form
- dataframe-aware previews
- better help / `?` / `??`
- multiple named Python runtimes with better environment resolution
- client-awareness in `rat ls`
- auto-stop idle kernels

---

## Tests to add

## CLI tests
- `rat run py 'print(42)'`
- `rat run py 'x=42'` then `rat look py --at x`
- `rat look py --code 'impor'`
- `rat reset py`

## MCP tests
- initialize session
- call `run`
- call `look(at=...)`
- call `look(code,cursor)`
- call `ctl(cancel)` during execution
- interactive stdin round-trip

## Frontend tests
- `rat py` launches
- completion works for shared variables
- code executed by Claude appears in the shared namespace
- frontend handles kernel restart cleanly

---

## Open design questions

1. Should `%run script.py` execute file contents on the shared kernel or remain local-only?
   - Recommendation: read local file, send contents to kernel.

2. Should `!shell` inside `rat py` be local or shared?
   - Recommendation: local, because it is a frontend convenience, not Python runtime state.

3. How much of IPython help (`?`, `??`) should be mapped to `look` vs handled locally?
   - Recommendation: start with simple MCP-backed inspection for Python names/objects and improve incrementally.

4. How should rich display data be represented through MCP?
   - Recommendation: keep text first, add structured display payloads later.

---

## Recommendation summary

Best stack for Python in rat:
- **frontend:** IPython
- **kernel:** thin custom Python subprocess
- **internal protocol:** JSON over pipes
- **external protocol:** MCP
- **completion:** jedi + shared namespace
- **control plane:** Go
- **installation:** interpreter/venv-aware, cross-platform

The guiding principle is the same one that worked for shell:

> borrow the real REPL, build only the glue, keep the shared runtime explicit.
