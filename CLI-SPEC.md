# rat CLI specification

## Guiding principle

**`rat py` just works.** First time and hundredth time.
No manual. No thinking. Just delight.

### Who this is for

rat's value is the **shared namespace**: your terminal, Claude,
Cursor, and notebook apps all see the same variables in the same
Python session. If you already use `ipython` alone and never talk
to an LLM about your data, rat adds little. The magic happens
when you load a dataframe in your terminal and Claude can see it,
or when Cursor runs code in the same session you're debugging.

---

## Core concepts

### How naming works

When you type a language — `py`, `sh`, `r`, `jl`, `js` — rat
finds or creates the right kernel for your current project.
The kernel's full name includes your project:
`py@amrmdtest`, `sh@myapp`.

When you type a full name — `py@amrmdtest`, `py-ml` — rat uses
it directly.

You never need to type the full name unless you have multiple
projects and want to reach across them.

```
rat py                →  finds (or creates) py@amrmdtest
rat run py 'x=1'      →  same kernel
rat run py-ml 'x=1'   →  uses py-ml directly
rat status            →  shows py@amrmdtest, py-ml, etc.
```

This works like autocomplete: `py` expands to the right
`py@<project>` for where you are. Full names pass through
unchanged.

### The `@` suffix

Every kernel that rat creates from a language shorthand gets
a name of the form `<lang>@<project>`. The `@project` part
is determined by where you are when the kernel is created.

`py@amrmdtest`, `py@scratch`, `py@home` — each is a separate
Python world with its own namespace, venv, and working
directory.

### Resolution

Every command uses the same `resolve()` function. The key
design: language aliases are resolved early (step 2), before
prefix matching (step 3), so that `rat py` from `~/` always
works even when `py@api` and `py@web` are running.

```
resolve(input, cwd):

  1. Exact match
     Running kernel or saved runtime with this exact name?
     → use it.
     
     "py@amrmdtest" → py@amrmdtest
     "py-ml"        → py-ml

  2. Language alias (py, sh, r, jl, js, python, bash, etc.)
     Compute the canonical name for this cwd: lang@projectname.
     
     a. That name exists (running or saved)? → use it.
     b. Doesn't exist? → return it as new.
        Caller decides whether to create/start.

     "py" from ~/Projects/amrmdtest
       → canonical: py@amrmdtest
       → exists? yes → use it

     "py" from ~/ (py@home not running)
       → canonical: py@home
       → exists? no → return py@home (new, caller creates)

     "py" from ~/ (py@home already running)
       → canonical: py@home
       → exists? yes → use it

     This means `rat py` ALWAYS resolves to a single kernel
     for this directory, whether it exists yet or not. No
     ambiguity, no "multiple matches" error. Languages always
     know exactly where they belong.

  3. Prefix match
     Collect all running kernels + saved runtimes whose name
     starts with input. This step handles partial names like
     "py-", "sh@", "py@am" — things that aren't language
     aliases but could match existing kernels.
     
     a. 1 match  → use it (autocompleted)
     b. N matches → show list, ask user to be specific

     "py-" from anywhere
       → matches: py-ml (1 match) → use it

     "py@" from anywhere
       → matches: py@amrmdtest, py@scratch
       → ambiguous:
         "Multiple runtimes match 'py@':
            py@amrmdtest   running  ~/Projects/amrmdtest
            py@scratch     running  ~/Projects/scratch
          Use the full name, e.g.: rat run py@amrmdtest '...'"

  4. Error
     "No runtime matching 'xyz'. Use a language (py, sh, r, jl, js)
      or see 'rat status' for running kernels."
```

Shell tab-completion uses the same candidates as steps 1–3.

### Project name cascade

When creating a new kernel via language expansion (step 2b), rat
determines the `@` suffix:

```
1. Directory basename (the default, covers 99% of cases)
   Use the name of the project root folder.
   If no project root found, use the cwd folder name.
   Home directory (~/) becomes "home".
   
   ~/Projects/amrmdtest  →  py@amrmdtest
   ~/ml                  →  py@ml
   ~/                    →  py@home

2. Collision tiebreaker
   If the name already exists in state for a different path,
   prepend the parent folder:
     py@backend (exists for ~/Work/startup/backend)
     py@sidehustle-backend (new, for ~/Work/sidehustle/backend)
```

Because stopped runtimes persist in state, the collision
tiebreaker is stable across reboots. The first project to
claim a name keeps it regardless of which kernels are running.

Simple, predictable, and what the user already calls the project
in their head. No metadata parsing, no git inspection — just the
folder name.

### Project root detection

Walk from cwd upward. The first directory containing any of these
is the project root:

```
.git  pyproject.toml  setup.py  setup.cfg  requirements.txt
Pipfile  tox.ini  DESCRIPTION  renv.lock  Project.toml
JuliaProject.toml  package.json  deno.json  Cargo.toml  go.mod
Gemfile  composer.json  *.sln  *.csproj  pom.xml  build.gradle
stack.yaml  mix.exs  pubspec.yaml  Package.swift  build.zig
CMakeLists.txt  meson.build  Makefile  .editorconfig
```

If none is found before reaching the filesystem root, there is no
project. The cwd itself is used, and the folder name (or "home"
for `~/`) becomes the suffix.

### Runtime and environment detection

Each language has its own detection cascade. The pattern is
the same: explicit override → environment/tooling → project-local
detection → system fallback.

#### Python

rat looks for an active Python environment in this order:

```
1. Explicit override
   RAT_PYTHON env var → use that interpreter directly.

2. Active environment (set by the user's shell/tooling)
   VIRTUAL_ENV env var  → use $VIRTUAL_ENV/bin/python
   CONDA_PREFIX env var → use $CONDA_PREFIX/bin/python
   
   These catch poetry, conda, mise, pixi, and any tool that
   activates an environment by setting these variables.

3. Directory-local venv
   Search for .venv/ or venv/ containing a Python binary:
     a. In cwd
     b. In ancestors up to (and including) the project root
   Closest wins. Never walk past the project root.
   If no project root, only check cwd.

4. System Python
   python3, python, py -3 (in PATH order).
```

This order means:
- If you `conda activate myenv && rat py`, it uses your conda env.
- If you have a `.venv/` in your project, it uses that.
- If neither, it falls back to system Python.
- You can always override with `RAT_PYTHON=/path/to/python rat py`.

#### R

rat looks for an R runtime in this order:

```
1. Explicit override
   RAT_R env var → use that R binary directly.

2. System R
   R, Rscript (in PATH order).
```

For package libraries, rat respects the R session's `.libPaths()`.
If `renv.lock` exists in the project root, rat activates renv so
the session uses the project-local library.

**REPL strategy:** R's built-in console is minimal. rat uses
`radian` — a Python-based R console (built on `rchitect` +
`prompt_toolkit`) with syntax highlighting, multiline editing,
bracket matching, and tab completion. `rat install r` installs
radian into a managed venv (separate from the user's Python).
Execution is intercepted via radian's eval hook and routed to
the shared kernel via MCP.

**Fallback:** If radian is not available, use R's built-in
readline console with a custom eval wrapper that routes through
MCP. Less pretty, but functional.

**When R is not found:**
```
R not found. Install it:
  brew install r                (macOS)
  sudo apt install r-base      (Ubuntu)
  https://cran.r-project.org
```

**Introspection (`rat look r`):**
Lists objects with `ls()`. Inspects with `str()`, `class()`,
`dim()`. Data frames show dimensions, column names, and types.

#### Julia

rat looks for a Julia runtime in this order:

```
1. Explicit override
   RAT_JULIA env var → use that Julia binary directly.

2. JULIA_PROJECT env var
   If set, start Julia with --project=$JULIA_PROJECT.

3. Directory-local project
   Search for Project.toml in:
     a. cwd
     b. Ancestors up to (and including) the project root
   Closest wins. Never walk past the project root.
   If found, start Julia with --project=<dir>.

4. System Julia
   julia (in PATH). juliaup handles version resolution
   transparently if installed.
```

**REPL strategy:** Julia's built-in REPL is excellent — modes
(help `?`, shell `;`, package `]`), Unicode completion
(`\alpha<tab>` → α), syntax highlighting, contextual help.
rat uses it directly. Execution is intercepted via Julia's
`ast_transforms` hook: every expression is captured before
evaluation and routed to the shared kernel via MCP. The REPL
is 100% native Julia — only eval is redirected.

No packages are auto-installed.

**When Julia is not found:**
```
Julia not found. Install it:
  curl -fsSL https://install.julialang.org | sh
  brew install julia            (macOS)
  https://julialang.org/downloads
```

**Introspection (`rat look jl`):**
Lists variables with `varinfo()`. Inspects with `typeof()`,
`fieldnames()`, `size()`. Arrays and DataFrames show shape
and element/column types.

#### Node.js

rat looks for a Node.js runtime in this order:

```
1. Explicit override
   RAT_NODE env var → use that Node binary directly.

2. Version manager
   If .nvmrc or .node-version exists in the project root,
   and nvm/fnm is available, use the specified version.

3. System Node
   node (in PATH).
```

Node.js uses per-project `node_modules/` — there is no virtual
environment to detect. rat doesn't manage packages; `require()`
and `import` resolve from the project's `node_modules/` as usual.

**REPL strategy:** Node's built-in `repl` module is used. rat
overrides the `eval` function to route execution to the shared
kernel via MCP, and the `completer` function to get completions
from the live namespace. Everything else is native Node REPL:
readline, history, `.editor` mode, `.break`, `.clear`.

No packages are auto-installed.

**When Node is not found:**
```
Node.js not found. Install it:
  brew install node             (macOS)
  sudo apt install nodejs       (Ubuntu)
  https://nodejs.org

Or: nvm install --lts
```

**Introspection (`rat look js`):**
Lists user-defined globals. Inspects with `typeof`,
`Object.keys()`, `Array.length`. Objects show their enumerable
properties and types.

#### Shell (bash, zsh)

```
1. Explicit override
   RAT_SHELL env var → use that shell binary directly.

2. User's shell
   $SHELL env var.

3. System default
   /bin/bash, /bin/sh (in order).
```

Shell kernels have no package environment. The working directory
is set to the project root.

**REPL strategy:** No REPL reimplementation. The kernel runs
bash in a PTY (via Go's `creack/pty`). `rat sh` attaches your
terminal to that PTY — same approach as tmux/screen. It's the
real shell: `PS1` prompt, readline, `.bashrc`, tab completion,
job control, everything.

MCP clients coexist with the PTY: `run` calls inject code and
capture output between markers. Interactive use and MCP calls
are serialized.

Nothing to install. Shell is always available.

**Introspection (`rat look sh`):**
Lists exported variables with `env`. Shows functions with
`declare -F`. Inspects commands with `type`. Shows variable
values with `echo $VAR`.

---

## Commands

### Tier 1 — daily use

These are the product. A user who only knows these six commands
can do everything.

#### `rat <lang>`

**JTBD:** "I want an interactive session."

```
rat py
rat sh
rat r
rat jl
rat js
```

This is the hero command. It is the entire onboarding AND the
daily driver.

**First run behavior (all languages):**
1. Detect language runtime (see each language's detection cascade)
2. Resolve name (`py` → `py@amrmdtest`)
3. Detect environment (venv, renv, Project.toml, etc. — never create)
4. Auto-install REPL deps if needed (Python only — see below)
5. Start kernel in background
6. Launch the language's native REPL with execution redirected
   to the shared kernel via MCP (see REPL strategy per language)

Subsequent runs skip detection: resolve → find kernel → connect.

The key principle: **use the real REPL, intercept execution.**
`rat py` IS IPython. `rat jl` IS the Julia REPL. `rat sh` IS
bash in a PTY. Only the pipe between "user hits Enter" and
"code executes" is redirected to the shared MCP kernel.

**Python-specific bootstrapping:**
Python is the only language that may need REPL dependencies
installed. Check if IPython/jedi importable; if not:
- Into venv if one exists → `pip install ipython jedi`
- No venv? Use `uvx ipython` (ephemeral, no system mutation)
- No uv? Degrade gracefully to basic Python REPL without
  IPython. Print: "For a better REPL: uv venv && rat py"
- Never `pip install --user` (breaks PEP 668 / externally
  managed environments on modern Linux/macOS)
- Noisy: "Installing IPython + jedi into .venv... done"

R, Julia, Node, and Shell have built-in REPLs that need
no extra packages.

**When no venv is found (Python):**
```
rat py | py@amrmdtest | system python (no venv)
Tip: create a project venv with: uv venv
```
One-line hint, not a blocker. Detect `uv` vs `python3 -m venv`
and show the right command.

**When the language runtime is not found:**
Each language prints platform-specific install instructions.
See the runtime detection sections under Core concepts.

**Args:** none.

---

#### `rat run <runtime> '<code>'`

**JTBD:** "Run this code quick."

```
rat run py 'x = 42'
rat run py 'print(x)'
rat run sh 'ls -la'
rat run py@amrmdtest 'df.head()'
rat run py-ml 'import torch'
```

Resolves the runtime, auto-starts if needed (same as `rat py`
minus the REPL), executes, prints output, exits.

**Args:** runtime (resolved), code string.

---

#### `rat look <runtime> [--at <symbol>]`

**JTBD:** "What's in my session?"

```
rat look py                 # variable overview
rat look py --at df         # inspect df in detail
rat look py --at df.columns # drill into attribute
```

Resolves, auto-starts if needed, inspects, prints.

**Args:** runtime (resolved), optional `--at`.

---

#### `rat cancel <runtime>`

**JTBD:** "It's stuck, stop it."

```
rat cancel py
```

Resolves, connects (does NOT auto-start — if it's not running,
there's nothing to cancel), sends interrupt.

**Args:** runtime (resolved).

---

#### `rat restart <runtime>`

**JTBD:** "Start over, fresh."

```
rat restart py
rat restart py@amrmdtest
```

Kills the kernel process and starts a new one. Fresh namespace,
fresh language subprocess. More reliable than clearing the
namespace in-process.

Resolves the name. If multiple match and ambiguous, shows the list
(same as any other command). If no kernel is running, starts a
fresh one (same auto-start as `rat py`).

**Args:** runtime (resolved).

---

#### `rat status`

**JTBD:** "What's running?"

```
rat status
```

```
  NAME              STATUS    CWD                        VENV
  py@amrmdtest      running   ~/Projects/amrmdtest       .venv
  py@scratch        idle 28m  ~/Projects/scratch         .venv
  sh@amrmdtest      running   ~/Projects/amrmdtest       —
  py-ml             stopped   ~/ml                       .venv
```

Shows all known runtimes: running, idle, and stopped. Friendly,
human-readable. Replaces `rat ls`.

**Args:** none.

---

### Tier 2 — setup and management

These commands exist for explicit environment management and
power-user scenarios. They appear below the tier 1 commands
in `--help`, separated by a header.

#### `rat install <lang> [<lang>...]`

**JTBD:** "Set up this project properly."

```
rat install py
rat install py r sh
```

The full ceremony (varies by language):
1. Detect runtime
2. Resolve name (project-aware)
3. Set up environment:
   - **Python:** find or **create** venv (unlike `rat py`
     which never creates), install IPython + jedi
   - **R:** install radian (into a rat-managed venv, separate
     from the user's Python), activate renv if `renv.lock` exists
   - **Julia:** `Pkg.instantiate()` if Project.toml exists
   - **JS:** `npm install` if `package.json` exists and
     `node_modules/` is missing
   - **Shell:** nothing
4. Start kernel
5. Smoke test
6. Configure Claude Desktop and Cursor

This is for when you want the complete setup: environment, deps,
and client configuration. `rat py` gets you in fast;
`rat install py` sets you up properly.

**Args:** one or more language names.

---

#### `rat doctor [<lang>]`

**JTBD:** "Something's broken, help me fix it."

```
rat doctor
rat doctor py
```

Checks (language-aware):
- Language runtime found? Version?
- Environment tooling available?
  (Python: uv/pip; R: radian/renv; Julia: Pkg; JS: npm; Shell: n/a)
- Environment detected?
  (Python: venv; R: renv library; Julia: Project.toml; JS: node_modules)
- REPL dependencies installed? (Python: IPython/jedi)
- Config directories writable?
- Running kernels healthy?

Prints actionable fix instructions for each problem.

**Args:** optional language to scope diagnostics.

---

#### `rat start <name>`

**JTBD:** "Start this kernel."

```
rat start py              # resolves: py@amrmdtest
rat start py@amrmdtest    # explicit
rat start py-ml           # named runtime
```

Resolves the name (same as everything else), starts the kernel
in the background. If already running and healthy, reports it.
If running but unhealthy (wedged, stopped), kills and restarts.

**Args:** runtime (resolved).

---

#### `rat stop <name> [--all]`

**JTBD:** "Stop this kernel."

```
rat stop py               # resolves: py@amrmdtest
rat stop py@amrmdtest     # explicit
rat stop --all            # stop everything
```

Resolves, sends SIGTERM, marks the runtime as stopped. The state
entry is preserved so the runtime remains visible in `rat status`
and resolvable by name from any directory.

**Args:** runtime (resolved), or `--all`.

---

#### `rat add <name> [dir]`

**JTBD:** "Register a named runtime with custom config."

```
rat add py-ml ~/ml
rat add py-web --venv ~/web/.venv --cwd ~/web
rat add r-stats --lang r --cwd ~/stats
```

Saves a named runtime configuration. Language is inferred from
the name prefix (`py-ml` → `py`) or set with `--lang`.

The second positional arg is a shorthand for `--cwd`. If a Python
venv is found in the directory, it's auto-detected.

**Args:** name, optional dir, optional `--venv`, `--cwd`, `--lang`.

---

#### `rat rm <name>`

**JTBD:** "Delete a runtime's state."

```
rat rm py-ml
rat rm py@amrmdtest
```

Stops the kernel if running and removes the state entry entirely.
Works on any runtime — custom (`py-ml`) or auto-generated
(`py@amrmdtest`). Asks for confirmation.

This is the only way to fully erase a runtime from state.
`rat stop` marks it stopped; `rat rm` deletes it.

**Args:** name (exact or resolved).

---

#### `rat serve <name> [--http] [--port PORT]`

**JTBD:** "I'm building a notebook/IDE, give me an MCP server."

```
rat serve py --http --port 8717    # HTTP server
rat serve py                       # stdio server (for Claude Desktop config)
```

Runs an MCP server in the foreground. This is the atomic building
block — every kernel IS a `rat serve` process.

Not typically used by end users. Used by `daemon.Start` internally,
and by app builders who want direct MCP access.

**Args:** name, `--http`, `--port`, `--cwd`, `--venv`, `--lang`.

---

#### `rat reset <name>`

**JTBD:** "Clear namespace without restarting the process."

```
rat reset py
```

Clears the language namespace in-process (Python: `globals()`,
R: `rm(list=ls())`, Julia: workspace, JS: global scope, Shell:
unset variables). Faster than restart but less reliable (some
state may leak). Kept for power users who want a quick clear.
Most users should use `rat restart`.

Requires a running kernel. Does NOT auto-start — if it's not
running, there's nothing to reset.

**Args:** runtime (resolved).

---

## Help output

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

---

## Behavioral rules

### 1. Languages expand, names don't
When you type `py`, rat expands it to the right `py@<project>`
for where you are. When you type `py@amrmdtest` or `py-ml`,
rat uses it as-is. Every command works the same way.

### 2. All commands use the same resolve()
No command is "smart" and another "literal." The resolution
algorithm is one function, called everywhere.

### 3. Never create an environment unless explicitly asked
`rat <lang>` detects and uses existing environments (venv, renv,
Project.toml, node_modules) or the system runtime.
`rat install <lang>` creates or initializes them if missing.
The hint to set one up is shown but never forced.

### 4. Respect the user's environment
Each language has its own detection cascade (see Runtime and
environment detection). The pattern is the same: explicit
override → active environment/tooling → project-local detection
→ system fallback. The user's activated environment always wins.
Never walk past the project root for project-local detection.

### 5. Auto-install deps, be noisy about it
If IPython is missing, install it. But print what you're doing:
```
Installing IPython + jedi into .venv... done
```

### 6. Auto-start kernels on first use
`rat py`, `rat run py`, `rat look py`, `rat restart py` — all
auto-start if no kernel is running. The user never needs to think
about start/stop for normal use.

`rat cancel` and `rat reset` do NOT auto-start — they operate
on running kernels only. If nothing is running, they report it
and exit.

### 7. Health-check before reusing a kernel
When finding an existing kernel, don't just check PID liveness.
Do an MCP round-trip (`ctl status`). A stopped (Ctrl-Z) or wedged
kernel has a live PID but can't serve requests. Kill and restart
if unhealthy.

### 8. Ambiguity is surfaced, never silently resolved
Language shorthands (`py`, `sh`) always resolve unambiguously
to the kernel for your current project. But partial names
(`py-`, `py@`) may match multiple kernels — when they do,
rat shows the list and asks you to be specific. Never guesses.

### 9. Side effects are noisy
Every action that changes the system — installing packages,
creating kernels, writing config files — prints what it's doing.
The user should never wonder "what just happened?"

### 10. Never mutate system Python
No `pip install --user`. Modern systems (Ubuntu 23.04+, macOS
Homebrew) enforce PEP 668 and will reject it. Install deps into
a venv, use `uvx` for ephemeral tools, or degrade gracefully.

### 11. Cancel and restart are separate
`cancel` = interrupt the current execution (Ctrl-C equivalent).
`restart` = kill the process, start fresh (Ctrl-D + re-enter).
Different jobs, different commands.

---

## Kernel lifecycle

Kernels auto-start on first use and stay warm. But they must
not accumulate silently and eat resources.

### Auto-idle shutdown

Kernels track client activity (last MCP request timestamp).
After a configurable idle period with no connected clients,
the kernel shuts itself down. The state entry is preserved
(marked stopped) so that `resolve()` can still find it by name
from any directory, and the collision tiebreaker remains stable.

```
Default idle timeout:  24h (no activity, no clients)
Configurable:          RAT_IDLE_TIMEOUT=1h  (env var)
                       rat.idleTimeout in config file
```

`rat status` shows idle time for running kernels:
```
  NAME              STATUS    CWD                        VENV
  py@amrmdtest      running   ~/Projects/amrmdtest       .venv
  py@scratch        idle 28m  ~/Projects/scratch         .venv
  sh@amrmdtest      running   ~/Projects/amrmdtest       —
  py-ml             stopped   ~/ml                       .venv
```

### Connected clients keep kernels alive

A kernel stays alive as long as any client is connected:
terminal REPL, Claude Desktop, Cursor, notebook app.
The idle timer only starts when the last client disconnects.

### Startup cost is low

Starting a Python kernel takes ~1-2 seconds (subprocess spawn +
IPython init). Users shouldn't fear auto-shutdown — the next
`rat py` or `rat run py` restarts it transparently.

### Explicit control

```
rat stop py              # stop now (state preserved)
rat stop --all           # stop everything (state preserved)
rat restart py           # kill and restart
rat rm py@scratch        # delete state entry entirely
```

These override auto-idle — you don't need to wait.

### State cleanup

Because stopped runtimes persist in state, the state file can
grow over time. Cleanup is explicit and automatic:

- `rat rm <name>` deletes a single entry.
- `rat stop --all --clean` stops everything and removes all
  state entries.
- Automatic pruning: stopped runtimes untouched for 30 days
  are removed from state on the next `rat` invocation.
  Configurable: `RAT_GC_DAYS=30` or `rat.gcDays` in config.

### Transparency

All state is in one file: `~/.config/rat/state.yaml`.
It is human-readable YAML. `rat status` shows everything.
Logs are in `~/.config/rat/logs/<name>.log`.

There is no hidden magic. Every background process is a
`rat serve` invocation visible in `ps`. Every state change
is written to the state file. If something goes wrong,
`rat doctor` tells you what and how to fix it.

### Resource budget

Each kernel is one Go process (~10MB) + one language subprocess
(Python: ~50MB base, more with loaded packages). Ten kernels ≈
600MB. Auto-idle keeps this bounded for developers who work
across many projects.

---

## Language aliases

These all resolve to their canonical form before entering
the resolve algorithm:

```
py, python             →  py
r                      →  r
jl, ju, julia          →  jl
sh, bash               →  sh
js, javascript, node   →  js
```

These must match `langAliases` in lang.go. Names that aren't
language aliases (like `py-ml`, `py@project`) skip step 2 and
go straight to exact/prefix matching.

---

## MCP tools (the kernel API)

Every kernel exposes exactly three MCP tools:

- **`run`** — execute code or provide input
- **`look`** — inspect variables, complete code, see state
- **`ctl`** — reset, cancel, restart, status

Same three tools for every language. Same three tools whether
the caller is a human in a terminal, an LLM in Claude, or a
notebook app rendering cells.

---

## Architecture layers

```
Layer 1: rat serve          The kernel (MCP server + language subprocess)
Layer 2: daemon.Start       Background process management + state tracking
Layer 3: resolve()          Name resolution (autocomplete semantics)
```

Every command is: `resolve()` → `ensure_kernel()` (maybe) → `do_thing()`.

```
         resolve("py", cwd)
              │
              ▼
        ┌─────────────┐
        │ py@amrmdtest │
        └──────┬──────┘
               │
        ensure_kernel()
        (start if needed,
         health check if exists)
               │
               ▼
          rat serve
          (MCP server +
           Python subprocess)
```
