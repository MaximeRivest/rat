# Implementation plan: CLI spec ↔ codebase alignment

## Current state of the codebase

### What exists and works
- `rat serve sh|py` — MCP server (HTTP + stdio) for bash and Python
- `rat start <name>` — background kernel launch via daemon.Start
- `rat stop <name> [--all]` — SIGTERM + state cleanup
- `rat restart <name>` — stop + start
- `rat run <name> 'code'` — execute code via MCP
- `rat look <name> [--at x]` — inspect variables
- `rat cancel <name>` — interrupt execution
- `rat reset <name>` — clear namespace
- `rat add <name> [dir]` — register named runtime
- `rat rm <name>` — unregister
- `rat ls` — list running kernels
- `rat install sh|py` — detect + install + smoke test
- `rat doctor sh|py` — diagnostics
- `rat py` / `rat sh` — REPL (IPython frontend, tmux attach)
- `rat version` — print version
- State management (state.yaml with kernels + runtimes)
- Project root detection, venv detection, Python env detection
- Language alias resolution (lang.go)
- Health check (full MCP round-trip)

### What doesn't exist yet
- `rat status` (spec's replacement for `rat ls`)
- `resolve()` as the unified function the spec describes
- Persistent stopped-state entries (auto-idle marks stopped, not deletes)
- State GC (30-day pruning of stale stopped entries)
- `rat stop --all --clean`
- `rat r`, `rat jl`, `rat js` kernels/REPLs
- R (radian), Julia (ast_transforms), Node (repl module) frontends
- `rat install r|jl|js`
- `rat doctor r|jl|js`
- Auto-idle timeout
- Client tracking (who's connected)
- `rat setup` wizard
- `rat update`
- Claude Desktop / Cursor config writing (configure_clients)

---

## Gap analysis: spec vs code

### 1. Resolution — the biggest architectural gap

**Spec says:** One `resolve()` function, used by every command. Language
aliases go through project-aware expansion (step 2). Full names go
through exact match (step 1) or prefix match (step 3).

**Code has:** Three separate resolution paths:
- `connectToKernel()` — checks running → auto-starts (looks up saved
  runtime or falls back to language alias, bare name, no project logic)
- `handleREPL()` → `handleProjectREPL()` — project-aware naming
  (resolveProjectKernelName), but only for the REPL command
- `startCmd` — different again: literal name or language alias,
  optional dir arg changes semantics

**The fix:** Extract a single `resolve(input, cwd) → (name, lang, cwd,
venv, isNew)` function that every command calls. This is the spec's
step 1→2→3→4 algorithm. Then `connectToKernel`, `handleREPL`, and
`startCmd` all call `resolve()` first.

### 2. State lifecycle — stopped entries not preserved

**Spec says:** `rat stop` marks as stopped, state entry stays.
`rat rm` is the only way to fully erase. Auto-idle marks stopped,
not deletes. Stopped entries are visible in `rat status` and
resolvable by name.

**Code has:** `stopKernel()` calls `store.Remove(k.Name)` — deletes
the entry entirely. `state.Store` has no concept of a stopped kernel.
The `Kernel` struct has no `Status` field.

**The fix:**
- Add `Status string` field to `state.Kernel` ("running", "stopped")
- `stopKernel()` sets status to "stopped" instead of removing
- `store.List()` returns all entries (running + stopped)
- Add `store.ListRunning()` for when you only want alive kernels
- `store.Remove()` stays as the hard delete (used by `rat rm`)
- PID cleanup on read: if status="running" but PID dead → mark stopped

### 3. `rat ls` → `rat status`

**Spec says:** `rat status` with columns: NAME, STATUS, CWD, VENV.

**Code has:** `rat ls` with columns: NAME, LANG, PORT, STATE, VENV,
CWD, STARTED. Only shows running kernels.

**The fix:** Rename `lsCmd` to `statusCmd` (keep `ls` as alias for
backward compat). Show running + stopped. Use spec's column layout.

### 4. `rat rm` scope

**Spec says:** Works on any runtime — auto-generated or custom.

**Code has:** Only removes saved runtimes (from `rat add`), not
running/stopped kernels.

**The fix:** `rmCmd` should check both `store.GetRuntime()` (saved
configs) and `store.Get()` (kernel entries). Stop if running, then
remove both entries.

### 5. `rat restart` auto-start when nothing exists

**Spec says:** "If no kernel is running, starts a fresh one (same
auto-start as `rat py`)."

**Code has:** If not running, tries `resolveLang(name)` and starts
fresh. But doesn't go through project resolution.

**The fix:** Route through `resolve()` so `rat restart py` from
`~/Projects/foo` resolves to `py@foo` and auto-starts.

### 6. `rat cancel` / `rat reset` — no auto-start

**Spec says:** These do NOT auto-start.

**Code has:** Both use `connectToKernel()` which DOES auto-start.

**The fix:** Add a `connectToRunningKernel()` (or pass a flag) that
errors if not running instead of auto-starting.

### 7. Help output doesn't match spec

**Spec says:** Grouped into "Daily use" and "Setup & management"
with specific descriptions.

**Code has:** Cobra default help with old descriptions.

### 8. `run` and `look` don't go through resolve()

**Spec says:** `rat run py 'code'` resolves py → py@project.

**Code has:** `rat run py 'code'` uses `connectToKernel("py")` which
tries exact match "py", then auto-starts a bare "py" kernel — no
project resolution.

### 9. Project root markers missing Julia

**Spec says:** `Project.toml`, `JuliaProject.toml`, `renv.lock`.

**Code has:** Only the original markers (no Julia/R).

---

## Implementation order

### Phase 0: Foundation (do first, everything depends on it)

#### 0a. `resolve()` — the unified resolution function

```
File: internal/resolve/resolve.go (new package)

func Resolve(s *state.Store, input string, cwd string) (*Result, error)

type Result struct {
    Name   string   // resolved kernel name
    Lang   string   // canonical language
    Cwd    string   // working directory
    Venv   string   // detected venv (py only)
    IsNew  bool     // true if this kernel doesn't exist yet
    Source string   // "exact" | "language" | "prefix" | "new"
}
```

Algorithm (from spec):
1. Exact match: running kernel or saved runtime with this exact name
2. Language alias: compute canonical name for cwd (lang@project),
   check if it exists (running or saved) → use it, else return as new
3. Prefix match: collect all whose name starts with input
   - 1 match → use it
   - N matches → error with list
4. Error: no match

This replaces: `connectToKernel`'s name lookup, `handleProjectREPL`'s
resolution, `startCmd`'s name handling. One function, everywhere.

#### 0b. State lifecycle: stopped entries

```
File: internal/state/state.go

Add to Kernel:
    Status  string    `yaml:"status"`  // "running" or "stopped"

Modify:
    List()          → returns all (running + stopped)
    ListRunning()   → new, returns only status=running with live PIDs
    partitionAlive  → marks dead PIDs as stopped, doesn't delete
```

#### 0c. Add project root markers

```
File: cmd/rat/commands/project.go

Add: "Project.toml", "JuliaProject.toml", "renv.lock"
```

### Phase 1: Wire resolve() into all commands

#### 1a. `connectToKernel()` uses resolve()

Replace the current auto-start logic with:
```go
func connectToKernel(ctx context.Context, input string) (*mcpclient.Session, error) {
    cwd, _ := os.Getwd()
    r, err := resolve.Resolve(store(), input, cwd)
    // if r.IsNew or kernel stopped → auto-start
    // if running → connect
}
```

Add `connectToRunningKernel()` variant that errors if not running
(for cancel, reset).

#### 1b. `rat run` and `rat look` — go through resolve()

These already call `connectToKernel`, so 1a fixes them automatically.

#### 1c. `rat cancel` and `rat reset` — no auto-start

Switch from `connectToKernel` to `connectToRunningKernel`.

#### 1d. `rat <lang>` REPL — use resolve()

Replace `handleREPL` / `handleProjectREPL` / `handleNamedREPL` with:
```go
func handleREPL(input string) error {
    cwd, _ := os.Getwd()
    r, err := resolve.Resolve(store(), input, cwd)
    // ensure kernel running (same as connectToKernel path)
    // launch REPL
}
```

#### 1e. `rat start`, `rat stop`, `rat restart` — use resolve()

All go through resolve(). `rat start py` from a project dir
resolves to `py@project`.

### Phase 2: State & status commands

#### 2a. `rat stop` preserves state

`daemon.stopKernel()` sets status="stopped" instead of removing.

#### 2b. `rat status` replaces `rat ls`

Rename command. New column layout: NAME, STATUS, CWD, VENV.
Show running + idle + stopped. Keep `ls` as hidden alias.

#### 2c. `rat rm` expanded scope

Check both kernel entries and saved runtimes. Stop if running,
then hard-delete from state.

#### 2d. State GC

On any `rat` invocation, prune stopped entries older than 30 days.
Configurable via `RAT_GC_DAYS`.

### Phase 3: Help & docstrings

#### 3a. Root command help

```
rat — Run AnyThing

  rat py                        Enter your Python world
  rat run py '…'                One-liner
  rat look py [--at x]          See what's inside
  rat cancel py                 Unstick
  rat restart py                Fresh start
  rat status                    What's running

Setup & management:
  rat install py                Full setup (venv, deps, configure Claude)
  rat doctor [py]               Diagnostics
  rat start <name>              Start a kernel
  rat stop <name> [--all]       Stop a kernel
  rat add <name> [dir]          Register a named runtime
  rat rm <name>                 Delete a runtime's state
  rat reset <name>              Clear namespace (keep process)
  rat serve <name> [--http]     MCP server (for app builders)

Every command accepts a language (py, sh, r, jl, js) or a full
kernel name (py@myproject, py-ml). Languages auto-resolve to the
kernel for your current project.

See: rat <command> --help
```

#### 3b. Per-command docstrings

```
rat run:
  Use:   "run <runtime> '<code>'"
  Short: "Run code on a kernel"
  Long:  "Execute code on a kernel. Resolves the runtime name, auto-starts
          if needed, runs the code, prints output.

          The runtime can be a language (py, sh, r, jl, js) which resolves
          to your current project's kernel, or a full name (py@myproject,
          py-ml).

          Examples:
            rat run py 'x = 42'
            rat run py 'print(x)'
            rat run sh 'ls -la'
            rat run py@myproject 'df.head()'
            rat run py-ml 'import torch'"

rat look:
  Use:   "look <runtime> [--at <symbol>]"
  Short: "Inspect variables and state"
  Long:  "Inspect a kernel's namespace. Without --at, shows a variable
          overview. With --at, inspects a specific symbol in detail.

          Examples:
            rat look py                 # variable overview
            rat look py --at df         # inspect df in detail
            rat look py --at df.columns # drill into attribute"

rat cancel:
  Use:   "cancel <runtime>"
  Short: "Cancel running execution"
  Long:  "Interrupt the current execution on a kernel (Ctrl-C equivalent).
          Does NOT auto-start — if the kernel isn't running, reports it.

          Example:
            rat cancel py"

rat restart:
  Use:   "restart <runtime>"
  Short: "Restart a kernel (fresh namespace)"
  Long:  "Kill the kernel process and start a new one. Fresh namespace,
          fresh language subprocess. If no kernel is running, starts one.

          Examples:
            rat restart py
            rat restart py@myproject"

rat reset:
  Use:   "reset <runtime>"
  Short: "Clear namespace without restarting"
  Long:  "Clear the namespace in-process. Faster than restart but less
          reliable. Does NOT auto-start — the kernel must be running.

          For a full restart: rat restart <name>"

rat status:
  Use:   "status"
  Short: "Show all runtimes and their state"

rat install:
  Use:   "install <lang> [<lang>...]"
  Short: "Full setup (venv, deps, configure Claude)"
  Long:  "Set up a language runtime for this project. Creates a venv
          (Python), installs dependencies, starts the kernel, runs a
          smoke test, and configures Claude Desktop and Cursor.

          Examples:
            rat install py
            rat install py r sh"

rat start:
  Use:   "start <runtime>"
  Short: "Start a kernel"
  Long:  "Resolve the name and start the kernel in the background.
          If already running and healthy, reports it.

          Examples:
            rat start py              # resolves: py@myproject
            rat start py@myproject    # explicit
            rat start py-ml           # named runtime"

rat stop:
  Use:   "stop <runtime> [--all]"
  Short: "Stop a kernel"
  Long:  "Stop a kernel. The state entry is preserved (marked stopped)
          so the name remains resolvable. Use 'rat rm' to delete state.

          Examples:
            rat stop py
            rat stop --all"

rat add:
  Use:   "add <name> [dir] [--lang py] [--venv PATH]"
  Short: "Register a named runtime"

rat rm:
  Use:   "rm <name>"
  Short: "Delete a runtime's state"
  Long:  "Stop the kernel if running and remove the state entry entirely.
          Works on any runtime — custom or auto-generated.

          Examples:
            rat rm py-ml
            rat rm py@myproject"

rat serve:
  Use:   "serve <name> [--http] [--port PORT]"
  Short: "MCP server (for app builders)"

rat doctor:
  Use:   "doctor [<lang>]"
  Short: "Run diagnostics"
```

### Phase 4: Behavioral polish

#### 4a. `rat restart` auto-starts when nothing exists

After resolve(), if the kernel doesn't exist, start a fresh one
(spec: "same auto-start as `rat py`").

#### 4b. `rat stop --all --clean`

Add `--clean` flag that calls `store.Remove()` for each instead
of just marking stopped.

#### 4c. Side effects are noisy (behavioral rule #9)

Audit all auto-start paths. Every kernel start, dep install, or
config write prints what it's doing to stderr.

### Phase 5: Future languages (not blocking, parallel work)

#### 5a. R kernel + radian frontend
#### 5b. Julia kernel + ast_transforms frontend
#### 5c. Node.js kernel + repl module frontend
#### 5d. `rat install r|jl|js` + `rat doctor r|jl|js`

---

## Concentric circles (updated to match code + spec)

```
┌─────────────────────────────────────────────────────────────────┐
│ rat py  (REPL — the outermost ring)                             │
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │ resolve("py", cwd)  — ONE function, ALL commands           │  │
│  │  → "py@myproject" (from step 2: language alias expansion)  │  │
│  └──────────────────────────┬─────────────────────────────────┘  │
│                              │                                    │
│  detect runtime (A)          │                                    │
│  find venv (D)               │                                    │
│  install deps if missing (F) │ ← noisy: "Installing IPython..."  │
│                              │                                    │
│  ┌───────────────────────────┴──────────────────────────────┐    │
│  │ ensure_kernel (auto-start if needed, health-check if not) │    │
│  │  ┌─────────────────────────────────────────────────────┐  │    │
│  │  │ daemon.Start                                        │  │    │
│  │  │  ┌───────────────────────────────────────────────┐  │  │    │
│  │  │  │ rat serve  (MCP server + language subprocess)  │  │  │    │
│  │  │  └───────────────────────────────────────────────┘  │  │    │
│  │  │  + record state (H) + wait ready (I)                │  │    │
│  │  └─────────────────────────────────────────────────────┘  │    │
│  └───────────────────────────────────────────────────────────┘    │
│                                                                    │
│  launch REPL frontend (V)                                          │
│  (frontend does: run, look, cancel via MCP internally)             │
└────────────────────────────────────────────────────────────────────┘

┌───────────────────────────────────────────────────────┐
│ rat run py 'code'                                     │
│  resolve("py", cwd) → py@myproject                    │
│  ensure_kernel (auto-start if needed)                 │
│  connect (K) + execute (L) + display (W)              │
└───────────────────────────────────────────────────────┘

┌───────────────────────────────────────────────────────┐
│ rat look py [--at x]                                  │
│  resolve("py", cwd) → py@myproject                    │
│  ensure_kernel (auto-start if needed)                 │
│  connect (K) + look (M) + display (W)                 │
└───────────────────────────────────────────────────────┘

┌──────────────────────────────────────┐
│ rat cancel py                        │
│  resolve("py", cwd) → py@myproject   │
│  connect to RUNNING kernel only (K)  │
│  ctl cancel (N)                      │
│  (NO auto-start)                     │
└──────────────────────────────────────┘

┌──────────────────────────────────────┐
│ rat reset py                         │
│  resolve("py", cwd) → py@myproject   │
│  connect to RUNNING kernel only (K)  │
│  ctl reset (O)                       │
│  (NO auto-start)                     │
└──────────────────────────────────────┘

┌───────────────────────────────────────────────────────┐
│ rat restart py                                        │
│  resolve("py", cwd) → py@myproject                    │
│  if running: stop (P + mark stopped)                  │
│  ensure_kernel (always starts fresh)                  │
└───────────────────────────────────────────────────────┘

┌───────────────────────────────────────────────────────┐
│ rat install py                                        │
│  resolve("py", cwd) → py@myproject                    │
│  detect runtime (A)                                   │
│  find or CREATE venv (D + E)  ← unique to install     │
│  install deps (F)                                     │
│  ensure_kernel                                        │
│  smoke test: connect (K) + execute (L)                │
│  configure Claude/Cursor (U)  ← unique to install     │
└───────────────────────────────────────────────────────┘

┌───────────────────────────────────────────────────────┐
│ rat start <name>                                      │
│  resolve(name, cwd)                                   │
│  ensure_kernel                                        │
└───────────────────────────────────────────────────────┘

┌───────────────────────────────────────────────────────┐
│ rat stop <name> [--all]                               │
│  resolve(name, cwd)                                   │
│  kill process (P) + mark stopped (not delete)         │
└───────────────────────────────────────────────────────┘

┌────────────────────────────────┐  ┌──────────────────────────────┐
│ rat add <name> [dir]           │  │ rat rm <name>                │
│  find project (B)              │  │  resolve(name)               │
│  find venv (D)                 │  │  stop if running (P)         │
│  save runtime config (R)       │  │  hard-delete state entry (Q) │
└────────────────────────────────┘  └──────────────────────────────┘

┌─────────────────────┐  ┌─────────────────────────┐
│ rat status           │  │ rat doctor [lang]        │
│  read state (T)      │  │  detect runtime (A)      │
│  display (W)         │  │  check deps (D, F)       │
│                      │  │  check state (T)          │
│                      │  │  display (W)              │
└─────────────────────┘  └─────────────────────────┘
```

---

## Atoms (updated to match implementation)

```
ATOMS (building blocks):
  A. detect_runtime    — find Python/R/Julia/Node/bash binary + version
  B. find_project      — walk up for .git, pyproject.toml, Project.toml, etc.
  C. resolve           — THE function: input + cwd → (name, lang, cwd, venv, isNew)
                         combines: project detection, name expansion, venv detection
  D. find_venv         — find .venv in cwd → project root (called by resolve for py)
  E. create_venv       — uv venv or python -m venv (install only)
  F. install_deps      — pip install ipython jedi (install + first-run auto-install)
  G. spawn_serve       — rat serve as background process
  H. record_state      — write kernel to state.yaml
  I. wait_ready        — poll HTTP endpoint
  J. health_check      — MCP round-trip (ctl status)
  K. connect           — MCP initialize + session
  L. execute           — tools/call run
  M. look              — tools/call look
  N. ctl_cancel        — tools/call ctl cancel
  O. ctl_reset         — tools/call ctl reset
  P. kill_process      — SIGTERM/SIGKILL + mark stopped in state
  Q. remove_state      — hard-delete from state.yaml (rat rm only)
  R. save_runtime      — save named runtime config to state.yaml
  S. remove_runtime    — remove saved config
  T. read_state        — read state.yaml (running + stopped + dead PID cleanup)
  U. configure_clients — write Claude Desktop / Cursor config
  V. launch_repl       — start IPython / radian / julia / node repl / tmux attach
  W. display           — format + print to terminal

COMPOUND:
  ensure_kernel = J (health check) → if unhealthy: P + G + H + I
                  if not running: G + H + I
                  if healthy: noop
```
