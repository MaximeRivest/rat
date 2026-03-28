# REPL Architecture — Use the real REPL, intercept execution

The rule: **if the language has a great REPL, use it.** We only replace the pipe between "user hits Enter" and "code executes." Everything else stays native.

`rat py` is not a custom readline loop that sort of looks like IPython. It IS IPython. Full syntax highlighting, multiline editing, tab completion, `?` help, `%magic`, paste mode, auto-indent, history. The only thing we change is where code goes when you hit Enter — it goes to the shared kernel via MCP instead of a local namespace.

---

## Per-language strategy

### Python — IPython `TerminalInteractiveShell` override

IPython has clean extension points. We subclass `TerminalInteractiveShell` and override two methods:

```python
class RatShell(TerminalInteractiveShell):

    def run_cell(self, raw_cell, **kw):
        # This is the ONLY execution override.
        # Everything else is real IPython.
        result = mcp_call(self.server_url, "run", {"code": raw_cell})
        self.display(result)

    def complete(self, text, line, cursor_pos):
        # Completions from the shared kernel (live namespace)
        return mcp_call(self.server_url, "look", {"code": line, "cursor": cursor_pos})
```

Everything else is untouched IPython:

| Feature | Where it runs | Why |
|---------|---------------|-----|
| Regular code | shared kernel | the whole point |
| Tab completion | shared kernel | needs live namespace |
| `?` / `??` inspect | shared kernel via `look(at=...)` | needs live objects |
| `%magic` commands | local IPython | IPython feature, not Python |
| `!shell` commands | local | about your machine, not the kernel |
| `%run script.py` | read local, execute on kernel | script is local, execution is shared |
| `%debug` | local fallback | too stateful for shared mode |
| History (`↑`/`↓`) | local IPython | per-terminal history |
| Syntax highlighting | local IPython | UI concern |
| Multiline editing | local IPython | UI concern |
| Auto-indent | local IPython | UI concern |
| Paste mode | local IPython | UI concern |

**How `rat py` launches it:**

The Go binary:
1. Reads kernel config → finds venv path and server URL
2. Runs: `/path/to/venv/bin/python <embedded rat_ipython_frontend.py> --server http://127.0.0.1:8717/mcp`

The frontend script is embedded in the Go binary via `go:embed`. It runs in the **same venv** as the kernel — same interpreter, same packages, same completions.

### Julia — REPL module with eval hook

Julia's REPL is famously good: modes (help `?`, shell `;`, package `]`), Unicode completion (`\alpha<tab>` → `α`), contextual help, syntax highlighting. We keep all of it.

```julia
using REPL

function rat_eval(code::String)
    mcp_call("run", Dict("code" => code))
end

# Hook into the REPL's backend eval
# Julia's REPL module supports custom eval functions
atreplinit() do repl
    repl.interface.modes[1].on_done = rat_eval
end
```

The Julia REPL handles everything visual and interactive. We only replace eval.

### R — radian (best R console)

R's built-in console has minimal editing. `radian` is a Python-based R console with syntax highlighting, multiline editing, bracket matching, and completion. It uses `prompt_toolkit` and has a clean eval interface.

```python
# radian's eval can be overridden
def rat_eval(code):
    return mcp_call("run", {"code": code})
```

`rat install r` installs radian into a managed venv (separate from the user's Python). `rat r` launches radian with the custom eval backend.

**Fallback:** On systems where radian isn't available, use R's built-in readline console with a custom `.Rprofile` that wraps `eval()` to route through MCP. Less pretty, but functional.

### Bash — PTY passthrough

No REPL reimplementation needed. The kernel runs bash in a PTY (via Go's `creack/pty`, already proven in mrmd-bash). `rat sh` attaches your terminal to that PTY.

This is how `tmux` and `screen` work. The shell runs in a server-owned PTY. The client connects its terminal to it. It's the real bash — `PS1` prompt, readline, `.bashrc`, everything.

MCP clients and the PTY coexist:
- MCP `run` calls inject code into the PTY via `write()`, capture output between markers
- `rat sh` connects stdin/stdout directly to the PTY
- When both are active, MCP calls queue behind interactive use

### Node.js — `repl` module with eval override

```javascript
const repl = require('repl');

repl.start({
  prompt: '> ',
  eval: (code, context, filename, callback) => {
    const result = mcpCall("run", { code });
    callback(null, result);
  },
  completer: (line) => {
    const result = mcpCall("look", { code: line, cursor: line.length });
    return [result.matches, line];
  }
});
```

Node's REPL handles: syntax highlighting, tab completion rendering, multiline, `.break`, `.clear`, `.editor` mode.

---

## Edge cases

### Which kernel does `rat py` connect to?

**CWD-based resolution.** `rat py` looks for a kernel whose cwd matches the current directory or a parent.

```bash
cd ~/analysis && rat py     # → kernel with cwd ~/analysis
cd ~/ml && rat py           # → kernel with cwd ~/ml
cd ~/random && rat py       # → default py kernel
```

Resolution order:
1. Exact cwd match
2. Nearest parent match
3. Default kernel for that language
4. No kernel → auto-start one with current cwd + auto-detected venv

The prompt shows which kernel:

```python
# rat py | ~/analysis/.venv | :8717
In [1]:
```

### venv for the REPL frontend

The REPL frontend must run in the **same venv** as the kernel. Otherwise tab completion won't know about installed packages (`import pan<tab>` → nothing, because pandas isn't in the frontend's Python).

`rat py` reads the kernel config, finds its venv, and launches the frontend script with that venv's Python interpreter. Same interpreter, same packages, same world.

### Kernel dies mid-REPL

The Go server detects the subprocess exit. The REPL frontend detects the broken MCP connection.

```python
In [3]: segfaulty_c_extension()

Kernel disconnected. Restart? [Y/n] y
Restarted. Namespace cleared.

In [1]:
```

Clean recovery. The user chooses whether to restart or exit.

### Concurrent execution (you + Claude at the same time)

The kernel serializes execution. One at a time. If you hit Enter while Claude is executing:

```python
In [3]: df.describe()
# ⏳ waiting — another client is executing...
# ...done
Out[3]:        col1    col2
       count  1000    1000
       mean   42.3    18.7
```

The REPL shows a waiting indicator. No interleaving, no corruption. First in, first out.

When the other client's execution completes, the REPL could optionally show what happened:

```python
# [Claude ran: model.fit(X, y) — ✓ 3.2s]
In [3]: df.describe()
```

This keeps the REPL user aware of shared activity without breaking their flow.

### Ctrl+C

The REPL frontend catches SIGINT and sends `ctl(op="cancel")` to the kernel via MCP. The kernel sends SIGINT to the language subprocess. Execution aborts. Same behavior as native IPython / Julia / R.

```python
In [4]: slow_computation()
^C
KeyboardInterrupt
In [5]:
```

### Plots and rich output

```python
In [4]: plt.plot([1,2,3]); plt.show()
```

The kernel captures the figure as an asset (PNG/SVG). How the REPL displays it depends on the terminal:

| Terminal | Display |
|----------|---------|
| iTerm2 / kitty | inline image (imgcat protocol) |
| VS Code terminal | inline image |
| Plain terminal | `[image: ~/.rat/assets/fig-a1b2.png]` + auto-open if configured |
| tmux | path only |

MCP clients get the asset URI in the tool response. Notebook apps render inline. Each client displays what it can.

### Large output

The kernel streams output. The REPL frontend shows it all (it's a terminal — scrollback is fine). MCP clients get a truncated version with a note:

```
… [847 lines truncated — showing first 50 and last 50] …
```

### Stale kernel (process died, state file says running)

```bash
rat py run 'x = 42'
```

1. Read state file → kernel should be at `:8717`
2. Try to connect → connection refused
3. Kernel is dead → clean up state file
4. Auto-start a new kernel
5. Run code

The user sees a brief delay on the first call, not an error.

### Port conflicts

`rat serve py` tries `:8717`. Taken? Tries `:8718`, `:8719`, etc. The assigned port is recorded in `~/.config/rat/state.yaml`. All clients discover the port from there, never hardcoded.

### `input()` calls from shared kernel

Someone calls `input("Name: ")` — who answers?

```python
# In the REPL — you see it, you answer it
In [5]: name = input("Name: ")
Name: Alice

# From Claude — Claude sees INPUT REQUESTED, responds via run(input="Alice")

# From a script — rat py run 'name = input("Name: ")' blocks,
#                  shows "INPUT REQUESTED: Name:" on stderr,
#                  reads from stdin
```

The kernel sends an input request via MCP. Whichever client initiated the execution is responsible for providing the input.

### Multiple REPLs attached to the same kernel

Terminal 1 and Terminal 2 both run `rat py`. Both are connected to the same kernel.

```
Terminal 1:                        Terminal 2:
In [1]: x = 42                     In [1]: x
                                    Out[1]: 42
In [2]: del x
                                    In [2]: x
                                    NameError: name 'x' is not defined
```

Each REPL has its own IPython frontend (own history, own prompt counter). They share the namespace. Execution is serialized — if Terminal 1 is running something, Terminal 2 waits.

Optional: broadcast notifications so Terminal 2 sees what Terminal 1 did:

```
Terminal 2:
# [another client ran: del x — ✓ 1ms]
In [2]:
```

---

## What we build vs what we borrow

| Component | Build or borrow | Detail |
|-----------|----------------|--------|
| MCP server | build (Go) | shared across all languages |
| Kernel protocol | build (Go ↔ JSON pipes) | simple, language-agnostic |
| Python kernel | build (kernel.py, ~400 lines) | IPython execution engine |
| Python REPL | **borrow IPython** | subclass `TerminalInteractiveShell`, override 2 methods |
| R kernel | build (kernel.R, ~300 lines) | R execution engine |
| R REPL | **borrow radian** | override eval function |
| Julia kernel | build (kernel.jl, ~300 lines) | Julia execution engine |
| Julia REPL | **borrow Julia REPL module** | override eval function |
| Bash kernel | build (Go, already exists) | PTY management |
| Bash REPL | **borrow bash itself** | PTY passthrough |
| Node kernel | build (kernel.js, ~200 lines) | Node execution engine |
| Node REPL | **borrow Node `repl` module** | override eval function |

The kernels are thin: execute code, capture output, list variables, provide completions. ~200-400 lines each.

The REPLs are zero: we don't build them. We use the real thing and redirect where "execute" goes.

---

## The test

If someone runs `rat py` and can't tell the difference from running `ipython` directly — except that Claude can see their variables — we did it right. Same for `rat r` vs `radian`, `rat ju` vs `julia`, `rat sh` vs `bash`.

The REPL should feel native because it IS native. We're not reimplementing REPLs. We're connecting them.
