
What's missing

### 1. State management — the foundation for everything else

Nothing persists. No ~/.config/rat/state.yaml. Without this, none of the lifecycle
commands can work.

Needed:
- State file (~/.config/rat/state.yaml) — tracks running kernels: name, lang, port,
PID, cwd, venv, state
- State read/write — CRUD for kernel entries
- Stale detection — check if a PID is actually alive, clean up if not

This blocks: ls, start, stop, restart, reset, cancel, run, look, and the REPL
shorthand.

### 2. Background kernel management (rat start/stop/restart)

Currently rat serve sh runs in the foreground. The README vision is auto-start kernels
in the background.

Needed:
- rat start <name> — fork rat serve <name> --http --port <auto> as a background
process, record PID/port in state
- rat stop <name> — read state, send SIGTERM to PID, clean up state
- rat stop --all — stop everything
- rat restart <name> — stop + start
- Auto-port selection (try 8717, 8718, etc.)

### 3. Auto-start on first use

The README promises rat run py 'x=42' auto-starts if no kernel is running.

Needed:
- Every command that talks to a kernel (run, look, reset, cancel, the REPL) should:
check state → not running? → start → then proceed
- MCP client in the Go binary — HTTP client that can call run/look/ctl on a running
kernel's HTTP endpoint

### 4. rat run <name> '<code>' — CLI code execution

Stub only. Needs:
- Resolve kernel (state lookup + auto-start)
- HTTP POST to the kernel's MCP endpoint → run(code=...)
- Print result to stdout, errors to stderr
- Exit code: 0 on success, 1 on error

### 5. rat look <name> [--at <sym>] — CLI inspection

Same pattern as run but calls look tool.

### 6. rat reset <name> / rat cancel <name> — CLI control

Same pattern, calls ctl(op="reset") / ctl(op="cancel").

### 7. rat ls — list runtimes

Read state file, check each PID is alive, print table:

```
NAME     LANG  PORT   STATE    CWD
sh       sh    8720   running  ~/project
```

### 8. rat <name> — REPL

The big one. For sh, the REPL-ARCHITECTURE.md says PTY passthrough (like tmux).

Needed for rat sh:
- Connect terminal stdin/stdout to the kernel's PTY (or proxy through MCP)
- This is different from other languages — bash REPL IS the kernel, not a frontend that
talks to MCP

Question: Does rat sh attach directly to the PTY, or does it go through MCP like
everything else? The architecture doc says "PTY passthrough" which suggests direct
attach, but that breaks the "everything through MCP" model for concurrent access.

### 9. rat install <lang>

For sh: essentially a no-op (bash is built-in). But still needs to:
- Verify bash exists
- Start the kernel in background
- Write Claude Desktop / Cursor config files

### 10. rat setup — interactive wizard

Terminal UI: checkbox selection of languages, detect MCP clients, write configs.

### 11. rat add <name> / rat rm <name> — named runtimes

For custom named kernels (e.g., py-ml). Needs --venv and --cwd flags on add. Writes to
a config file (~/.config/rat/config.yaml).

### 12. rat doctor — diagnostics

Check: bash available, ports free, state file consistent, Claude Desktop config valid,
etc.

### 13. rat update — self-update

Download new binary, replace in place.

### 14. input support in MCP run tool

The MCP server has a TODO for input handling. The bash worker already supports onStdin
callbacks, but the MCP layer doesn't wire it up.

### 15. --port 0 (auto-assign)

README shows rat serve py --http --port 0 for app builders. Needs to listen on :0,
report the actual port.

────────────────────────────────────────────────────────────────────────────────

Suggested build order

1. State file (unlocks everything)
2. start/stop (background kernel management)
3. MCP HTTP client (Go code to call a running kernel)
4. run/look/reset/cancel (CLI commands that use the client)
5. ls (reads state)
6. rat sh REPL (PTY attach)
7. install sh + config writing
8. doctor
9. add/rm/setup

The bash kernel and MCP server are solid. The main gap is the orchestration layer —
state tracking, background process management, and an MCP client so CLI commands can
talk to running kernels.