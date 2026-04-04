> [!WARNING]
> In early development. README is the vision, not yet fully implemented. Subject to change. Use with caution.

# rat

**Run AnyThing.** One binary. Every REPL language. Every client shares one namespace.

```bash
curl -fsSL https://runanything.dev/install.sh | sh
rat install py
rat py
```

```python
In [1]: df = pd.read_csv("sales.csv")

In [2]: df.head()
Out[2]:
   region  quarter  revenue
0  East    Q1       42000
1  West    Q1       38000
```

Leave IPython open. Switch to Claude Desktop. "Analyze the dataframe I just loaded." Claude sees the same `df`. Same namespace. Same REPL.

---

## The idea

A REPL is a REPL. It shouldn't matter whether you're typing in a terminal, talking to Claude, writing in a notebook, or scripting in CI. One kernel, many windows.

```
┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐
│  Terminal   │ │   Claude    │ │   Cursor    │ │  Notebook   │
│  rat py     │ │   Desktop   │ │   agent     │ │  app        │
│  (IPython)  │ │             │ │             │ │             │
└──────┬──────┘ └──────┬──────┘ └──────┬──────┘ └──────┬──────┘
       │               │               │               │
       └───────────────┴───────┬───────┴───────────────┘
                               │
                    http://127.0.0.1:8717/mcp
                               │
                       rat serve py
                               │
                    Python kernel subprocess
                               │
                         one namespace
                      df, model, x = 42
```

Three tools. That's the entire kernel API:
- **`run`** — execute code or provide input
- **`look`** — inspect variables, complete code, see state
- **`ctl`** — reset, cancel, restart (MCP tool; from the CLI: `rat reset`, `rat cancel`, `rat restart`)

Same three tools for every language. Same three tools whether you're a human in a terminal, an LLM in Claude, or a notebook app rendering cells.

The protocol is MCP. Any MCP client can connect. The kernel is just an MCP server.

---

## Install

### macOS and Linux

```bash
curl -fsSL https://runanything.dev/install.sh | sh
```

Downloads the `rat` binary. That's it. One binary, no dependencies.

### Windows

```powershell
irm https://runanything.dev/install.ps1 | iex
```

### Non-technical users (Claude Desktop / Cursor)

Go to [runanything.dev](https://runanything.dev). Click "Download." Open the installer. It installs `rat`, installs Python if needed, configures Claude Desktop and Cursor automatically. Restart Claude. Done. No terminal required.

---

## Setup

```bash
rat install py
```

```text
Detected Python 3.12.1 (/usr/bin/python3)
Detected venv: ~/project/.venv
Installing IPython + jedi... done
Started py kernel on http://127.0.0.1:8717/mcp
Configured Claude Desktop
Configured Cursor

Ready.
```

If Python isn't installed:

```text
Python not found.

Install it:
  macOS:   brew install python3
  Ubuntu:  sudo apt install python3
  Windows: winget install Python.Python.3

Or: rat install py --with-python

Then retry: rat install py
```

Every language:

```bash
rat install py              # Python
rat install r               # R
rat install jl              # Julia
rat install sh              # Shell / Bash
rat install js              # JavaScript / Node

rat install py r jl sh      # multiple at once
```

Interactive wizard for first-timers:

```bash
rat setup
# What do you want to run?
#   [x] Python
#   [ ] R
#   [ ] Julia
#   [x] Shell
#   [ ] Node.js
#
# Configure for:
#   [x] Claude Desktop (detected)
#   [x] Cursor (detected)
#
# Done. Restart Claude and Cursor.
```

---

## Use

### Terminal — drop into IPython

```bash
rat py
```

```python
Python 3.12.1 | rat py kernel @ http://127.0.0.1:8717/mcp

In [1]: import pandas as pd

In [2]: df = pd.read_csv("sales.csv")

In [3]: df.groupby("region").revenue.sum()
Out[3]:
region
East    87000
West    38000
```

Full IPython. Syntax highlighting, tab completion, `?` help, `%magic` commands, history. But the namespace is shared — everything you do here is visible to Claude, Cursor, notebooks, anything connected to the same kernel.

### Terminal — one-liners

```bash
rat run python 'x = 42; print(x)'
# 42
# ✓ 3ms | 1 var

rat run python 'print(f"x is {x}")'
# x is 42
# ✓ 2ms | 1 var

rat run python 'look()'
# python idle | 2 vars | exec #2
#
# x   int        42
# df  DataFrame  (1000, 5)

rat run python 'look(at="df")'
# df: DataFrame (1000 rows × 5 columns)
#   = ...
#
#   ▸ columns  Index  ['region', 'quarter', 'revenue', ...]
#   ▸ dtypes   dict   {'region': 'object', 'revenue': 'int64', ...}
#     shape    tuple  (1000, 5)

rat reset python
# RESET | namespace cleared | 0 vars
```

### Claude Desktop / Cursor

They connect to the same kernel. No config beyond `rat install py`.

> **You:** Analyze the dataframe I just loaded
>
> **Claude:** *calls `look(at="df")`* → sees the same df
>
> *calls `run(code="df.groupby('region').revenue.sum()")`*
>
> East region has 2.3x the revenue of West.

Back in your terminal:

```python
In [4]: _   # Claude's result is here
Out[4]:
region
East    87000
West    38000
```

### Other languages

```bash
rat r 
# R 4.4.1 | rat r kernel @ http://127.0.0.1:8718/mcp
# >

rat jl
# Julia 1.11.0 | rat jl kernel @ http://127.0.0.1:8719/mcp
# julia>

rat sh
# bash 5.2.15 | rat sh kernel @ http://127.0.0.1:8720/mcp
# $

rat run r 'summary(mtcars)'
rat run jl 'using Statistics; mean([1,2,3])'
rat run sh 'ls -la'
```

Same pattern. Same three tools. Same shared namespace per kernel.

---

## Multiple runtimes

Default runtime uses auto-detected venv and cwd. Named runtimes let you have multiple environments:

```bash
rat add py-ml --venv ~/ml/.venv --cwd ~/ml
rat add py-web --venv ~/web/.venv --cwd ~/web

rat run py-ml 'import torch; print(torch.cuda.is_available())'
# True

rat run py-web 'import flask; print(flask.__version__)'
# 3.0.0

rat run py 'import pandas'
# default runtime, default venv — separate from py-ml and py-web
```

Each is a separate kernel, separate namespace, separate venv:

```text
py     → :8717  ~/project/.venv     one namespace
py-ml  → :8718  ~/ml/.venv          another namespace
py-web → :8719  ~/web/.venv         another namespace
```

```bash
# IPython into a named runtime
rat py-ml
```

```python
In [1]: torch.cuda.is_available()
Out[1]: True
```

---

## Lifecycle

Kernels auto-start on first use and stay warm. You never need to think about start/stop for normal use.

```bash
rat run py'x = 42'       # not running? starts automatically, then runs
rat py                      # not running? starts automatically, then REPL
rat look py           # not running? starts automatically, then inspects
```

When you need control:

```bash
# See everything
rat ls
# NAME     LANG  PORT   STATE    VENV                 CWD
# py       py    8717   running  ~/project/.venv      ~/project
# py-ml    py    8718   running  ~/ml/.venv           ~/ml
# r        r     8719   idle     —                    ~/project
# sh       sh    8720   running  —                    ~/project
# py-web   py    —      stopped  ~/web/.venv          ~/web

# Stop a runtime
rat stop py-ml

# Start explicitly
rat start py-ml

# Restart (same config, fresh namespace)
rat restart py

# Stop everything
rat stop --all

# Remove a named runtime
rat remove py-web

# Diagnostics
rat doctor
```

---

## Attach

There's no "attach" command. `rat py` always connects to the running kernel. If it's running, you're in. If it's not, it starts.

Terminal 1:
```bash
rat py
```
```python
In [1]: x = 42
```

Terminal 2:
```bash
rat py
# Attached to py @ http://127.0.0.1:8717/mcp
```
```python
In [1]: x
Out[1]: 42    # ← same namespace
```

Claude Desktop, Cursor, notebook apps — all attached automatically. There's nothing to do.

### Who's connected?

The MCP server tracks connected clients in memory. `rat ls` shows them:

```bash
rat ls
# NAME  LANG  PORT   STATE    CLIENTS                        CWD
# py    py    8717   running  terminal, claude-desktop (2)   ~/project
# sh    sh    8720   idle     —                              ~/project
```

Each client is identified by type (terminal, claude-desktop, cursor, mcp2cli, mcp2py, notebook), source host, connection time, and last activity. This helps decide whether to stop a kernel — if Claude is mid-execution, don't.

```bash
rat ls py --clients
# py @ http://127.0.0.1:8717/mcp | running | 2 clients
#
#   TYPE              HOST    CONNECTED  LAST CALL  CALLS
#   terminal          local   2m ago     5s ago     12
#   claude-desktop    local   10m ago    5s ago     3
```

Auto-idle: kernels with no client activity for a configurable duration can auto-stop to free resources.

---

## For app builders

Notebook apps, IDEs, tools — anyone who wants a kernel. You don't need the `rat` CLI. You just need the MCP server.

### Start a kernel

```bash
rat serve py --http --port 8717 --cwd /user/project --venv /user/project/.venv
rat serve r --http --port 8718 --cwd /user/project
```

### Connect from your app

```javascript
const py = new McpClient("http://localhost:8717/mcp");

// Run code
await py.callTool("run", { code: "df = pd.read_csv('data.csv')" });

// Variable explorer
await py.callTool("look");
// → { variables: [{ name: "df", type: "DataFrame", value: "..." }] }

// Drill into a variable
await py.callTool("look", { at: "df.columns" });

// Autocomplete
await py.callTool("look", { code: "df.des", cursor: 6 });
// → [{ label: "describe", kind: "function" }]

// Reset
await py.callTool("ctl", { op: "reset" });
```

### Or via mcp2r (R app)

```r
library(mcp2r)
py <- mcp_connect("http://localhost:8717/mcp")
py$run(code = "x = 42")
py$look(at = "x")
```

### Or via mcp2py (Python app)

```python
from mcp2py import connect
py = connect("http://localhost:8717/mcp")
py.run(code="x = 42")
py.look(at="x")
```

### Kernel picker for multi-language notebooks

```javascript
const py = spawn("rat", ["serve", "py", "--http", "--port", "0"]);
const r  = spawn("rat", ["serve", "r",  "--http", "--port", "0"]);

function runCell(cell) {
  const kernel = cell.language === "python" ? pyClient : rClient;
  return kernel.callTool("run", { code: cell.code });
}
```

### Claude Desktop / Cursor config

```json
{
  "mcpServers": {
    "python": { "url": "http://127.0.0.1:8717/mcp" },
    "r":      { "url": "http://127.0.0.1:8718/mcp" }
  }
}
```

`rat setup` writes this automatically.

---

## Architecture

One Go binary. Kernel scripts embedded for each language.

```
rat (Go binary)
├── CLI          install, add, remove, ls, start, stop, restart, doctor
├── REPL         rat py → launches IPython connected to shared kernel via MCP
├── MCP server   rat serve py → shared server, HTTP or stdio
└── Kernels      thin scripts in each language, embedded via go:embed
     ├── python/kernel.py    runs IPython, speaks JSON protocol
     ├── r/kernel.R          runs R session, speaks JSON protocol
     ├── julia/kernel.jl     runs Julia session, speaks JSON protocol
     ├── node/kernel.js      runs Node.js REPL, speaks JSON protocol
     └── bash                direct PTY (no script needed)
```

The MCP server is Go — shared code for all languages. The kernel is a subprocess in the target language — runs in the user's interpreter, has access to their packages. Communication is JSON over pipes.

```
rat serve py
│
├── Go: MCP server (HTTP on :8717)
│   ├── tool: run   → send execute command to kernel
│   ├── tool: look  → send inspect/complete command to kernel
│   └── tool: ctl   → send reset/cancel command to kernel
│
└── subprocess: /home/user/.venv/bin/python kernel.py
    ├── IPython execution engine
    ├── variable inspection
    ├── jedi completions
    └── one namespace (shared by all MCP clients)
```

### What `rat install py` does

1. Find Python (or install it with `--with-python`)
2. Find or create a venv
3. Install IPython + jedi into the venv
4. Start the kernel: `rat serve py --http --port 8717` (background)
5. Configure Claude Desktop and Cursor (write their config files)

No pip install of rat itself. The kernel script is embedded in the Go binary. The only Python dependencies are IPython and jedi, which go in the user's venv.

### What `rat py run 'x = 42'` does

1. Is there a `py` kernel running? Check `~/.config/rat/state.yaml`
2. No → start one (discover venv/cwd, `rat serve py --http`, background)
3. Yes → call the MCP server: `POST http://127.0.0.1:8717/mcp` → `run(code="x = 42")`
4. Print the result

### What `rat py` (REPL) does

1. Ensure kernel is running (same as above)
2. Find the venv's Python interpreter
3. Run embedded `ipython_frontend.py` with that interpreter
4. The script starts IPython with a custom execution backend
5. Every cell → POST to `http://127.0.0.1:8717/mcp` → `run(code=...)`
6. Completions → POST → `look(code=..., cursor=...)`
7. Full IPython UX, shared namespace

---

## Command reference

```bash
# Setup (once)
rat install <lang>              # install runtime: py, r, jl, sh, js
rat install <lang> --with-<lang>  # also install the language itself
rat setup                       # interactive wizard
rat add <name> [--venv] [--cwd] # register a named runtime
rat remove <name> [--all]       # unregister

# Daily use
rat <name>                      # REPL — auto-starts kernel
rat run <name> '<code>'         # run code — auto-starts kernel
rat look <name> [--at <sym>]    # inspect — auto-starts kernel
rat reset <name>                # clear namespace
rat cancel <name>               # cancel running execution

# Manage
rat ls                          # list all runtimes and their state
rat start <name>                # start a kernel explicitly
rat stop <name>                 # stop a kernel
rat restart <name>              # restart (fresh namespace, same config)
rat stop --all                  # stop everything
rat doctor                      # full diagnostics
rat update                      # update rat binary + kernel deps
rat version                     # version info for everything

# Server (for Claude Desktop, Cursor, notebook apps, other MCP clients)
rat serve <name>                # MCP stdio server (default)
rat serve <name> --http [--port PORT]  # MCP HTTP server (shared access)
```

---

## Supported languages

| Lang | REPL         | `rat install` installs            | Kernel           |
|------|--------------|-----------------------------------|------------------|
| `py` | IPython      | ipython, jedi into venv           | kernel.py        |
| `r`  | R console    | R kernel packages                 | kernel.R         |
| `jl` | Julia REPL   | Julia kernel packages             | kernel.jl        |
| `sh` | bash         | nothing (built-in)                | direct PTY       |
| `js` | Node.js REPL | nothing (uses system Node)        | kernel.js        |

---

## The vision

```
Any language runtime
    ↓ wrap in MCP (rat serve py, rat serve r, ...)
MCP Server (3 tools: run, look, ctl)
    ↓ rat CLI wraps these ergonomically
    ↓ mcp2cli gives generic CLI access to any MCP tool
    ↓ connect from anywhere
├── rat py              → IPython REPL in your terminal
├── rat run py          → one-liners from shell scripts
├── Claude Desktop      → LLM runs code in your environment
├── Cursor              → coding agent with live runtime
├── notebook app        → cells, variable explorer, completions
├── mcp2py              → Python library
├── mcp2r               → R library
└── any MCP client      → the protocol is the API
```

Write the kernel once. Connect from everywhere. One namespace. Run AnyThing.
