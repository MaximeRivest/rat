"""
Shared prompt_toolkit frontend for rat.

Works for any language — R, Julia, JavaScript, or custom runtimes.
Routes all execution and completions through the shared MCP kernel.
Everything the human types goes through MCP — same namespace as Claude.

Usage (called by Go binary):
    python3 frontend.py --server http://127.0.0.1:8720/mcp --name r@rat --lang r
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


def _make_client_name(lang):
    try:
        tty = os.path.basename(os.ttyname(sys.stdin.fileno()))
    except OSError:
        tty = f"pid-{os.getpid()}"
    return f"rat-{lang}-repl@{tty}"


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


# ── Language-specific configuration ──────────────────────────

# Pygments lexer classes by language.
_LEXER_MAP = {
    "r":  ("pygments.lexers", "SLexer"),
    "jl": ("pygments.lexers", "JuliaLexer"),
    "js": ("pygments.lexers", "JavaScriptLexer"),
}

# Prompt strings by language.
_PROMPT_MAP = {
    "r":  ("r> ", "r+ "),
    "jl": ("jl> ", "jl> "),
    "js": ("js> ", "js> "),
}

# Display names for the banner.
_DISPLAY_MAP = {
    "r":  "R",
    "jl": "Julia",
    "js": "JavaScript",
    "py": "Python",
    "sh": "Shell",
    "pi": "pi",
}

# Languages that support ? inspection.
_INSPECT_LANGS = {"r", "jl", "py"}


def _get_lexer(lang):
    """Try to load a pygments lexer for the language."""
    if lang not in _LEXER_MAP:
        return None
    try:
        from prompt_toolkit.lexers import PygmentsLexer
        mod_name, cls_name = _LEXER_MAP[lang]
        mod = __import__(mod_name, fromlist=[cls_name])
        cls = getattr(mod, cls_name)
        return PygmentsLexer(cls)
    except (ImportError, AttributeError):
        return None


def _get_prompts(lang):
    """Return (primary_prompt, continuation_prompt) for a language."""
    return _PROMPT_MAP.get(lang, (f"{lang}> ", f"{lang}> "))


def _get_display(lang):
    """Human-readable language name."""
    return _DISPLAY_MAP.get(lang, lang)


# ── Expression completeness detection ────────────────────────

def _is_complete_r(code):
    """Heuristic: check if an R expression looks complete."""
    code = code.strip()
    if not code:
        return True
    cleaned = re.sub(r'#.*$', '', code, flags=re.MULTILINE)
    cleaned = re.sub(r'"(?:[^"\\]|\\.)*"', '""', cleaned)
    cleaned = re.sub(r"'(?:[^'\\]|\\.)*'", "''", cleaned)
    opens = cleaned.count('(') + cleaned.count('[') + cleaned.count('{')
    closes = cleaned.count(')') + cleaned.count(']') + cleaned.count('}')
    if opens > closes:
        return False
    last_line = code.rstrip().split('\n')[-1].rstrip()
    if last_line and last_line[-1] in ('+', '-', '*', '/', '|', '&', ',', '=', '~', '\\'):
        return False
    if last_line.endswith('%>%') or last_line.endswith('|>'):
        return False
    return True


def _is_complete_bracket(code):
    """Generic bracket-based completeness for JS, Julia, etc."""
    code = code.strip()
    if not code:
        return True
    # Remove strings and comments.
    cleaned = re.sub(r'#.*$', '', code, flags=re.MULTILINE)
    cleaned = re.sub(r'//.*$', '', cleaned, flags=re.MULTILINE)
    cleaned = re.sub(r'"(?:[^"\\]|\\.)*"', '""', cleaned)
    cleaned = re.sub(r"'(?:[^'\\]|\\.)*'", "''", cleaned)
    cleaned = re.sub(r'`(?:[^`\\]|\\.)*`', '``', cleaned)
    opens = cleaned.count('(') + cleaned.count('[') + cleaned.count('{')
    closes = cleaned.count(')') + cleaned.count(']') + cleaned.count('}')
    return opens <= closes


def _is_complete(lang, code):
    """Check if code is a complete expression for the given language."""
    if lang == "r":
        return _is_complete_r(code)
    return _is_complete_bracket(code)


# ── Activity watcher ─────────────────────────────────────────

_BAR     = "\033[2;35m"
_LABEL   = "\033[2;37m"
_CODE    = "\033[2m"
_OUTPUT  = "\033[2;33m"
_ERR_DIM = "\033[2;31m"
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
    # Strip the repl@tty prefix to show just the tty.
    for prefix in ("rat-r-repl@", "rat-py-repl@", "rat-jl-repl@", "rat-js-repl@"):
        if client.startswith(prefix):
            return f"repl {client.split('@', 1)[1]}"
    return client


def _format_event(entry):
    """Format a kernel-pushed event (message, alert, etc.)."""
    event_type = entry.get("event", "")
    data = entry.get("data", {})
    if event_type == "message":
        sender = data.get("from", "")
        text = data.get("text", "")
        channel = data.get("channel", "")
        header = sender
        if channel:
            header += f" @{channel}"
        lines = [f"{_BAR}▍\033[1m{header}\033[0m"]
        for tl in text.split("\n"):
            lines.append(f"{_BAR}▍  {tl}")
        lines.append("")
        return "\n".join(lines)
    elif event_type == "error":
        msg = data.get("msg", "")
        return f"{_BAR}▍{_ERR_DIM}✗ {msg}{_R}\n"
    elif event_type == "alert":
        msg = data.get("msg", "")
        level = data.get("level", "")
        icon = "🔴" if level == "error" else "⚠️" if level == "warning" else "🔔"
        return f"{_BAR}▍ {icon} {msg}\n"
    else:
        text = data.get("text", data.get("msg", ""))
        if not text:
            text = json.dumps(data)
        return f"{_BAR}▍\033[2m[{event_type}] {text}\033[0m\n"


def _format_activity(entries, own_client=""):
    lines = []
    for e in entries:
        # Kernel-pushed events (message, alert, etc.)
        if e.get("event"):
            lines.append(_format_event(e))
            continue
        # Execution records from other clients.
        code = e.get("code", "")
        output = e.get("output", "")
        ok = e.get("ok", True)
        mark = "✓" if ok else "✗"
        mark_color = _LABEL if ok else _ERR_DIM
        label = _activity_label(e, own_client)
        code_lines = [cl for cl in code.split("\n") if cl.strip()][:6]
        out_lines = [ol for ol in output.split("\n") if ol.strip()][:6]
        lines.append(f"{_BAR}▍{mark_color} {label} {mark}{_R}")
        for cl in code_lines:
            lines.append(f"{_BAR}▍ {_CODE}{cl}{_R}")
        if out_lines:
            for ol in out_lines:
                lines.append(f"{_BAR}▍  {_OUTPUT}{ol}{_R}")
        lines.append("")
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
    parser = argparse.ArgumentParser(description="Shared rat frontend")
    parser.add_argument("--server", required=True, help="MCP server URL")
    parser.add_argument("--name", default="kernel", help="Kernel name")
    parser.add_argument("--lang", default="", help="Language (r, jl, js, ...)")
    parser.add_argument("--activity-log", default=None, help="Activity log path")
    args = parser.parse_args()

    lang = args.lang

    # Ctrl+Z detaches with exit code 2 → Go repl returns to shell.
    import signal as _signal
    def _sigtstp_handler(signum, frame):
        print(f"\nDetached. Kernel still running. Reconnect: rat {args.name}")
        sys.exit(2)
    _signal.signal(_signal.SIGTSTP, _sigtstp_handler)

    # Connect to kernel.
    client_name = _make_client_name(lang)
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
        from prompt_toolkit.formatted_text import ANSI
        from prompt_toolkit import print_formatted_text as ptk_print
        from prompt_toolkit.key_binding import KeyBindings
        from prompt_toolkit.styles import Style
        from prompt_toolkit.keys import Keys
    except ImportError:
        print("prompt_toolkit not installed. Install: pip install prompt-toolkit", file=sys.stderr)
        print("Falling back to basic REPL.", file=sys.stderr)
        _basic_repl(args)
        return

    lexer = _get_lexer(lang)
    primary_prompt, cont_prompt = _get_prompts(lang)
    display_name = _get_display(lang)

    # Activity watcher.
    activity = ActivityWatcher(args.activity_log)
    activity.seek_to_end()

    # ── Style ────────────────────────────────────────────────

    style = Style.from_dict({
        "prompt":           "ansiblue bold",
        "prompt.cont":      "ansigray bold",
        "bottom-toolbar":           "bg:#262626 #d0d0d0 noreverse",
        "bottom-toolbar.text":      "noreverse",
        "rat.label":                "fg:#00d7ff bold noreverse",
        "rat.bar":                  "fg:#d0d0d0 noreverse",
        "rat.sep":                  "fg:#8a8a8a noreverse",
        "rat.dot":                  "fg:#8a8a8a noreverse",
        "rat.green":                "fg:#00d75f noreverse",
        "rat.tab.active":           "fg:#00d7ff bold noreverse",
        "rat.tab.inactive":         "fg:#8a8a8a noreverse",
        "completion-menu.completion":             "bg:ansiblack ansiwhite",
        "completion-menu.completion.current":     "bg:ansiblue ansiwhite bold",
        "completion-menu.meta.completion":         "bg:ansiblack ansigray",
        "completion-menu.meta.completion.current": "bg:ansiblue ansigray",
    })

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

    class LangCompleter(Completer):
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
        if buf.cursor_position < len(text):
            buf.insert_text("\n")
            return
        if _is_complete(lang, text):
            buf.validate_and_handle()
        else:
            state.in_multiline = True
            buf.insert_text("\n")
            indent = _suggest_indent(text)
            if indent > 0:
                buf.insert_text("  " * indent)

    @bindings.add("escape", "enter")
    def _(event):
        """Alt+Enter: force newline."""
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
        """Ctrl-Z: leave the REPL (skip picker)."""
        event.app.exit(result="__rat_quit__")

    # ── Tab cycling: Alt + vim keys ──────────────────────────
    # Alt-l / Alt-h: next/prev instance of the same language.
    # Alt-j / Alt-k: next/prev language in the same project.
    # These exec into `rat <target>`, replacing the current process.

    def _cycle(lst, current, direction):
        """Return next/prev item in a list, wrapping around."""
        if len(lst) <= 1:
            return current
        try:
            idx = lst.index(current)
        except ValueError:
            return lst[0]
        return lst[(idx + direction) % len(lst)]

    def _switch_instance(event, direction):
        """Switch to next/prev instance."""
        target = _cycle(siblings, instance, direction)
        if target == instance:
            return
        event.app.exit(result=f"__rat_switch__ {lang} {target}")

    def _switch_lang(event, direction):
        """Switch to next/prev language."""
        target = _cycle(project_langs, lang, direction)
        if target == lang:
            return
        event.app.exit(result=f"__rat_switch__ {target}")

    @bindings.add("escape", "l")  # Alt-l: next instance
    def _(event):
        _switch_instance(event, 1)

    @bindings.add("escape", "h")  # Alt-h: prev instance
    def _(event):
        _switch_instance(event, -1)

    @bindings.add("escape", "j")  # Alt-j: next language
    def _(event):
        _switch_lang(event, 1)

    @bindings.add("escape", "k")  # Alt-k: prev language
    def _(event):
        _switch_lang(event, -1)

    def _suggest_indent(code):
        """Count net open brackets for auto-indent level."""
        cleaned = re.sub(r'#.*$', '', code, flags=re.MULTILINE)
        cleaned = re.sub(r'//.*$', '', cleaned, flags=re.MULTILINE)
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
            return [("class:prompt.cont", cont_prompt)]
        return [("class:prompt", primary_prompt)]

    # ── Toolbar ──────────────────────────────────────────────

    def get_toolbar():
        import shutil
        cols = shutil.get_terminal_size((80, 24)).columns

        left_parts = []
        left_parts.append(("class:rat.label", " rat "))
        left_parts.append(("class:rat.bar", f" {display_name} "))
        left_parts.append(("class:rat.sep", "│"))
        left_parts.append(("class:rat.bar", f" {args.name} "))

        right_segs = []
        right_segs.append(("class:rat.green", "shared"))
        right_segs.append(("class:rat.bar", f"{state.var_count} vars"))
        if state.last_time_ms > 0:
            if state.last_time_ms >= 1000:
                right_segs.append(("class:rat.bar", f"{state.last_time_ms/1000:.1f}s"))
            else:
                right_segs.append(("class:rat.bar", f"{state.last_time_ms}ms"))
        if state.is_busy:
            right_segs.append(("class:rat.label", "running…"))
        right_segs.append(("class:rat.bar", "F2:vars"))
        right_segs.append(("class:rat.bar", "Ctrl+D exit"))

        right_parts = []
        for i, seg in enumerate(right_segs):
            if i > 0:
                right_parts.append(("class:rat.dot", " • "))
            right_parts.append(seg)

        left_len = sum(len(t) for _, t in left_parts)
        right_len = sum(len(t) for _, t in right_parts)
        pad = max(1, cols - left_len - right_len)

        return left_parts + [("class:rat.bar", " " * pad)] + right_parts

    # ── History ──────────────────────────────────────────────

    hist_dir = os.path.join(
        os.environ.get("XDG_CACHE_HOME", os.path.join(os.path.expanduser("~"), ".cache")),
        "rat", "kernels", args.name,
    )
    os.makedirs(hist_dir, exist_ok=True)
    hist_file = os.path.join(hist_dir, f"{lang}-history")
    replay_recent_activity = os.path.exists(hist_file) and os.path.getsize(hist_file) > 0
    history = FileHistory(hist_file)

    # ── Session ──────────────────────────────────────────────

    session = PromptSession(
        history=history,
        completer=LangCompleter(),
        lexer=lexer,
        multiline=False,
        key_bindings=bindings,
        style=style,
        bottom_toolbar=get_toolbar,
        complete_while_typing=True,
        enable_open_in_editor=True,
    )

    info = r.get("serverInfo", {})

    # ── Banner ───────────────────────────────────────────────

    runtime_version = ""
    try:
        status_r = _tool(args.server, "status", {}, timeout=5)
        status_text = _text(status_r)
        for line in status_text.split("\n"):
            if "runtime_version" in line:
                runtime_version = line.split(":", 1)[1].strip()
                break
    except Exception:
        pass

    BLUE = "\033[34m"
    DIM_ = "\033[2m"
    BOLD = "\033[1m"
    R = "\033[0m"

    print("\033[2J\033[H", end="")
    version_label = runtime_version or display_name
    print(f"  {BLUE}rat{R} {BOLD}{args.name}{R} — {version_label} on {args.server}")
    print(f"  {DIM_}Shared namespace · other clients see your variables{R}")
    hints = [f"{BOLD}Ctrl-D{R}{DIM_} exit"]
    if lang in _INSPECT_LANGS:
        hints.insert(0, f"{BOLD}?name{R}{DIM_} inspect")
    hints.insert(len(hints) - 1, f"{BOLD}Alt+Enter{R}{DIM_} newline")
    print(f"  {DIM_}" + " · ".join(hints) + f"{R}")
    print()

    # ── REPL loop ────────────────────────────────────────────

    RED = "\033[31m"
    DIMGRAY = "\033[2m"

    buffer = ""
    stop = threading.Event()
    try:
        with patch_stdout():
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

                # Handle Ctrl-Z (quit without picker).
                if isinstance(line, str) and line == "__rat_quit__":
                    stop.set()
                    print(f"\nDetached. Kernel still running. Reconnect: rat {args.name}")
                    sys.exit(2)

                if buffer:
                    buffer += "\n" + line
                else:
                    buffer = line

                if not _is_complete(lang, buffer):
                    continue

                code = buffer.strip()
                buffer = ""
                state.in_multiline = False

                if not code:
                    continue

                # ? inspection.
                if lang in _INSPECT_LANGS and code.startswith("?"):
                    sym = code.lstrip("?").strip()
                    if sym:
                        try:
                            result = _tool(args.server, "look", {"at": sym}, timeout=10)
                            out = _text(result)
                            if out:
                                print(out)
                        except Exception as e:
                            print(f"{RED}[rat] error: {e}{R}")
                    continue

                if code in ("q()", "quit()", "exit", "exit()", ":q"):
                    stop.set()
                    print(f"\nDetached. Kernel still running. Reconnect: rat {args.name}")
                    sys.exit(2)

                # Execute on the shared kernel.
                activity.mark_own(code)
                state.is_busy = True
                t0 = time.monotonic()
                try:
                    result = _tool(args.server, "run", {"code": code})
                    elapsed = time.monotonic() - t0
                    state.last_time_ms = int(elapsed * 1000)
                    state.is_busy = False
                    state.exec_count += 1

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
                            ptk_print(ANSI(f"{RED}{out}{R}"))
                        else:
                            print(out)

                except Exception as e:
                    state.is_busy = False
                    ptk_print(ANSI(f"{RED}[rat] error: {e}{R}"))
                    ptk_print(ANSI(f"{DIMGRAY}[rat] kernel may have stopped. "
                          f"Ctrl-D to exit, then 'rat {lang}' to reconnect.{R}"))

    finally:
        stop.set()


def _basic_repl(args):
    """Fallback REPL without prompt_toolkit."""
    lang = args.lang
    prompt_str, cont_str = _get_prompts(lang)
    try:
        import readline  # noqa: for basic line editing
    except ImportError:
        pass
    client_name = _make_client_name(lang)
    r = _mcp_init(args.server, client_name)
    if "_error" in r:
        print(f"Cannot connect to kernel at {args.server}", file=sys.stderr)
        sys.exit(1)
    server = args.server
    buffer = ""
    while True:
        prompt = prompt_str if not buffer else cont_str
        try:
            line = input(prompt)
        except (KeyboardInterrupt, EOFError):
            break
        if buffer:
            buffer += "\n" + line
        else:
            buffer = line
        if not _is_complete(lang, buffer):
            continue
        code = buffer.strip()
        buffer = ""
        if not code:
            continue
        if code in ("q()", "quit()", "exit", "exit()", ":q"):
            break
        try:
            result = _tool(server, "run", {"code": code})
            out = _strip_hint(_text(result))
            if out:
                print(out)
        except Exception as e:
            print(f"[rat] error: {e}")
    print(f"\nDetached. Kernel still running. Reconnect: rat {args.name}")


if __name__ == "__main__":
    main()
