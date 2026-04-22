"""
IPython frontend for rat py.

Routes execution, completions, and %magics to the shared MCP kernel.
Everything else stays native IPython: history, multiline editing,
syntax highlighting, !shell.

Usage (called by Go binary):
    python frontend.py --server http://127.0.0.1:8717/mcp --name py
"""

import argparse
import http.client
import json
import os
import re
import sys
import threading
import urllib.parse


# ── Minimal MCP client (stdlib only, no dependencies) ────────

_MSG_ID = 0
_SESSION_ID = None


def _mcp_call(url, method, params=None, timeout=300):
    """JSON-RPC call to the MCP server. Returns the result dict.

    Non-streaming: waits for the whole response body, then scans for
    the data frame with our id. Use _mcp_call_streaming for run()
    where you want notifications (rat/output, elicitation/create)
    delivered as they arrive.
    """
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


def _mcp_call_streaming(url, method, params=None, timeout=600,
                       on_output=None, on_elicit=None, on_input_request=None):
    """Like _mcp_call but reads SSE line-by-line and delivers
    intermediate frames to callbacks as they arrive.

    Callbacks:
      on_output(text)            — rat/output notification
      on_elicit(req) -> str|None — elicitation/create request; return
                                    text to accept, or None to cancel.
      on_input_request()         — legacy rat/input_request (when
                                    elicitation isn't supported)

    Returns the final result dict for the original request id.
    """
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

    result_holder = {"result": None, "error": None}
    conn = None
    try:
        conn = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=timeout)
        conn.request("POST", parsed.path, body, headers)
        resp = conn.getresponse()
        sid = resp.getheader("Mcp-Session-Id")
        if sid:
            _SESSION_ID = sid

        ct = resp.getheader("Content-Type", "")
        if "text/event-stream" not in ct:
            # Plain JSON — no streaming possible, just parse body.
            raw = resp.read().decode()
            obj = json.loads(raw)
            if "error" in obj:
                return {"_error": obj["error"]}
            return obj.get("result", {})

        # Read SSE frames line-by-line. Each frame is a "data: ..."
        # line terminated by a blank line.
        fp = resp.fp  # underlying file-like, supports readline()
        buf = []
        while True:
            line = fp.readline()
            if not line:
                break
            line = line.decode("utf-8", errors="replace").rstrip("\r\n")
            if line == "":
                # Frame terminator.
                if not buf:
                    continue
                data = "\n".join(s[6:] for s in buf if s.startswith("data: "))
                buf = []
                if not data:
                    continue
                try:
                    obj = json.loads(data)
                except json.JSONDecodeError:
                    continue
                _handle_sse_frame(obj, url, mid,
                                  on_output, on_elicit, on_input_request,
                                  result_holder)
                if result_holder["result"] is not None or result_holder["error"] is not None:
                    break
                continue
            buf.append(line)
    except Exception as e:
        return {"_error": str(e)}
    finally:
        if conn is not None:
            try:
                conn.close()
            except Exception:
                pass

    if result_holder["error"] is not None:
        return {"_error": result_holder["error"]}
    return result_holder["result"] or {}


def _handle_sse_frame(obj, url, mid, on_output, on_elicit, on_input_request, holder):
    """Dispatch a single parsed JSON-RPC frame."""
    method = obj.get("method")
    if obj.get("id") == mid:
        # Our final response.
        if "error" in obj:
            holder["error"] = obj["error"]
        else:
            holder["result"] = obj.get("result", {})
        return
    if method == "rat/output":
        text = (obj.get("params") or {}).get("text", "")
        if text and on_output is not None:
            try:
                on_output(text)
            except Exception:
                pass
        return
    if method == "rat/input_request":
        if on_input_request is not None:
            try:
                on_input_request()
            except Exception:
                pass
        return
    # Server-initiated request (elicitation/create) — has both id and
    # method. Must respond with the same id.
    if method == "elicitation/create" and "id" in obj:
        req_id = obj["id"]
        content = None
        action = "cancel"
        if on_elicit is not None:
            try:
                text = on_elicit(obj.get("params") or {})
            except Exception:
                text = None
            if text is not None:
                action = "accept"
                content = {"text": text}
        # Post the reply back on a new HTTP call.
        reply = {
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {"action": action, "content": content},
        }
        _mcp_post_raw(url, reply)
        return


def _mcp_post_raw(url, payload):
    """Fire-and-forget POST used to reply to server-initiated requests."""
    global _SESSION_ID
    body = json.dumps(payload).encode()
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
        conn.getresponse().read()
        conn.close()
    except Exception:
        pass


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


def _make_client_name(prefix):
    try:
        tty = os.path.basename(os.ttyname(sys.stdin.fileno()))
    except (OSError, AttributeError):
        tty = f"pid-{os.getpid()}"
    return f"{prefix}@{tty}"



def _mcp_init(url, client_name):
    """Initialize the MCP session. Declares elicitation support so
    the server will route input() prompts back to us via
    elicitation/create instead of silently hanging."""
    r = _mcp_call(url, "initialize", {
        "protocolVersion": "2025-03-26",
        "capabilities": {"elicitation": {}},
        "clientInfo": {"name": client_name, "version": "0.1.0"},
    })
    if "_error" not in r:
        _mcp_notify(url, "notifications/initialized")
    return r


def _tool(url, name, args, timeout=300):
    """Call an MCP tool."""
    return _mcp_call(url, "tools/call", {"name": name, "arguments": args}, timeout=timeout)


def _tool_streaming(url, name, args, timeout=3600,
                    on_output=None, on_elicit=None, on_input_request=None):
    """Call an MCP tool and stream rat/output notifications +
    respond to elicitation requests live. Used for `run` so code
    output appears in the REPL as it's produced."""
    return _mcp_call_streaming(
        url, "tools/call", {"name": name, "arguments": args},
        timeout=timeout, on_output=on_output, on_elicit=on_elicit,
        on_input_request=on_input_request,
    )


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


# ── Event subscriber (live fanout from the MCP server) ──────

class EventSubscriber:
    """Opens a persistent GET /mcp SSE subscription and dispatches
    ``rat/event`` notifications to a callback.

    Events are emitted by the Go event bus for every run on the
    kernel, regardless of which client triggered it. This is how the
    REPL sees VS Code / Claude / other terminals' code streaming in
    real time.

    Implementation notes:
      - A socket read timeout of _IDLE_SOCK_TIMEOUT keeps ``readline``
        from blocking forever, so ``stop()`` can be observed within a
        bounded window.
      - ``stop()`` shuts down the underlying socket so a pending
        ``readline`` returns immediately. On platforms where that
        isn't enough, the socket timeout eventually fires.
      - The thread is daemon, so Python exits cleanly even if a
        subscriber is still running.
    """

    _IDLE_SOCK_TIMEOUT = 15.0  # seconds

    def __init__(self, url, session_id, on_event, on_stop=None):
        self._url = url
        self._session_id = session_id
        self._on_event = on_event
        self._on_stop = on_stop
        self._stop = threading.Event()
        self._thread = None
        self._conn = None
        self._conn_lock = threading.Lock()

    def start(self):
        if self._thread is not None:
            return
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()

    def stop(self, join_timeout=2.0):
        self._stop.set()
        # Close the live socket so a pending readline returns ASAP.
        with self._conn_lock:
            conn = self._conn
            self._conn = None
        if conn is not None:
            try:
                sock = getattr(conn, "sock", None)
                if sock is not None:
                    import socket as _sock
                    try:
                        sock.shutdown(_sock.SHUT_RDWR)
                    except OSError:
                        pass
                conn.close()
            except Exception:
                pass
        if self._thread is not None and join_timeout is not None:
            self._thread.join(timeout=join_timeout)

    def _run(self):
        # Dropped subscription? Retry with exponential backoff so
        # live events resume after a server hiccup.
        backoff = 0.5
        while not self._stop.is_set():
            try:
                self._listen_once()
                backoff = 0.5
            except Exception:
                if self._stop.is_set():
                    return
                time.sleep(backoff)
                backoff = min(backoff * 2, 10.0)
        if callable(self._on_stop):
            try:
                self._on_stop()
            except Exception:
                pass

    def _listen_once(self):
        parsed = urllib.parse.urlparse(self._url)
        conn = http.client.HTTPConnection(
            parsed.hostname, parsed.port,
            timeout=self._IDLE_SOCK_TIMEOUT,
        )
        with self._conn_lock:
            if self._stop.is_set():
                try: conn.close()
                except Exception: pass
                return
            self._conn = conn
        try:
            headers = {
                "Accept": "text/event-stream",
                "Mcp-Session-Id": self._session_id,
            }
            conn.request("GET", parsed.path, None, headers)
            resp = conn.getresponse()
            if resp.status != 200:
                try: resp.read()
                except Exception: pass
                return
            buf = []
            while not self._stop.is_set():
                try:
                    line = resp.fp.readline()
                except (TimeoutError, OSError):
                    # Either the socket timeout fired or stop()
                    # shut the socket down. Loop to re-check _stop.
                    if self._stop.is_set():
                        return
                    continue
                if not line:
                    return  # server closed the stream; _run will retry
                line = line.decode("utf-8", errors="replace").rstrip("\r\n")
                if line == "":
                    data = "\n".join(s[6:] for s in buf if s.startswith("data: "))
                    buf = []
                    if not data:
                        continue
                    try:
                        obj = json.loads(data)
                    except json.JSONDecodeError:
                        continue
                    if obj.get("method") == "rat/event":
                        try:
                            self._on_event(obj.get("params") or {})
                        except Exception:
                            pass
                else:
                    buf.append(line)
        finally:
            with self._conn_lock:
                if self._conn is conn:
                    self._conn = None
            try: conn.close()
            except Exception: pass


# ── Missing stdlib shim (http stream needs time) ────────────

import time  # noqa: E402 (intentionally late, only for the subscriber)


# Activity colors — use ANSI 16 colors so terminal themes control readability.
_BAR     = "\033[2;35m"   # dim magenta for the border
_LABEL   = "\033[2;37m"   # dim white for the label
_CODE    = "\033[2m"      # dim default for code
_OUTPUT  = "\033[2;33m"   # dim yellow for output
_ERR_DIM = "\033[2;31m"   # dim red for failed marker
_R       = "\033[0m"


def _activity_label(entry, own_client=""):
    client = (entry.get("client") or entry.get("caller") or "").strip()
    if not client:
        return "activity"
    if own_client and client == own_client:
        return "you"
    if client.startswith("rat-py-repl@"):
        return f"repl {client.split('@', 1)[1]}"
    if client.startswith("rat-r-repl@"):
        return f"R repl {client.split('@', 1)[1]}"
    return client


def _format_live_event(kind, caller, own_client="",
                      code="", output="", error="",
                      max_code=0, max_output=100):
    """Render one live-event banner.

    kind:    run_started | run_output | run_waiting | run_ended
    output:  accumulated stdout so far (for run_output/run_ended)
    code:    code being executed (for run_started)
    error:   error text (for run_ended with ok=False)
    """
    label = _activity_label({"caller": caller}, own_client)
    if kind == "run_started":
        mark_color = _LABEL
        mark = "▸"  # running marker
        raw_code = [cl for cl in code.split("\n") if cl.strip()]
        code_lines, code_dropped = _cap_lines(raw_code, max_code)
        lines = [f"{_BAR}▍{mark_color} {label} {mark}{_R}"]
        for cl in code_lines:
            lines.append(f"{_BAR}▍ {_CODE}{cl}{_R}")
        if code_dropped:
            lines.append(f"{_BAR}▍ {_CODE}… {code_dropped} more lines{_R}")
        return "\n".join(lines)
    if kind == "run_output":
        raw_out = [ol for ol in output.split("\n") if ol.strip()]
        if not raw_out:
            return ""
        # Only render new chunk lines, not the whole accumulated output.
        return "\n".join(f"{_BAR}▍  {_OUTPUT}{ol}{_R}" for ol in raw_out)
    if kind == "run_waiting":
        return f"{_BAR}▍  {_OUTPUT}✋ waiting for input…{_R}"
    if kind == "run_ended":
        ok = error == ""
        mark = "✓" if ok else "✗"
        mark_color = _LABEL if ok else _ERR_DIM
        footer = f"{_BAR}▍{mark_color} {label} {mark}{_R}"
        if not ok and error:
            err_lines = [el for el in error.split("\n") if el.strip()][:4]
            parts = [footer]
            for el in err_lines:
                parts.append(f"{_BAR}▍  {_ERR_DIM}{el}{_R}")
            return "\n".join(parts)
        return footer
    return ""



def _cap_lines(lines, limit):
    """Cap a list of lines to `limit`. 0 means unlimited.
    Returns (capped_lines, dropped_count)."""
    if limit <= 0 or len(lines) <= limit:
        return lines, 0
    return lines[:limit], len(lines) - limit


def _format_activity(entries, own_client="", max_code=0, max_output=100):
    """Format activity entries for display in the REPL.
    max_code=0 / max_output=0 mean unlimited."""
    lines = []
    for e in entries:
        code = e.get("code", "")
        output = e.get("output", "")
        ok = e.get("ok", True)
        mark = "✓" if ok else "✗"
        mark_color = _LABEL if ok else _ERR_DIM
        label = _activity_label(e, own_client)

        # Filter blank code/output lines then cap.
        raw_code = [cl for cl in code.split("\n") if cl.strip()]
        raw_out = [ol for ol in output.split("\n") if ol.strip()]
        code_lines, code_dropped = _cap_lines(raw_code, max_code)
        out_lines, out_dropped = _cap_lines(raw_out, max_output)

        # Header: thin bar + label.
        lines.append(f"{_BAR}\u258d{mark_color} {label} {mark}{_R}")
        for cl in code_lines:
            lines.append(f"{_BAR}\u258d {_CODE}{cl}{_R}")
        if code_dropped:
            lines.append(f"{_BAR}\u258d {_CODE}… {code_dropped} more lines{_R}")
        if out_lines:
            for ol in out_lines:
                lines.append(f"{_BAR}\u258d  {_OUTPUT}{ol}{_R}")
            if out_dropped:
                lines.append(f"{_BAR}\u258d  {_OUTPUT}… {out_dropped} more lines{_R}")
        elif out_dropped:
            lines.append(f"{_BAR}\u258d  {_OUTPUT}… {out_dropped} more lines{_R}")
        lines.append("")  # blank line between entries
    return "\n".join(lines)


# ── IPython shell with MCP backend ──────────────────────────

def _make_shell(server_url, kernel_name="py", activity_log=None, cwd="", venv="", py_version="", client_name="",
                activity_max_code_lines=0, activity_max_output_lines=100,
                history_seed_from_runtime=True, history_seed_limit=0):
    """Create an IPython shell class that routes execution to MCP."""
    from IPython.terminal.interactiveshell import TerminalInteractiveShell
    from IPython.core.interactiveshell import ExecutionResult

    class RatShell(TerminalInteractiveShell):
        """Real IPython. Only execution and completions go through MCP."""

        def __init__(self, *args, **kwargs):
            super().__init__(*args, **kwargs)
            self._server = server_url
            self._kernel_name = kernel_name
            import os as _os
            self._kernel_cwd = cwd.replace(_os.path.expanduser('~'), '~') if cwd else ''
            self._py_version = py_version
            self._venv = _os.path.basename(venv) if venv else ''
            self._mcp_ok = False
            self._client_name = client_name
            self._activity_max_code = activity_max_code_lines
            self._activity_max_output = activity_max_output_lines
            self._history_seed_from_runtime = history_seed_from_runtime
            self._history_seed_limit = history_seed_limit
            self._activity = ActivityWatcher(activity_log)
            self._activity.seek_to_end()  # will be rewound in interact()
            self._rat_var_count = 0
            self._rat_last_ms = 0
            self._rat_busy = False
            self._live_runs = {}  # run_id -> {caller, rendered_started, output}
            self._patch_completer()
            self._patch_toolbar()
            self._refresh_var_count()

        def _own_caller_id(self):
            # Equal to the MCP session id captured during _mcp_init.
            return _SESSION_ID or ""

        def interact(self):
            """Main REPL loop — wrapped with patch_stdout so live
            activity banners can be printed above the prompt without
            corrupting it."""
            from prompt_toolkit.patch_stdout import patch_stdout
            from prompt_toolkit.formatted_text import ANSI
            from prompt_toolkit import print_formatted_text as ptk_print

            stop = threading.Event()

            def render(text):
                if text:
                    ptk_print(ANSI(text))

            def on_event(evt):
                kind = evt.get("kind")
                run_id = evt.get("run_id")
                caller = evt.get("caller") or ""
                # Skip our own runs — they're already rendered inline
                # by run_cell. Match on MCP session id.
                if evt.get("caller_id") == self._own_caller_id():
                    if kind == "run_ended" and run_id in self._live_runs:
                        self._live_runs.pop(run_id, None)
                    return
                if kind == "run_started":
                    self._live_runs[run_id] = {
                        "caller": caller, "output": "", "started": True,
                    }
                    render(_format_live_event(
                        "run_started", caller, self._client_name,
                        code=evt.get("code", ""),
                        max_code=self._activity_max_code,
                        max_output=self._activity_max_output,
                    ))
                elif kind == "run_output":
                    live = self._live_runs.setdefault(
                        run_id,
                        {"caller": caller, "output": "", "started": False},
                    )
                    # If we missed run_started (e.g. joined mid-run),
                    # render a synthetic header first so the output
                    # chunks have context.
                    if not live["started"]:
                        render(_format_live_event(
                            "run_started", caller, self._client_name,
                            code="",  # we don't have it
                            max_code=self._activity_max_code,
                            max_output=self._activity_max_output,
                        ))
                        live["started"] = True
                    live["output"] += evt.get("text", "")
                    render(_format_live_event(
                        "run_output", caller, self._client_name,
                        output=evt.get("text", ""),
                        max_output=self._activity_max_output,
                    ))
                elif kind == "run_waiting":
                    render(_format_live_event(
                        "run_waiting", caller, self._client_name,
                    ))
                elif kind == "run_ended":
                    err = "" if evt.get("ok") else (evt.get("error") or "")
                    render(_format_live_event(
                        "run_ended", caller, self._client_name,
                        error=err,
                    ))
                    render("")  # blank line between runs
                    self._live_runs.pop(run_id, None)
                    # Refresh var count opportunistically.
                    try:
                        self._refresh_var_count()
                    except Exception:
                        pass
                    # Seed code into history for up-arrow recall.
                    if kind == "run_ended" and evt.get("ok"):
                        # We don't have the code here — ignore;
                        # history is also seeded from activity.jsonl.
                        pass

            # Start event subscriber. Requires an initialised session.
            subscriber = None
            if _SESSION_ID:
                subscriber = EventSubscriber(self._server, _SESSION_ID, on_event)
                subscriber.start()

            try:
                with patch_stdout():
                    # Always seed history from the runtime activity log
                    # so up-arrow cycles through every execution in this
                    # kernel, not just the ones typed here.
                    if self._history_seed_from_runtime:
                        self._seed_history_from_log()
                    # Show a recap of the most-recent activity so you
                    # see what happened while you were disconnected.
                    self._show_recent_activity(ptk_print, ANSI)
                    # Advance the file watcher cursor to end-of-file so
                    # we don't re-render runs we already saw via events.
                    self._activity.seek_to_end()

                    super().interact()
            finally:
                stop.set()
                if subscriber is not None:
                    subscriber.stop()

        def _show_recent_activity(self, ptk_print, ANSI):
            """On connect, replay the last few activity.jsonl entries
            so the REPL shows what happened while the user was away.
            Capped at 10 entries so the screen isn't flooded."""
            path = self._activity.path
            if not path:
                return
            try:
                with open(path, "r") as f:
                    lines = f.readlines()
            except (FileNotFoundError, OSError):
                return
            entries = []
            for line in lines[-10:]:
                line = line.strip()
                if not line:
                    continue
                try:
                    entries.append(json.loads(line))
                except (json.JSONDecodeError, ValueError):
                    continue
            if entries:
                ptk_print(ANSI(_format_activity(
                    entries, self._client_name,
                    max_code=self._activity_max_code,
                    max_output=self._activity_max_output,
                )))

        # Virtual SQLite session id for remote-seeded history.
        #
        # PtkHistoryAdapter.load_history_strings() pulls rows from
        # (current session) + (other sessions ordered by session DESC).
        # Real IPython sessions autoincrement from 1, so a large
        # positive id is guaranteed not to collide AND sorts before
        # every real session — which means seeded entries appear as
        # the most-recent "other session", i.e. right at the top of
        # up-arrow recall. That's what the user wants: remote
        # activity from VS Code / Claude / scripts should be the
        # first thing up-arrow hands back.
        _RAT_SEED_SESSION = 2_000_000_000

        def _seed_history(self, entries):
            """Seed remote code into IPython's SQLite history.

            ``PtkHistoryAdapter.store_string`` is a no-op in IPython
            (the adapter reads exclusively from ``history_manager``'s
            SQLite DB), so we write directly to the DB under a
            virtual session id. After writing we invalidate the
            adapter cache so the next up-arrow press reloads.
            """
            hm = getattr(self, "history_manager", None)
            if hm is None or not getattr(hm, "db", None):
                return
            db = hm.db
            # Find the last seeded line number (to continue appending)
            # and load every code string already in this session so we
            # can dedupe against the whole seed history, not just the
            # tail. This makes reconnecting idempotent.
            try:
                cur = db.execute(
                    "SELECT line, source_raw FROM history "
                    "WHERE session = ? ORDER BY line",
                    (self._RAT_SEED_SESSION,),
                )
                existing = cur.fetchall()
            except Exception:
                existing = []
            seen = {row[1].rstrip() for row in existing}
            next_line = (existing[-1][0] + 1) if existing else 1
            last_stored = existing[-1][1].rstrip() if existing else None
            rows = []
            for e in entries:
                code = e.get("code", "").strip()
                if not code or code == last_stored or code in seen:
                    continue
                rows.append((self._RAT_SEED_SESSION, next_line, code, code))
                seen.add(code)
                last_stored = code
                next_line += 1
            if not rows:
                return
            try:
                with db:
                    db.executemany(
                        "INSERT OR IGNORE INTO history "
                        "(session, line, source, source_raw) VALUES (?, ?, ?, ?)",
                        rows,
                    )
            except Exception:
                return
            if self.pt_app and hasattr(self.pt_app.history, "_loaded"):
                try:
                    self.pt_app.history._loaded = False
                    self.pt_app.history._refresh()
                except Exception:
                    pass

        def _seed_history_from_log(self):
            """Seed every code string from the activity log into history.
            Respects history_seed_limit (0 = unlimited)."""
            path = self._activity.path
            if not path:
                return
            try:
                with open(path, "r") as f:
                    lines = f.readlines()
            except (FileNotFoundError, OSError):
                return
            entries = []
            for line in lines:
                line = line.strip()
                if not line:
                    continue
                try:
                    entries.append(json.loads(line))
                except (json.JSONDecodeError, ValueError):
                    continue
            if self._history_seed_limit > 0 and len(entries) > self._history_seed_limit:
                entries = entries[-self._history_seed_limit:]
            if entries:
                self._seed_history(entries)

        def _refresh_var_count(self):
            """Quick look call to get current var count from kernel."""
            try:
                r = _tool(self._server, "look", {}, timeout=3)
                out = _text(r)
                # Parse "N vars" from overview like "py idle | 6 vars"
                m = re.search(r'(\d+) vars?', out)
                if m:
                    self._rat_var_count = int(m.group(1))
            except Exception:
                pass

        def _patch_toolbar(self):
            """Add a bottom toolbar showing rat status."""
            shell = self

            def _toolbar():
                import shutil
                cols = shutil.get_terminal_size((80, 24)).columns

                # Left side: rat <display> │ <name> [│ (venv)]
                left_parts = []
                left_parts.append(("class:rat.label", " rat "))
                left_parts.append(("class:rat.bar", " py "))
                left_parts.append(("class:rat.sep", "│"))
                left_parts.append(("class:rat.bar", f" {shell._kernel_name} "))
                if shell._venv:
                    left_parts.append(("class:rat.sep", "│"))
                    left_parts.append(("class:rat.bar", f" ({shell._venv}) "))

                # Right side: shared • N vars • F2:vars • Ctrl+D exit
                right_segs = []
                right_segs.append(("class:rat.green", "shared"))
                right_segs.append(("class:rat.bar", f" {shell._rat_var_count} vars"))
                if shell._rat_last_ms > 0:
                    t = f"{shell._rat_last_ms/1000:.1f}s" if shell._rat_last_ms >= 1000 else f"{shell._rat_last_ms}ms"
                    right_segs.append(("class:rat.bar", t))
                if shell._rat_busy:
                    right_segs.append(("class:rat.label", "running…"))
                right_segs.append(("class:rat.bar", "F2:vars"))
                right_segs.append(("class:rat.bar", "Ctrl+D exit"))

                # Join right segments with dot separators
                right_parts = []
                for i, seg in enumerate(right_segs):
                    if i > 0:
                        right_parts.append(("class:rat.dot", " • "))
                    right_parts.append(seg)

                # Calculate padding
                left_len = sum(len(t) for _, t in left_parts)
                right_len = sum(len(t) for _, t in right_parts)
                pad = max(1, cols - left_len - right_len)

                return left_parts + [("class:rat.bar", " " * pad)] + right_parts

            # pt_app is already created by super().__init__.
            if self.pt_app:
                self.pt_app.bottom_toolbar = _toolbar
                from prompt_toolkit.styles import Style, merge_styles
                from prompt_toolkit.keys import Keys
                toolbar_style = Style.from_dict({
                    "bottom-toolbar":           "bg:#262626 #d0d0d0 noreverse",
                    "bottom-toolbar.text":      "noreverse",
                    "rat.label":                "fg:#00d7ff bold noreverse",
                    "rat.bar":                  "fg:#d0d0d0 noreverse",
                    "rat.sep":                  "fg:#8a8a8a noreverse",
                    "rat.dot":                  "fg:#8a8a8a noreverse",
                    "rat.green":                "fg:#00d75f noreverse",
                })
                self.pt_app.style = merge_styles([self.pt_app.style, toolbar_style])

                # F2: show variable overview
                @self.pt_app.key_bindings.add(Keys.F2)
                def _show_vars(event):
                    try:
                        r = _tool(shell._server, "look", {}, timeout=5)
                        out = _text(r)
                        if out:
                            print(f"\n{out}\n")
                    except Exception as e:
                        print(f"\n[rat] error: {e}\n")

        def _ensure_mcp(self):
            if not self._mcp_ok:
                r = _mcp_init(self._server, self._client_name)
                self._mcp_ok = "_error" not in r

        def _rat_debug_dump(self, cmd):
            """Print frontend-local diagnostic info to stdout.

            Used for debugging the history-seeding pipeline: you
            type ``:ratdebug`` in the REPL and see exactly what the
            frontend's IPython shell, SQLite DB, and pt_app.history
            hold — none of which can be inspected by running code
            through the kernel.
            """
            import sqlite3
            parts = cmd.split()
            out_lines = []
            def p(msg=""):
                out_lines.append(str(msg))
            p("=== rat frontend debug ===")
            p(f"frontend PID: {os.getpid()}")
            p(f"shell class: {type(self).__name__}")
            p(f"activity log: {self._activity.path}")
            p(f"history_seed_from_runtime: {self._history_seed_from_runtime}")
            p(f"history_seed_limit: {self._history_seed_limit}")
            hm = getattr(self, "history_manager", None)
            p(f"history_manager: {hm!r}")
            if hm is not None:
                p(f"hist_file: {hm.hist_file}")
                p(f"session_number: {hm.session_number}")
                try:
                    rows = list(hm.db.execute(
                        "SELECT session, line, substr(source_raw,1,80) "
                        "FROM history ORDER BY session, line"))
                    p(f"SQLite rows: {len(rows)}")
                    for s, l, c in rows[:50]:
                        p(f"  sess={s:>12} line={l}: {c!r}")
                    if len(rows) > 50:
                        p(f"  ... {len(rows) - 50} more")
                except Exception as exc:
                    p(f"SQLite error: {exc}")
            pa = self.pt_app
            p(f"pt_app: {pa!r}")
            if pa is not None:
                h = pa.history
                p(f"pt_app.history type: {type(h).__name__}")
                p(f"pt_app.history module: {type(h).__module__}")
                p(f"pt_app.history id: {id(h)}")
                p(f"pt_app.history._loaded: {getattr(h, '_loaded', '?')}")
                try:
                    if hasattr(h, '_refresh'):
                        h._loaded = False
                        h._refresh()
                    strings = list(h.get_strings())
                    p(f"pt_app.history strings: {len(strings)}")
                    tail = strings[-15:] if len(strings) > 15 else strings
                    for i, s in enumerate(tail):
                        idx = len(strings) - len(tail) + i
                        p(f"  [{idx}] {s[:80]!r}")
                except Exception as exc:
                    p(f"pt_app.history error: {exc}")
            text = "\n".join(out_lines)
            print(text)
            # Also write to a file for async inspection.
            if len(parts) > 1:
                path = parts[1]
            else:
                path = "/tmp/rat-debug.txt"
            try:
                with open(path, "w") as f:
                    f.write(text + "\n")
                print(f"\n[debug written to {path}]")
            except Exception as exc:
                print(f"[could not write {path}: {exc}]")

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

                    # Filter dunder methods unless user is typing "__"
                    token_start = offset
                    while token_start > 0 and text[token_start - 1] not in " \t\n(,=[{.":
                        token_start -= 1
                    typing_prefix = text[token_start:offset]
                    if not typing_prefix.startswith("__"):
                        labels = [l for l in labels if not l.startswith("__")]

                    if labels:
                        # Find start of the token being completed.
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

            # Local debug escape hatch — kept intentionally.
            # ``:ratdebug [path]`` prints the frontend-side shell
            # state, SQLite history, and prompt_toolkit history
            # buffer. Runs inside the frontend process, never
            # touches the kernel, never lands in user history.
            # Documented in docs/config.md.
            if raw_cell.startswith(":ratdebug"):
                self._rat_debug_dump(raw_cell)
                return ExecutionResult(None)

            # Shell escapes stay local (user's terminal)
            if raw_cell.startswith("!"):
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

            # Execute on the shared kernel with live streaming.
            self._ensure_mcp()
            self._activity.mark_own(raw_cell)
            self._rat_busy = True
            er = ExecutionResult(None)
            t0 = __import__('time').monotonic()

            streamed_buf = []
            def _on_output(text):
                # Stream stdout/stderr to the user's terminal as it
                # arrives. Keep a copy so we can dedupe against the
                # final result text.
                streamed_buf.append(text)
                try:
                    sys.stdout.write(text)
                    sys.stdout.flush()
                except Exception:
                    pass

            def _on_elicit(params):
                # Server is asking us for input() text.
                msg = (params or {}).get("message", "")
                try:
                    return input()
                except (EOFError, KeyboardInterrupt):
                    return None

            try:
                r = _tool_streaming(
                    self._server, "run", {"code": raw_cell},
                    on_output=_on_output, on_elicit=_on_elicit,
                )
                elapsed = __import__('time').monotonic() - t0
                self._rat_last_ms = int(elapsed * 1000)
                self._rat_busy = False

                # Parse var count from hint.
                raw_text = _text(r)
                hint_match = _HINT_RE.search(raw_text)
                if hint_match:
                    var_match = re.search(r'(\d+) vars?', hint_match.group())
                    if var_match:
                        self._rat_var_count = int(var_match.group(1))

                # Strip both the hint and anything we already streamed
                # live, so output isn't printed twice.
                out = _strip_hint(raw_text)
                streamed = "".join(streamed_buf)
                if streamed and out.startswith(streamed):
                    out = out[len(streamed):]
                elif streamed and streamed.rstrip() and out.startswith(streamed.rstrip()):
                    out = out[len(streamed.rstrip()):].lstrip("\n")
                if out:
                    print(out)
                if _is_err(r):
                    # Signal failure via error_in_exec (success is a
                    # read-only property derived from this).
                    er.error_in_exec = RuntimeError("execution failed")
            except Exception as e:
                self._rat_busy = False
                print(f"[rat] connection error: {e}")
                print("[rat] kernel may have stopped. "
                      "Ctrl-D to exit, then 'rat py' to reconnect.")
                self._mcp_ok = False
                er.error_in_exec = e

            if store_history:
                self.execution_count += 1
                try:
                    self.history_manager.store_inputs(
                        self.execution_count, raw_cell)
                except Exception:
                    pass

            return er

    return RatShell


# ── Main ─────────────────────────────────────────────────────

def main():
    import signal as _signal
    if hasattr(_signal, 'SIGTSTP'):
        _signal.signal(_signal.SIGTSTP, _signal.SIG_IGN)  # block until args parsed

    parser = argparse.ArgumentParser(description="IPython frontend for rat py")
    parser.add_argument("--server", default="http://127.0.0.1:8717/mcp",
                        help="MCP server URL")
    parser.add_argument("--name", default="py",
                        help="Kernel name")
    parser.add_argument("--activity-log", default=None,
                        help="Path to activity log (for shared session visibility)")
    parser.add_argument("--cwd", default="",
                        help="Kernel working directory")
    parser.add_argument("--venv", default="",
                        help="Virtual environment path")
    parser.add_argument("--python-version", default="",
                        help="Python version string")
    parser.add_argument("--activity-max-code-lines", type=int, default=0,
                        help="Max code lines per activity entry (0 = unlimited)")
    parser.add_argument("--activity-max-output-lines", type=int, default=100,
                        help="Max output lines per activity entry (0 = unlimited)")
    parser.add_argument("--history-seed-limit", type=int, default=0,
                        help="Max entries to seed from activity.jsonl (0 = unlimited)")
    parser.add_argument("--no-history-seed", action="store_true",
                        help="Do not seed history from the runtime activity log")
    args = parser.parse_args()

    # Ctrl+Z detaches with exit code 2 → Go repl returns to shell.
    # Ctrl+D exits IPython normally with exit code 0 → Go repl shows picker.
    def _sigtstp_handler(signum, frame):
        print(f"\nDetached. Kernel still running. Reconnect: rat {args.name}")
        sys.exit(2)
    if hasattr(_signal, 'SIGTSTP'):
        _signal.signal(_signal.SIGTSTP, _sigtstp_handler)

    # Check IPython is available
    try:
        import IPython  # noqa: F401
    except ImportError:
        print("IPython is not installed.", file=sys.stderr)
        print("Install it:  pip install ipython", file=sys.stderr)
        sys.exit(1)

    # Connect to kernel
    client_name = _make_client_name("rat-py-repl")
    r = _mcp_init(args.server, client_name)
    if "_error" in r:
        print(f"Cannot connect to kernel at {args.server}",
              file=sys.stderr)
        print(f"Error: {r['_error']}", file=sys.stderr)
        print(f"\nIs the kernel running? Try: rat start {args.name}",
              file=sys.stderr)
        sys.exit(1)

    info = r.get("serverInfo", {})
    server_name = info.get("name", args.name)

    py_version = f"Python {args.python_version}" if args.python_version else "Python"
    cwd_display = args.cwd.replace(os.path.expanduser('~'), '~') if args.cwd else ''
    venv_display = os.path.basename(args.venv) if args.venv else ''

    # Launch IPython with our custom shell
    from IPython.terminal.ipapp import TerminalIPythonApp

    # Use a per-kernel IPython profile so history is isolated
    # from other kernels and normal IPython sessions.
    #
    # Use $HOME/.cache explicitly to match the Go side's cachedir
    # package — snap/flatpak set XDG_CACHE_HOME to a sandbox path
    # that the Go kernel daemon never sees, so deriving the path
    # from XDG_CACHE_HOME here would split the IPython profile
    # from activity.jsonl into two different directories.
    cache_home = os.path.join(os.path.expanduser("~"), ".cache")
    profile_dir = os.path.join(
        cache_home, "rat", "kernels", args.name, "ipython-profile",
    )
    hist_file = os.path.join(profile_dir, 'history.sqlite')
    Shell = _make_shell(args.server, kernel_name=args.name,
                        activity_log=args.activity_log, cwd=args.cwd,
                        venv=args.venv, py_version=py_version,
                        client_name=client_name,
                        activity_max_code_lines=args.activity_max_code_lines,
                        activity_max_output_lines=args.activity_max_output_lines,
                        history_seed_from_runtime=not args.no_history_seed,
                        history_seed_limit=args.history_seed_limit)
    os.makedirs(profile_dir, exist_ok=True)

    app = TerminalIPythonApp.instance()
    app.interact = True
    app.interactive_shell_class = Shell
    app.display_banner = False
    app.initialize(["--no-banner",
                    f"--HistoryManager.hist_file={hist_file}",
                    "--TerminalInteractiveShell.confirm_exit=False"])

    BLUE = "\033[34m"
    DIM = "\033[2m"
    BOLD = "\033[1m"
    R = "\033[0m"

    # Clear screen so prompt starts at top, toolbar at bottom.
    print("\033[2J\033[H", end="")
    banner_parts = [f"  {BLUE}rat{R} {BOLD}{args.name}{R} — {py_version}"]
    if cwd_display:
        banner_parts.append(f"in {cwd_display}")
    if venv_display:
        banner_parts.append(f"({venv_display})")
    print(" ".join(banner_parts))
    print(f"  {DIM}Shared namespace · other clients see your variables{R}")
    print()

    app.start()

    # Ctrl+D exits IPython cleanly. Print a detach message so the user
    # knows the kernel is still alive.
    print(f"\nDetached. Kernel still running. Reconnect: rat {args.name}")


if __name__ == "__main__":
    main()
