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
```

Leave IPython open. Open Claude Desktop. "Analyze the dataframe I just loaded." Claude sees the same `df` — same namespace, same kernel.

---

## How it works

rat wraps language runtimes as MCP servers. One kernel, many clients.

```
┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐
│ Terminal  │ │  Claude  │ │  VS Code │ │  Cursor  │
│  rat py   │ │ Desktop  │ │  ext     │ │  agent   │
└─────┬────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘
      └───────────┴──────┬─────┴────────────┘
                         │
              http://127.0.0.1:8717/mcp
                         │
                   rat serve py
                         │
              Python kernel subprocess
                         │
                    one namespace
```

Three MCP tools. That's the entire kernel API:

| Tool | What it does |
|------|-------------|
| `run` | Execute code or provide input |
| `look` | Inspect variables, complete code, see state |
| `ctl` | Reset, cancel, restart |

Same three tools for every language. Same three tools whether you're a human in a terminal, an LLM, or a notebook app.

---

## Install

### macOS and Linux

```bash
curl -fsSL https://runanything.dev/install.sh | sh
```

### Set up a language

```bash
rat install py    # Python — detects venv, installs IPython + jedi
rat install sh    # Shell — no deps
rat install r     # R — installs jsonlite
rat install pi    # pi agent — checks tmux + pi
```

Multiple at once:

```bash
rat install py r sh
```

Interactive wizard:

```bash
rat setup
```

---

## Daily use

### REPL

```bash
rat py
```

Full IPython. Syntax highlighting, tab completion, `?` help, `%magic`. The namespace is shared — everything you do is visible to connected clients.

```bash
rat r       # R console
rat sh      # bash
rat pi      # pi coding agent
```

### One-liners

```bash
rat run py 'x = 42; print(x)'
# 42

rat run sh 'ls -la'
rat run r 'summary(mtcars)'
```

### Inspect

```bash
rat look py              # variable overview
rat look py --at df      # inspect df in detail
```

### History

```bash
rat tail py              # recent activity
rat tail py --n 20       # last 20 entries
```

### Control

```bash
rat cancel py            # interrupt execution (Ctrl-C equivalent)
rat reset py             # clear namespace, keep process
rat restart py           # kill + fresh start
```

---

## Project-scoped kernels

Kernels are scoped to your project directory. `rat py` in `~/Projects/foo` creates `py@foo`, separate from `py@bar` in another project.

```bash
rat status
# NAME             STATUS   CWD                   VENV
# py@myproject     running  ~/Projects/myproject   .venv
# py@other         stopped  ~/Projects/other       .venv
# r@myproject      running  ~/Projects/myproject   —
```

Verbose:

```bash
rat status -v
# py@myproject  running
#   Python 3.12.1 · 158MB · idle 1m · PID 316257
#   http://127.0.0.1:8717/mcp
#   ~/Projects/myproject · .venv
#   Clients: rat, rat-vscode (6)
```

### Named runtimes

For multiple environments in the same language:

```bash
rat add py-ml ~/ml                  # auto-detects venv in ~/ml
rat add py-web --venv ~/web/.venv --cwd ~/web
rat add r-stats --lang r --cwd ~/stats
```

Point at a specific binary:

```bash
rat add py-311 --runtime /opt/python3.11/bin/python3
```

Configure pi options:

```bash
rat add pi-sonnet --lang pi --opt model=claude-sonnet-4-5 --opt thinking=high
```

### Kernel picker

```bash
rat pick    # interactive picker — arrow keys, Enter to connect
```

Also appears on `Ctrl-D` from any REPL.

---

## Lifecycle

Kernels auto-start on first use. No manual start/stop needed for normal work.

```bash
rat run py 'x = 42'     # not running? starts automatically
rat py                   # not running? starts automatically
rat look py              # not running? starts automatically
```

When you need control:

```bash
rat start py             # start explicitly
rat stop py              # stop (preserves state entry)
rat stop --all           # stop everything
rat remove py-ml         # delete state entry entirely
rat remove --all         # clean slate
```

---

## VS Code extension

Run code cells in Markdown and Quarto files, powered by rat kernels.

| Feature | How |
|---------|-----|
| Run cells | `▶ Run` CodeLens or `Ctrl+Enter` |
| Output | Results in ` ```output ``` ` blocks |
| Plots | Matplotlib PNGs saved to `_assets/` |
| Completions | Live from kernel |
| Hover | Rich inspection (type, shape, docs) |
| stdin | `input()` pops a VS Code input box |

Install from the VS Code marketplace or see [vscode-rat/](vscode-rat/).

---

## For app builders

### Start a kernel

```bash
rat serve py --http --port 8717 --cwd ~/project --venv ~/project/.venv
rat serve r --http --port 8718
rat serve py                    # stdio mode (default)
```

### Connect from your app

```javascript
const py = new McpClient("http://localhost:8717/mcp");
await py.callTool("run", { code: "x = 42" });
await py.callTool("look");
await py.callTool("look", { at: "df" });
await py.callTool("ctl", { op: "reset" });
```

### Claude Desktop / Cursor config

```json
{
  "mcpServers": {
    "python": { "url": "http://127.0.0.1:8717/mcp" }
  }
}
```

`rat install` writes this automatically.

---

## Custom runtimes

Add any language by dropping a `runtime.yaml` and a kernel script in `~/.config/rat/runtimes/<lang>/`. No Go code, no recompilation.

```yaml
name: ruby
display: Ruby

detect:
  commands: [ruby]
  env: RAT_RUBY

kernel:
  type: json
  script: kernel.rb
```

The kernel script reads JSON lines from stdin, writes JSON lines to stdout. See [docs/custom-runtimes.md](docs/custom-runtimes.md) for the full guide and [KERNEL-PROTOCOL.md](KERNEL-PROTOCOL.md) for the wire format.

Built-in runtimes (R, pi) use the same mechanism.

---

## Architecture

One Go binary. Language kernels as subprocesses.

```
rat (Go binary)
├── CLI           install, run, look, status, start, stop, ...
├── REPL          rat py → IPython connected to shared kernel via MCP
├── MCP server    rat serve → HTTP or stdio
├── Resolver      project-scoped naming (py@myproject)
└── Kernels
     ├── python   built-in, IPython + jedi
     ├── bash     built-in, direct PTY
     ├── r        generic JSON kernel (runtime.yaml + kernel.R)
     ├── pi       generic tmux kernel (runtime.yaml + bridge.ts)
     └── custom   ~/.config/rat/runtimes/<lang>/
```

The MCP server is Go. The kernel subprocess runs in the user's interpreter with access to their packages. Communication is JSON lines over pipes.

---

## Command reference

```
Daily use
  rat <lang>                      REPL (auto-starts kernel)
  rat run <runtime> '<code>'      Execute code
  rat look <runtime> [--at SYM]   Inspect namespace
  rat tail <runtime>              Recent activity
  rat pick                        Interactive kernel picker
  rat cancel <runtime>            Interrupt execution
  rat reset <runtime>             Clear namespace
  rat restart <runtime>           Fresh start

Setup
  rat install <lang> [<lang>...]  Set up runtimes + deps
  rat setup                       Interactive wizard
  rat add <name> [dir]            Register named runtime
  rat remove <name> [--all]       Delete state entry

Management
  rat status [-v]                 What's running
  rat start <runtime>             Start a kernel
  rat stop <runtime> [--all]      Stop a kernel
  rat serve <name> [--http]       MCP server (for app builders)
  rat doctor [<lang>]             Diagnostics
  rat version                     Version info
  rat update                      Update rat
```

---

## Supported languages

| Lang | REPL | Kernel type | `rat install` installs |
|------|------|-------------|----------------------|
| `py` | IPython | built-in (JSON) | ipython, jedi into venv |
| `sh` | bash | built-in (PTY) | nothing |
| `r` | R console | generic (JSON) | jsonlite |
| `pi` | pi agent | generic (tmux) | @mariozechner/pi-coding-agent |

More via [custom runtimes](docs/custom-runtimes.md). Julia and Node.js are in progress.

---

## Links

- [Kernel protocol](KERNEL-PROTOCOL.md)
- [Custom runtimes](docs/custom-runtimes.md)
- [VS Code extension](vscode-rat/)
- [Roadmap](ROADMAP.md)
- [Original vision document](docs/README-vision.md)
