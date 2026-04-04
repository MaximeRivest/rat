"""
R frontend for rat — radian-style REPL routed through the shared MCP kernel.

Uses prompt_toolkit for the input experience (syntax highlighting, multi-line
editing, history) and routes execution + completions to the shared kernel.
Everything the human types goes through MCP — same namespace as Claude.

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


def _mcp_init(url):
    r = _mcp_call(url, "initialize", {
        "protocolVersion": "2025-03-26",
        "capabilities": {},
        "clientInfo": {"name": "rat-r-repl", "version": "0.1.0"},
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
    """Heuristic: check if an R expression looks complete.
    Counts brackets/parens/braces and checks for trailing operators."""
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

DIM = "\033[2m"
RESET = "\033[0m"


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


def _format_activity(entries):
    lines = []
    for e in entries:
        code = e.get("code", "")
        output = e.get("output", "")
        n = e.get("n", "?")
        ok = e.get("ok", True)
        mark = "✓" if ok else "✗"
        lines.append(f"{DIM}── rat: exec #{n} (another client) {mark} ──{RESET}")
        for cl in code.split("\n")[:5]:
            lines.append(f"{DIM}> {cl}{RESET}")
        if output:
            for ol in output.split("\n")[:5]:
                lines.append(f"{DIM}{ol}{RESET}")
        lines.append(f"{DIM}{'─' * 42}{RESET}")
    return "\n".join(lines)


# ── prompt_toolkit REPL ──────────────────────────────────────

def main():
    parser = argparse.ArgumentParser(description="R frontend for rat")
    parser.add_argument("--server", required=True, help="MCP server URL")
    parser.add_argument("--name", default="r", help="Kernel name")
    parser.add_argument("--activity-log", default=None, help="Activity log path")
    args = parser.parse_args()

    # Connect to kernel.
    r = _mcp_init(args.server)
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
    except ImportError:
        print("prompt_toolkit not installed. Install: pip install prompt-toolkit", file=sys.stderr)
        print("Falling back to basic REPL.", file=sys.stderr)
        _basic_repl(args)
        return

    # Try to get R syntax highlighting via pygments.
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

    # MCP-backed completer.
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
                        parts = line.split()
                        if parts:
                            label = parts[0]
                            # Find word start.
                            start = cursor
                            while start > 0 and text[start - 1] not in " \t\n(,=[{":
                                start -= 1
                            yield Completion(label, start_position=start - cursor)
            except Exception:
                pass

    # History file.
    hist_dir = os.path.join(
        os.environ.get("XDG_CACHE_HOME", os.path.join(os.path.expanduser("~"), ".cache")),
        "rat", "kernels", args.name,
    )
    os.makedirs(hist_dir, exist_ok=True)
    history = FileHistory(os.path.join(hist_dir, "r-history"))

    session = PromptSession(
        history=history,
        completer=RCompleter(),
        lexer=lexer,
        multiline=False,  # We handle multi-line ourselves.
    )

    server = args.server
    info = r.get("serverInfo", {})
    server_name = info.get("name", args.name)

    print(f"rat {args.name} | {server_name} @ {server}")
    print("Shared namespace — other clients see your variables.")
    print()

    # Background activity watcher.
    stop = threading.Event()

    def watcher():
        while not stop.wait(0.5):
            entries = activity.check()
            if entries:
                print(_format_activity(entries))

    thread = threading.Thread(target=watcher, daemon=True)
    thread.start()

    buffer = ""
    try:
        with patch_stdout():
            while True:
                prompt = "r$> " if not buffer else "r+> "
                try:
                    line = session.prompt(prompt)
                except KeyboardInterrupt:
                    buffer = ""
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

                if not code:
                    continue

                if code in ("q()", "quit()", "exit", "exit()"):
                    break

                # Execute on the shared kernel.
                activity.mark_own(code)
                try:
                    result = _tool(server, "run", {"code": code})
                    out = _strip_hint(_text(result))
                    if out:
                        print(out)
                    if _is_err(result):
                        pass  # Error already printed in output.
                except Exception as e:
                    print(f"[rat] error: {e}")
                    print("[rat] kernel may have stopped. Ctrl-D to exit, then 'rat r' to reconnect.")

    finally:
        stop.set()


def _basic_repl(args):
    """Fallback REPL without prompt_toolkit."""
    import readline  # noqa: for basic line editing
    server = args.server
    buffer = ""
    while True:
        prompt = "r$> " if not buffer else "r+> "
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
