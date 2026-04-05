"""
R frontend for rat — radian-style REPL routed through the shared MCP kernel.

Uses prompt_toolkit for the input experience (syntax highlighting, multi-line
editing, history, toolbar) and routes execution + completions to the shared
kernel. Everything the human types goes through MCP — same namespace as Claude.

Usage (called by Go binary):
    python3 frontend-r.py --server http://127.0.0.1:8720/mcp --name r@rat
"""

import argparse
import http.client
import json
import os
import re
import sys
import threading
import time
import urllib.parse


# ── Minimal MCP client (stdlib only) ────────────────────────

_MSG_ID = 0
_SESSION_ID = None


def _mcp_call(url, method, params=None, timeout=300):
    global _MSG_ID, _SESSION_ID
    _MSG_ID += 1
    mid = _MSG_ID
    body = json.dumps({
        "jsonrpc": "2.0", "id": mid, "method": method,
        "params": params or {},
    }).encode()
    parsed = urllib.parse.urlparse(url)
    headers = {"Content-Type": "application/json",
               "Accept": "application/json, text/event-stream"}
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
        if "text/event-stream" in ct:
            for line in raw.split("\n"):
                if line.startswith("data: "):
                    try:
                        obj = json.loads(line[6:])
                        if obj.get("id") == mid:
                            return obj.get("result", {}) if "error" not in obj else {"_error": obj["error"]}
                    except json.JSONDecodeError:
                        pass
            return {"_error": "no matching response in SSE stream"}
        obj = json.loads(raw)
        return obj.get("result", {}) if "error" not in obj else {"_error": obj["error"]}
    except Exception as e:
        return {"_error": str(e)}


def _mcp_notify(url, method, params=None):
    global _SESSION_ID
    body = json.dumps({"jsonrpc": "2.0", "method": method, "params": params or {}}).encode()
    parsed = urllib.parse.urlparse(url)
    headers = {"Content-Type": "application/json",
               "Accept": "application/json, text/event-stream"}
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


def _make_client_name(prefix):
    try:
        tty = os.path.basename(os.ttyname(sys.stdin.fileno()))
    except OSError:
        tty = f"pid-{os.getpid()}"
    return f"{prefix}@{tty}"



def _mcp_init(url, client_name):
    r = _mcp_call(url, "initialize", {
        "protocolVersion": "2025-03-26",
        "capabilities": {},
        "clientInfo": {"name": client_name, "version": "0.1.0"},
    })
    if "_error" not in r:
        _mcp_notify(url, "notifications/initialized")
    return r


def _tool(url, name, args, timeout=300):
    return _mcp_call(url, "tools/call", {"name": name, "arguments": args}, timeout=timeout)


def _text(result):
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
    return result.get("isError", False) or "_error" in result


_HINT_RE = re.compile(r"\n\n[✓✗] .+$")


def _strip_hint(s):
    return _HINT_RE.sub("", s)


# ── R expression completeness detection ──────────────────────

def _is_complete_r(code):
    """Heuristic: check if an R expression looks complete."""
    code = code.strip()
    if not code:
        return True

    # Remove strings and comments for bracket counting.
    cleaned = re.sub(r'#.*$', '', code, flags=re.MULTILINE)
    cleaned = re.sub(r'"(?:[^"\\]|\\.)*"', '""', cleaned)
    cleaned = re.sub(r"'(?:[^'\\]|\\.)*'", "''", cleaned)

    opens = cleaned.count('(') + cleaned.count('[') + cleaned.count('{')
    closes = cleaned.count(')') + cleaned.count(']') + cleaned.count('}')
    if opens > closes:
        return False

    # Trailing operators suggest continuation.
    last_line = code.rstrip().split('\n')[-1].rstrip()
    if last_line and last_line[-1] in ('+', '-', '*', '/', '|', '&', ',', '=', '~', '\\'):
        return False
    if last_line.endswith('%>%') or last_line.endswith('|>'):
        return False

    return True


# ── Activity watcher ─────────────────────────────────────────

# Activity colors — use ANSI 16 colors so terminal themes control readability.
_BAR     = "\033[2;35m"   # dim magenta for the border
_LABEL   = "\033[2;37m"   # dim white for the label
_CODE    = "\033[2m"      # dim default for code
_OUTPUT  = "\033[2m"      # dim default for output
_ERR_DIM = "\033[2;31m"   # dim red for failed marker
_R       = "\033[0m"


class ActivityWatcher:
    def __init__(self, path):
        self.path = path
        self.pos = 0
        self._my_codes = []
        self._lock = threading.Lock()

    def mark_own(self, code):
        with self._lock:
            self._my_codes.append(code.strip())
            if len(self._my_codes) > 20:
                self._my_codes = self._my_codes[-20:]

    def check(self):
        with self._lock:
            if not self.path:
                return []
            try:
                with open(self.path, "r") as f:
                    f.seek(self.pos)
                    lines = f.readlines()
                    self.pos = f.tell()
            except (FileNotFoundError, OSError):
                return []
            others = []
            for line in lines:
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
        with self._lock:
            if not self.path:
                return
            try:
                with open(self.path, "r") as f:
                    f.seek(0, 2)
                    self.pos = f.tell()
            except (FileNotFoundError, OSError):
                pass


def _activity_label(entry, own_client=""):
    client = (entry.get("client") or "").strip()
    if not client:
        return "activity"
    if own_client and client == own_client:
        return "you"
    if client.startswith("rat-r-repl@"):
        return f"repl {client.split('@', 1)[1]}"
    if client.startswith("rat-py-repl@"):
        return f"Python repl {client.split('@', 1)[1]}"
    return client



def _format_activity(entries, own_client=""):
    lines = []
    for e in entries:
        code = e.get("code", "")
        output = e.get("output", "")
        ok = e.get("ok", True)
        mark = "✓" if ok else "✗"
        mark_color = _LABEL if ok else _ERR_DIM
        label = _activity_label(e, own_client)

        # Filter blank code lines.
        code_lines = [cl for cl in code.split("\n") if cl.strip()][:6]
        out_lines = [ol for ol in output.split("\n") if ol.strip()][:6]

        # Header: thin bar + label.
        lines.append(f"{_BAR}▍{mark_color} {label} {mark}{_R}")
        for cl in code_lines:
            lines.append(f"{_BAR}▍ {_CODE}{cl}{_R}")
        if out_lines:
            for ol in out_lines:
                lines.append(f"{_BAR}▍ {_OUTPUT}{ol}{_R}")
        lines.append("")  # blank line between entries
    return "\n".join(lines)



def _read_recent_activity(path, limit=10):
    if not path:
        return []
    try:
        with open(path, "r") as f:
            lines = f.readlines()
    except (FileNotFoundError, OSError):
        return []
    entries = []
    for line in lines[-limit:]:
        line = line.strip()
        if not line:
            continue
        try:
            entries.append(json.loads(line))
        except (json.JSONDecodeError, ValueError):
            continue
    return entries


# ── prompt_toolkit REPL ──────────────────────────────────────

def main():
    parser = argparse.ArgumentParser(description="R frontend for rat")
    parser.add_argument("--server", required=True, help="MCP server URL")
    parser.add_argument("--name", default="r", help="Kernel name")
    parser.add_argument("--activity-log", default=None, help="Activity log path")
    args = parser.parse_args()

    # Ctrl+Z detaches (same as Ctrl+D) — rat owns the kernel lifecycle.
    import signal as _signal
    def _sigtstp_handler(signum, frame):
        print(f"\nDetached. Kernel still running. Reconnect: rat {args.name}")
        sys.exit(0)
    _signal.signal(_signal.SIGTSTP, _sigtstp_handler)

    # Connect to kernel.
    client_name = _make_client_name("rat-r-repl")
    r = _mcp_init(args.server, client_name)
    if "_error" in r:
        print(f"Cannot connect to kernel at {args.server}", file=sys.stderr)
        print(f"Error: {r['_error']}", file=sys.stderr)
        print(f"\nIs the kernel running? Try: rat start {args.name}", file=sys.stderr)
        sys.exit(1)

    # Import prompt_toolkit.
    try:
        from prompt_toolkit import PromptSession
        from prompt_toolkit.history import FileHistory
        from prompt_toolkit.completion import Completer, Completion
        from prompt_toolkit.patch_stdout import patch_stdout
        from prompt_toolkit.formatted_text import HTML, ANSI, FormattedText
        from prompt_toolkit import print_formatted_text as ptk_print
        from prompt_toolkit.key_binding import KeyBindings
        from prompt_toolkit.styles import Style, merge_styles, style_from_pygments_cls
        from prompt_toolkit.keys import Keys
    except ImportError:
        print("prompt_toolkit not installed. Install: pip install prompt-toolkit", file=sys.stderr)
        print("Falling back to basic REPL.", file=sys.stderr)
        _basic_repl(args)
        return

    # Try to get R syntax highlighting via pygments.
    # Keep token colors on terminal defaults so light and dark themes stay readable.
    lexer = None
    try:
        from prompt_toolkit.lexers import PygmentsLexer
        from pygments.lexers import SLexer
        lexer = PygmentsLexer(SLexer)
    except ImportError:
        pass

    # Activity watcher.
    activity = ActivityWatcher(args.activity_log)
    activity.seek_to_end()

    # ── Style ────────────────────────────────────────────────

    base_style = Style.from_dict({
        # Prompt — use ANSI names so terminal theme controls the shade.
        "prompt":           "ansiblue bold",
        "prompt.cont":      "ansigray bold",
        # Toolbar.
        "bottom-toolbar":   "reverse",
        "bottom-toolbar.text": "",
        "bottom-toolbar.key":  "ansiblue bold",
        # Completion menu — use reverse video for portability.
        "completion-menu.completion":             "bg:ansiblack ansiwhite",
        "completion-menu.completion.current":     "bg:ansiblue ansiwhite bold",
        "completion-menu.meta.completion":         "bg:ansiblack ansigray",
        "completion-menu.meta.completion.current": "bg:ansiblue ansigray",
    })

    # Keep syntax tokens on terminal defaults. Only style the UI chrome.
    style = base_style

    # ── State ────────────────────────────────────────────────

    class ReplState:
        exec_count = 0
        last_time_ms = 0
        var_count = 0
        is_busy = False
        in_multiline = False

    state = ReplState()

    try:
        overview = _tool(args.server, "look", {}, timeout=5)
        overview_text = _text(overview)
        match = re.search(r'(\d+) vars?', overview_text)
        if match:
            state.var_count = int(match.group(1))
    except Exception:
        pass

    # ── Completions ──────────────────────────────────────────

    class RCompleter(Completer):
        def get_completions(self, document, complete_event):
            text = document.text
            cursor = document.cursor_position
            try:
                result = _tool(args.server, "look",
                               {"code": text, "cursor": cursor}, timeout=5)
                out = _text(result)
                for line in out.split("\n"):
                    line = line.strip()
                    if line and line != "No completions.":
                        parts = line.split(None, 1)
                        if parts:
                            label = parts[0]
                            meta = parts[1] if len(parts) > 1 else ""
                            # Find word start.
                            start = cursor
                            while start > 0 and text[start - 1] not in " \t\n(,=[{":
                                start -= 1
                            yield Completion(label,
                                             start_position=start - cursor,
                                             display_meta=meta)
            except Exception:
                pass

    # ── Key bindings ─────────────────────────────────────────

    bindings = KeyBindings()

    @bindings.add("enter")
    def _(event):
        """Submit if expression is complete, else insert newline."""
        buf = event.current_buffer
        text = buf.text

        # If already multi-line and cursor not at end, just insert newline.
        if buf.cursor_position < len(text):
            buf.insert_text("\n")
            return

        if _is_complete_r(text):
            buf.validate_and_handle()
        else:
            state.in_multiline = True
            buf.insert_text("\n")
            # Auto-indent: count open braces/parens on previous lines.
            indent = _suggest_indent(text)
            if indent > 0:
                buf.insert_text("  " * indent)

    @bindings.add("escape", "enter")
    def _(event):
        """Alt+Enter: force newline (for manually entering multi-line code)."""
        state.in_multiline = True
        event.current_buffer.insert_text("\n")

    @bindings.add(Keys.F2)
    def _(event):
        """F2: show variable overview."""
        try:
            result = _tool(args.server, "look", {}, timeout=5)
            out = _text(result)
            if out:
                print(f"\n{out}\n")
        except Exception as e:
            print(f"\n[rat] error: {e}\n")

    @bindings.add("c-z")
    def _(event):
        """Ctrl-Z: leave the REPL."""
        event.app.exit(exception=EOFError)

    def _suggest_indent(code):
        """Count net open brackets for auto-indent level."""
        cleaned = re.sub(r'#.*$', '', code, flags=re.MULTILINE)
        cleaned = re.sub(r'"(?:[^"\\]|\\.)*"', '""', cleaned)
        cleaned = re.sub(r"'(?:[^'\\]|\\.)*'", "''", cleaned)
        level = 0
        for ch in cleaned:
            if ch in '({[':
                level += 1
            elif ch in ')}]':
                level = max(0, level - 1)
        return level

    # ── Prompt ───────────────────────────────────────────────

    def get_prompt():
        if state.in_multiline:
            return [("class:prompt.cont", "r+ ")]
        return [("class:prompt", "r> ")]

    # ── Toolbar ──────────────────────────────────────────────

    def get_toolbar():
        parts = []
        parts.append(("class:bottom-toolbar.key", " rat "))
        parts.append(("class:bottom-toolbar.text", f" {args.name} "))
        parts.append(("class:bottom-toolbar.text", "│ "))
        parts.append(("class:bottom-toolbar.text", f"{state.var_count} vars "))
        if state.last_time_ms > 0:
            parts.append(("class:bottom-toolbar.text", "│ "))
            if state.last_time_ms >= 1000:
                parts.append(("class:bottom-toolbar.text", f"{state.last_time_ms/1000:.1f}s "))
            else:
                parts.append(("class:bottom-toolbar.text", f"{state.last_time_ms}ms "))
        if state.is_busy:
            parts.append(("class:bottom-toolbar.text", "│ "))
            parts.append(("class:bottom-toolbar.key", "running… "))
        parts.append(("class:bottom-toolbar.text", "│ F2:vars │ Ctrl+D exit "))
        return parts

    # ── History ──────────────────────────────────────────────

    hist_dir = os.path.join(
        os.environ.get("XDG_CACHE_HOME", os.path.join(os.path.expanduser("~"), ".cache")),
        "rat", "kernels", args.name,
    )
    os.makedirs(hist_dir, exist_ok=True)
    hist_file = os.path.join(hist_dir, "r-history")
    replay_recent_activity = os.path.exists(hist_file) and os.path.getsize(hist_file) > 0
    history = FileHistory(hist_file)

    # ── Session ──────────────────────────────────────────────

    session = PromptSession(
        history=history,
        completer=RCompleter(),
        lexer=lexer,
        multiline=False,
        key_bindings=bindings,
        style=style,
        bottom_toolbar=get_toolbar,
        complete_while_typing=True,
        enable_open_in_editor=True,
    )

    server = args.server
    info = r.get("serverInfo", {})
    server_name = info.get("name", args.name)

    # ── Banner ───────────────────────────────────────────────

    # Query R version from the kernel.
    r_version = ""
    try:
        status_r = _tool(server, "status", {}, timeout=5)
        status_text = _text(status_r)
        for line in status_text.split("\n"):
            if "runtime_version" in line:
                r_version = line.split(":", 1)[1].strip()
                break
    except Exception:
        pass

    # ANSI 16 colors — terminal theme controls the actual shades.
    BLUE = "\033[34m"
    DIM_ = "\033[2m"
    BOLD = "\033[1m"
    R = "\033[0m"

    # Clear screen so prompt starts at top, toolbar at bottom.
    print("\033[2J\033[H", end="")
    print(f"  {BLUE}rat{R} {BOLD}{args.name}{R} — {r_version or 'R'} on {server}")
    print(f"  {DIM_}Shared namespace · other clients see your variables{R}")
    print(f"  {DIM_}Type {R}{BOLD}q(){R}{DIM_} or {R}{BOLD}Ctrl-D{R}{DIM_} to exit · {R}{BOLD}?name{R}{DIM_} to inspect · {R}{BOLD}Alt+Enter{R}{DIM_} for newline{R}")
    print()

    # ── ANSI helpers for output ──────────────────────────────

    RED = "\033[31m"
    DIMGRAY = "\033[2m"

    # ── REPL loop ────────────────────────────────────────────

    buffer = ""
    stop = threading.Event()
    try:
        with patch_stdout():
            # Start activity watcher INSIDE patch_stdout so
            # background prints don't corrupt the prompt.
            if replay_recent_activity:
                recent_entries = _read_recent_activity(args.activity_log)
                if recent_entries:
                    ptk_print(ANSI(_format_activity(recent_entries, client_name)))
            activity.seek_to_end()

            def watcher():
                while not stop.wait(0.5):
                    entries = activity.check()
                    if entries:
                        ptk_print(ANSI(_format_activity(entries, client_name)))

            thread = threading.Thread(target=watcher, daemon=True)
            thread.start()
            while True:
                state.in_multiline = bool(buffer)
                try:
                    line = session.prompt(get_prompt)
                except KeyboardInterrupt:
                    buffer = ""
                    state.in_multiline = False
                    print()
                    continue
                except EOFError:
                    break

                if buffer:
                    buffer += "\n" + line
                else:
                    buffer = line

                if not _is_complete_r(buffer):
                    continue

                code = buffer.strip()
                buffer = ""
                state.in_multiline = False

                if not code:
                    continue

                # ? inspection.
                if code.startswith("?"):
                    sym = code.lstrip("?").strip()
                    if sym:
                        try:
                            result = _tool(server, "look", {"at": sym}, timeout=10)
                            out = _text(result)
                            if out:
                                print(out)
                        except Exception as e:
                            print(f"{RED}[rat] error: {e}{R}")
                    continue

                if code in ("q()", "quit()", "exit", "exit()"):
                    break

                # Execute on the shared kernel.
                activity.mark_own(code)
                state.is_busy = True
                t0 = time.monotonic()
                try:
                    result = _tool(server, "run", {"code": code})
                    elapsed = time.monotonic() - t0
                    state.last_time_ms = int(elapsed * 1000)
                    state.is_busy = False
                    state.exec_count += 1

                    # Parse var count from hint if available.
                    raw_text = _text(result)
                    hint_match = _HINT_RE.search(raw_text)
                    if hint_match:
                        hint = hint_match.group()
                        var_match = re.search(r'(\d+) vars?', hint)
                        if var_match:
                            state.var_count = int(var_match.group(1))

                    out = _strip_hint(raw_text)
                    if out:
                        if _is_err(result):
                            # Color errors red.
                            ptk_print(ANSI(f"{RED}{out}{R}"))
                        else:
                            print(out)

                except Exception as e:
                    state.is_busy = False
                    ptk_print(ANSI(f"{RED}[rat] error: {e}{R}"))
                    ptk_print(ANSI(f"{DIMGRAY}[rat] kernel may have stopped. "
                          f"Ctrl-D to exit, then 'rat r' to reconnect.{R}"))

    finally:
        stop.set()
        print(f"\nDetached. Kernel still running. Reconnect: rat {args.name}")


def _basic_repl(args):
    """Fallback REPL without prompt_toolkit."""
    import readline  # noqa: for basic line editing
    server = args.server
    buffer = ""
    while True:
        prompt = "r> " if not buffer else "r+ "
        try:
            line = input(prompt)
        except (KeyboardInterrupt, EOFError):
            break
        if buffer:
            buffer += "\n" + line
        else:
            buffer = line
        if not _is_complete_r(buffer):
            continue
        code = buffer.strip()
        buffer = ""
        if not code:
            continue
        if code in ("q()", "quit()", "exit"):
            break
        try:
            result = _tool(server, "run", {"code": code})
            out = _strip_hint(_text(result))
            if out:
                print(out)
        except Exception as e:
            print(f"[rat] error: {e}")


if __name__ == "__main__":
    main()
