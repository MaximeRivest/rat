"""
IPython frontend for rat py.

Routes execution and completions to the shared MCP kernel.
Everything else stays native IPython: history, multiline editing,
syntax highlighting, %magics, !shell.

Usage (called by Go binary):
    python frontend.py --server http://127.0.0.1:8717/mcp --name py
"""

import argparse
import http.client
import json
import re
import sys
import threading
import urllib.parse


# ── Minimal MCP client (stdlib only, no dependencies) ────────

_MSG_ID = 0
_SESSION_ID = None


def _mcp_call(url, method, params=None, timeout=300):
    """JSON-RPC call to the MCP server. Returns the result dict."""
    global _MSG_ID, _SESSION_ID
    _MSG_ID += 1
    mid = _MSG_ID

    body = json.dumps({
        "jsonrpc": "2.0",
        "id": mid,
        "method": method,
        "params": params or {},
    }).encode()

    parsed = urllib.parse.urlparse(url)
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
    }
    if _SESSION_ID:
        headers["Mcp-Session-Id"] = _SESSION_ID

    try:
        conn = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=timeout)
        conn.request("POST", parsed.path, body, headers)
        resp = conn.getresponse()

        sid = resp.getheader("Mcp-Session-Id")
        if sid:
            _SESSION_ID = sid

        ct = resp.getheader("Content-Type", "")
        raw = resp.read().decode()
        conn.close()

        # Handle SSE responses (MCP streamable HTTP)
        if "text/event-stream" in ct:
            for line in raw.split("\n"):
                if line.startswith("data: "):
                    try:
                        obj = json.loads(line[6:])
                        if obj.get("id") == mid:
                            if "error" in obj:
                                return {"_error": obj["error"]}
                            return obj.get("result", {})
                    except json.JSONDecodeError:
                        pass
            return {"_error": "no matching response in SSE stream"}

        # Plain JSON response
        obj = json.loads(raw)
        if "error" in obj:
            return {"_error": obj["error"]}
        return obj.get("result", {})

    except Exception as e:
        return {"_error": str(e)}


def _mcp_notify(url, method, params=None):
    """JSON-RPC notification (no response expected)."""
    global _SESSION_ID
    body = json.dumps({
        "jsonrpc": "2.0",
        "method": method,
        "params": params or {},
    }).encode()
    parsed = urllib.parse.urlparse(url)
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
    }
    if _SESSION_ID:
        headers["Mcp-Session-Id"] = _SESSION_ID
    try:
        conn = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=10)
        conn.request("POST", parsed.path, body, headers)
        resp = conn.getresponse()
        sid = resp.getheader("Mcp-Session-Id")
        if sid:
            _SESSION_ID = sid
        resp.read()
        conn.close()
    except Exception:
        pass


def _mcp_init(url):
    """Initialize the MCP session."""
    r = _mcp_call(url, "initialize", {
        "protocolVersion": "2025-03-26",
        "capabilities": {},
        "clientInfo": {"name": "rat-py-repl", "version": "0.1.0"},
    })
    if "_error" not in r:
        _mcp_notify(url, "notifications/initialized")
    return r


def _tool(url, name, args, timeout=300):
    """Call an MCP tool."""
    return _mcp_call(url, "tools/call", {"name": name, "arguments": args}, timeout=timeout)


def _text(result):
    """Extract text content from an MCP tool result."""
    if "_error" in result:
        return f"[rat] error: {result['_error']}"
    parts = []
    for item in result.get("content", []):
        if isinstance(item, dict) and item.get("type") == "text":
            t = item.get("text", "")
            if t:
                parts.append(t)
    return "\n".join(parts)


def _is_err(result):
    """Check if an MCP tool result is an error."""
    return result.get("isError", False) or "_error" in result


_HINT_RE = re.compile(r"\n\n[✓✗] .+$")


def _strip_hint(s):
    """Remove the trailing timing hint (✓ 3ms) from run output."""
    return _HINT_RE.sub("", s)


# ── Activity watcher ─────────────────────────────────────────

class ActivityWatcher:
    """Watches the activity log written by Go and reports executions
    from other MCP clients (Claude, scripts, other terminals).
    Thread-safe — can be polled from a background watcher thread."""

    def __init__(self, path):
        self.path = path
        self.pos = 0
        self._my_codes = []
        self._lock = threading.Lock()

    def mark_own(self, code):
        """Remember a code string we submitted so we can skip it."""
        with self._lock:
            self._my_codes.append(code.strip())
            if len(self._my_codes) > 20:
                self._my_codes = self._my_codes[-20:]

    def check(self):
        """Read new activity entries. Return entries from other clients."""
        with self._lock:
            if not self.path:
                return []
            try:
                with open(self.path, "r") as f:
                    f.seek(self.pos)
                    new_lines = f.readlines()
                    self.pos = f.tell()
            except (FileNotFoundError, OSError):
                return []

            others = []
            for line in new_lines:
                line = line.strip()
                if not line:
                    continue
                try:
                    entry = json.loads(line)
                except (json.JSONDecodeError, ValueError):
                    continue
                code = entry.get("code", "").strip()
                if code in self._my_codes:
                    self._my_codes.remove(code)
                    continue
                others.append(entry)
            return others

    def seek_to_end(self):
        """Skip all existing entries (only show new activity)."""
        with self._lock:
            if not self.path:
                return
            try:
                with open(self.path, "r") as f:
                    f.seek(0, 2)
                    self.pos = f.tell()
            except (FileNotFoundError, OSError):
                pass


DIM = "\033[2m"
RESET_ANSI = "\033[0m"


def _format_activity(entries):
    """Format activity entries for display in the REPL."""
    lines = []
    for e in entries:
        code = e.get("code", "")
        output = e.get("output", "")
        n = e.get("n", "?")
        ok = e.get("ok", True)
        mark = "✓" if ok else "✗"

        lines.append(f"{DIM}── rat: exec #{n} (another client) {mark} ──{RESET_ANSI}")
        for cl in code.split("\n")[:8]:
            lines.append(f"{DIM}>>> {cl}{RESET_ANSI}")
        if output:
            for ol in output.split("\n")[:8]:
                lines.append(f"{DIM}{ol}{RESET_ANSI}")
        lines.append(f"{DIM}{'─' * 42}{RESET_ANSI}")
    return "\n".join(lines)


# ── IPython shell with MCP backend ──────────────────────────

def _make_shell(server_url, activity_log=None):
    """Create an IPython shell class that routes execution to MCP."""
    from IPython.terminal.interactiveshell import TerminalInteractiveShell
    from IPython.core.interactiveshell import ExecutionResult

    class RatShell(TerminalInteractiveShell):
        """Real IPython. Only execution and completions go through MCP."""

        def __init__(self, *args, **kwargs):
            super().__init__(*args, **kwargs)
            self._server = server_url
            self._mcp_ok = False
            self._activity = ActivityWatcher(activity_log)
            self._activity.seek_to_end()
            self._patch_completer()

        def interact(self):
            """Main REPL loop — wrapped with patch_stdout so the
            background activity watcher can print live updates
            without corrupting the prompt."""
            from prompt_toolkit.patch_stdout import patch_stdout

            stop = threading.Event()

            def watcher():
                while not stop.wait(0.5):
                    entries = self._activity.check()
                    if entries:
                        # patch_stdout makes this safe while the
                        # prompt is active — prompt_toolkit redraws
                        # the input line after our output.
                        print(_format_activity(entries))

            thread = threading.Thread(target=watcher, daemon=True)
            thread.start()
            try:
                with patch_stdout():
                    super().interact()
            finally:
                stop.set()

        def _ensure_mcp(self):
            if not self._mcp_ok:
                r = _mcp_init(self._server)
                self._mcp_ok = "_error" not in r

        def _patch_completer(self):
            """Replace IPython's completer with one backed by the shared kernel."""
            from IPython.core.completer import Completion

            original = self.Completer.completions
            server = self._server

            def completions(text, offset):
                try:
                    r = _tool(server, "look",
                              {"code": text, "cursor": offset},
                              timeout=5)
                    out = _text(r)

                    labels = []
                    for line in out.split("\n"):
                        line = line.strip()
                        if line and line != "No completions.":
                            parts = line.split()
                            if parts:
                                labels.append(parts[0])

                    if labels:
                        # Find start of the token being completed.
                        # If labels contain dots (full-path completions from
                        # fallback completer), go back past dots too.
                        has_dots = any("." in label for label in labels)
                        start = offset
                        if has_dots:
                            while start > 0 and text[start - 1] not in " \t\n(,=[{":
                                start -= 1
                        else:
                            while start > 0 and text[start - 1] not in " \t\n(,=[{.":
                                start -= 1
                        for label in labels:
                            yield Completion(start, offset, label)
                        return
                except Exception:
                    pass
                # Fall back to IPython local completions (magics, paths)
                yield from original(text, offset)

            self.Completer.completions = completions

        def run_cell(self, raw_cell, store_history=True, silent=False,
                     shell_futures=True, cell_id=None):
            """Execute on the shared MCP kernel."""
            raw_cell = raw_cell.strip()
            if not raw_cell:
                return ExecutionResult(None)

            # Exit locally
            if raw_cell in ("exit", "exit()", "quit", "quit()"):
                self.ask_exit()
                return ExecutionResult(None)

            # Magics and shell escapes stay local
            if raw_cell.startswith("%") or raw_cell.startswith("!"):
                return super().run_cell(
                    raw_cell, store_history=store_history,
                    silent=silent, shell_futures=shell_futures,
                    cell_id=cell_id,
                )

            # ? / ?? inspection via MCP look
            if raw_cell.endswith("??") or raw_cell.endswith("?"):
                sym = raw_cell.rstrip("?").strip()
                if sym:
                    try:
                        r = _tool(self._server, "look",
                                  {"at": sym}, timeout=10)
                        out = _text(r)
                        if out:
                            print(out)
                    except Exception as e:
                        print(f"[rat] error: {e}")
                    return ExecutionResult(None)

            # Execute on the shared kernel
            self._ensure_mcp()
            self._activity.mark_own(raw_cell)
            er = ExecutionResult(None)
            try:
                r = _tool(self._server, "run", {"code": raw_cell})
                out = _strip_hint(_text(r))
                if out:
                    print(out)
                if _is_err(r):
                    # Signal failure via error_in_exec (success is a
                    # read-only property derived from this).
                    er.error_in_exec = RuntimeError("execution failed")
            except Exception as e:
                print(f"[rat] connection error: {e}")
                print("[rat] kernel may have stopped. "
                      "Ctrl-D to exit, then 'rat py' to reconnect.")
                self._mcp_ok = False
                er.error_in_exec = e

            if store_history:
                self.execution_count += 1

            return er

    return RatShell


# ── Main ─────────────────────────────────────────────────────

def main():
    parser = argparse.ArgumentParser(description="IPython frontend for rat py")
    parser.add_argument("--server", default="http://127.0.0.1:8717/mcp",
                        help="MCP server URL")
    parser.add_argument("--name", default="py",
                        help="Kernel name")
    parser.add_argument("--activity-log", default=None,
                        help="Path to activity log (for shared session visibility)")
    args = parser.parse_args()

    # Check IPython is available
    try:
        import IPython  # noqa: F401
    except ImportError:
        print("IPython is not installed.", file=sys.stderr)
        print("Install it:  pip install ipython", file=sys.stderr)
        sys.exit(1)

    # Connect to kernel
    r = _mcp_init(args.server)
    if "_error" in r:
        print(f"Cannot connect to kernel at {args.server}",
              file=sys.stderr)
        print(f"Error: {r['_error']}", file=sys.stderr)
        print(f"\nIs the kernel running? Try: rat start {args.name}",
              file=sys.stderr)
        sys.exit(1)

    info = r.get("serverInfo", {})
    server_name = info.get("name", args.name)

    # Launch IPython with our custom shell
    from IPython.terminal.ipapp import TerminalIPythonApp

    Shell = _make_shell(args.server, activity_log=args.activity_log)
    app = TerminalIPythonApp.instance()
    app.interact = True
    app.interactive_shell_class = Shell
    app.display_banner = False
    app.initialize(["--no-banner"])

    print(f"rat {args.name} | {server_name} @ {args.server}")
    print("Shared namespace — other clients see your variables.")
    print()

    app.start()


if __name__ == "__main__":
    main()
