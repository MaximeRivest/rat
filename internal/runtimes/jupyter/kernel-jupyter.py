import builtins
import contextlib
import json
import os
import queue
import sys
import threading
import time
from pathlib import Path
from urllib.parse import urljoin

try:
    import requests
except Exception:
    requests = None

try:
    from jupyter_client import BlockingKernelClient, find_connection_file
except Exception:
    BlockingKernelClient = None
    find_connection_file = None

try:
    from jupyter_server.serverapp import list_running_servers as list_serverapp_servers
except Exception:
    list_serverapp_servers = None

try:
    from notebook.notebookapp import list_running_servers as list_notebook_servers
except Exception:
    list_notebook_servers = None


_pipe = sys.stdout
_write_lock = threading.Lock()
_commands = queue.Queue()
_shutdown = threading.Event()
_input_q = queue.Queue()
_busy = threading.Event()
_waiting_for_input = threading.Event()
_client = None
_monitor_client = None
_init_error = None
_connection_file = None
_kernel_id = None
_execution_count = 0
_activity_log = os.environ.get("RAT_ACTIVITY_LOG", "")
_own_session_id = ""


class InputMailbox:
    def provide(self, text):
        while True:
            try:
                _input_q.get_nowait()
            except queue.Empty:
                break
        _input_q.put(text)

    def wait(self):
        return _input_q.get()


mailbox = InputMailbox()


def send(obj):
    with _write_lock:
        _pipe.write(json.dumps(obj, ensure_ascii=False) + "\n")
        _pipe.flush()


def _env(name, default=""):
    v = os.environ.get(name, "")
    return v if v else default


def _normalize_path(value, cwd):
    p = Path(value)
    if not p.is_absolute():
        p = Path(cwd) / p
    return str(p.resolve())


def _server_token(server):
    token = _env("RAT_JUPYTER_TOKEN")
    if token:
        return token
    return server.get("token", "") if isinstance(server, dict) else ""


def _auth_params(server):
    token = _server_token(server)
    return {"token": token} if token else {}


def _running_servers():
    servers = []
    seen = set()
    for fn in (list_serverapp_servers, list_notebook_servers):
        if fn is None:
            continue
        try:
            for srv in fn():
                key = srv.get("url", "") + "|" + srv.get("root_dir", srv.get("notebook_dir", ""))
                if key in seen:
                    continue
                seen.add(key)
                servers.append(srv)
        except Exception:
            continue
    return servers


def _session_path(server, session):
    root = server.get("root_dir") or server.get("notebook_dir") or ""
    rel = session.get("path") or ""
    if not root:
        return rel
    return str((Path(root) / rel).resolve())


def _request_json(server, path):
    if requests is None:
        raise RuntimeError("requests not installed")
    url = urljoin(server["url"], path)
    headers = {}
    token = _server_token(server)
    if token:
        headers["Authorization"] = f"token {token}"
    resp = requests.get(url, params=_auth_params(server), headers=headers, timeout=5)
    resp.raise_for_status()
    return resp.json()


def _resolve_kernel_id_from_notebook(target):
    target_abs = str(Path(target).resolve())
    target_name = Path(target_abs).name
    matches = []
    server_filter = _env("RAT_JUPYTER_SERVER")
    for server in _running_servers():
        if server_filter and server_filter not in server.get("url", ""):
            continue
        try:
            sessions = _request_json(server, "api/sessions")
        except Exception:
            continue
        for session in sessions:
            session_abs = _session_path(server, session)
            if session_abs == target_abs or Path(session_abs).name == target_name:
                kernel = session.get("kernel") or {}
                kernel_id = kernel.get("id")
                if kernel_id:
                    matches.append(kernel_id)
    if len(matches) == 1:
        return matches[0]
    if len(matches) > 1:
        raise RuntimeError(f"multiple running kernels match notebook: {target}")
    raise RuntimeError(f"no running Jupyter session found for notebook: {target}")


def _resolve_connection_file(target, cwd):
    if find_connection_file is None:
        raise RuntimeError("jupyter_client not installed")

    if not target:
        raise RuntimeError("missing Jupyter target; set --opt target=... or RAT_JUPYTER_TARGET")

    target_path = Path(target)
    if target_path.suffix == ".json":
        if target_path.exists():
            return str(target_path.resolve()), target_path.stem.removeprefix("kernel-")
        try:
            path = find_connection_file(str(target))
            stem = Path(path).stem
            return str(Path(path).resolve()), stem.removeprefix("kernel-")
        except Exception as exc:
            raise RuntimeError(f"connection file not found: {target}: {exc}")

    if target_path.suffix == ".ipynb" or target_path.exists():
        target_abs = _normalize_path(target, cwd)
        kernel_id = _resolve_kernel_id_from_notebook(target_abs)
        try:
            path = find_connection_file(f"kernel-{kernel_id}.json")
            return str(Path(path).resolve()), kernel_id
        except Exception as exc:
            raise RuntimeError(f"kernel connection file not found for {kernel_id}: {exc}")

    kernel_id = target.removeprefix("kernel-").removesuffix(".json")
    try:
        path = find_connection_file(f"kernel-{kernel_id}.json")
        return str(Path(path).resolve()), kernel_id
    except Exception as exc:
        raise RuntimeError(f"kernel not found: {target}: {exc}")


def _connect():
    global _client, _monitor_client, _init_error, _connection_file, _kernel_id, _own_session_id
    if BlockingKernelClient is None:
        _init_error = "jupyter_client not installed"
        return
    cwd = _env("RAT_JUPYTER_CWD", os.getcwd())
    target = _env("RAT_JUPYTER_TARGET")
    try:
        _connection_file, _kernel_id = _resolve_connection_file(target, cwd)
        client = BlockingKernelClient(connection_file=_connection_file)
        client.load_connection_file()
        client.start_channels()
        client.wait_for_ready(timeout=10)
        _client = client
        _own_session_id = getattr(getattr(client, "session", None), "session", "") or ""

        monitor = BlockingKernelClient(connection_file=_connection_file)
        monitor.load_connection_file()
        monitor.start_channels()
        monitor.wait_for_ready(timeout=10)
        _monitor_client = monitor
    except Exception as exc:
        _init_error = str(exc)


def _append_activity(code, output, ok=True, client="notebook"):
    if not _activity_log or not code.strip():
        return
    entry = {
        "n": 0,
        "code": code[:500],
        "output": output[:500],
        "ok": bool(ok),
        "t": int(time.time()),
        "client": client,
    }
    try:
        Path(_activity_log).parent.mkdir(parents=True, exist_ok=True)
        with open(_activity_log, "a", encoding="utf-8") as f:
            f.write(json.dumps(entry, ensure_ascii=False) + "\n")
    except Exception:
        pass


def _monitor_iopub():
    if _monitor_client is None:
        return
    pending = {}
    while not _shutdown.is_set():
        try:
            msg = _monitor_client.get_iopub_msg(timeout=0.25)
        except queue.Empty:
            continue
        except Exception:
            continue

        header = msg.get("parent_header", {}) or {}
        msg_id = header.get("msg_id")
        if not msg_id:
            continue
        session_id = header.get("session", "")
        if _own_session_id and session_id == _own_session_id:
            continue

        item = pending.setdefault(msg_id, {
            "code": "",
            "output": [],
            "ok": True,
            "client": "notebook",
        })
        if session_id:
            item["client"] = f"notebook {session_id[:8]}"

        msg_type = msg.get("msg_type")
        content = msg.get("content", {})
        if msg_type == "execute_input":
            item["code"] = content.get("code", "")
        elif msg_type == "stream":
            text = content.get("text", "")
            if text:
                item["output"].append(text)
        elif msg_type in ("execute_result", "display_data"):
            data = content.get("data", {})
            text = data.get("text/plain", "")
            if text:
                item["output"].append(text + ("\n" if not text.endswith("\n") else ""))
        elif msg_type == "error":
            item["ok"] = False
            tb = content.get("traceback", [])
            item["output"].append(("\n".join(tb) if tb else f"{content.get('ename', 'Error')}: {content.get('evalue', '')}") + "\n")
        elif msg_type == "status" and content.get("execution_state") == "idle":
            code = item.get("code", "")
            if code.strip():
                _append_activity(code, "".join(item.get("output", [])), item.get("ok", True), item.get("client", "notebook"))
            pending.pop(msg_id, None)


def _collect_execute(msg_id, allow_input=True):
    global _execution_count
    output_parts = []
    error_text = ""
    idle = False
    shell_done = False
    while not (idle and shell_done):
        if allow_input:
            try:
                msg = _client.get_stdin_msg(timeout=0.05)
                if msg.get("parent_header", {}).get("msg_id") == msg_id and msg.get("msg_type") == "input_request":
                    prompt = msg.get("content", {}).get("prompt", "")
                    if prompt:
                        send({"op": "output_chunk", "text": prompt})
                    send({"op": "input_request", "prompt": prompt})
                    _waiting_for_input.set()
                    try:
                        text = mailbox.wait()
                    finally:
                        _waiting_for_input.clear()
                    _client.input(text)
                    send({"op": "input_delivered"})
            except queue.Empty:
                pass
            except Exception:
                pass

        try:
            msg = _client.get_iopub_msg(timeout=0.1)
            if msg.get("parent_header", {}).get("msg_id") != msg_id:
                continue
            msg_type = msg.get("msg_type")
            content = msg.get("content", {})
            if msg_type == "status" and content.get("execution_state") == "idle":
                idle = True
            elif msg_type == "stream":
                text = content.get("text", "")
                if text:
                    output_parts.append(text)
                    send({"op": "output_chunk", "text": text})
            elif msg_type in ("execute_result", "display_data"):
                data = content.get("data", {})
                text = data.get("text/plain", "")
                if text:
                    if output_parts and not output_parts[-1].endswith("\n"):
                        output_parts.append("\n")
                    output_parts.append(text + ("\n" if not text.endswith("\n") else ""))
            elif msg_type == "error":
                tb = content.get("traceback", [])
                error_text = "\n".join(tb) if tb else f"{content.get('ename', 'Error')}: {content.get('evalue', '')}".strip()
        except queue.Empty:
            pass

        if not shell_done:
            try:
                reply = _client.get_shell_msg(timeout=0.05)
                if reply.get("parent_header", {}).get("msg_id") != msg_id:
                    continue
                shell_done = True
                content = reply.get("content", {})
                if content.get("execution_count") is not None:
                    _execution_count = content.get("execution_count")
                if content.get("status") == "error" and not error_text:
                    tb = content.get("traceback", [])
                    error_text = "\n".join(tb) if tb else f"{content.get('ename', 'Error')}: {content.get('evalue', '')}".strip()
            except queue.Empty:
                pass

    return "".join(output_parts), error_text


def _silent_exec(code):
    msg_id = _client.execute(code, silent=False, store_history=False, allow_stdin=False)
    output, error = _collect_execute(msg_id, allow_input=False)
    if error:
        return f"ERROR: {error}"
    return output.strip()


def _overview_code():
    return r'''
import builtins
_skip = {
    "In", "Out", "exit", "quit", "get_ipython"
}
items = []
for _name, _value in globals().items():
    if _name in _skip:
        continue
    if _name.startswith("_"):
        continue
    try:
        _kind = type(_value).__name__
        _preview = repr(_value).replace("\n", " ")
    except Exception as _exc:
        _kind = type(_value).__name__
        _preview = f"<repr failed: {_exc}>"
    if len(_preview) > 60:
        _preview = _preview[:60]
    items.append((_name, _kind, _preview))
items.sort(key=lambda x: x[0])
if not items:
    print("python idle | 0 vars")
else:
    _nw = max(4, max(len(_n) for _n, _, _ in items))
    _tw = max(4, max(len(_t) for _, _t, _ in items))
    print(f"python idle | {len(items)} vars")
    print()
    for _n, _t, _p in items[:200]:
        print(f"{_n:<{_nw}}  {_t:<{_tw}}  {_p}")
'''


def _inspect_code(expr):
    return (
        "import inspect\n"
        f"_rat_expr = {expr!r}\n"
        "try:\n"
        "    _rat_value = eval(_rat_expr, globals(), globals())\n"
        "except Exception:\n"
        "    print(f'{_rat_expr}: not found')\n"
        "else:\n"
        "    _rat_kind = type(_rat_value).__name__\n"
        "    print(f'{_rat_expr}: {_rat_kind}')\n"
        "    try:\n"
        "        _rat_repr = repr(_rat_value).replace('\\n', ' ')\n"
        "        if len(_rat_repr) > 300: _rat_repr = _rat_repr[:300]\n"
        "        if _rat_repr: print(f'  = {_rat_repr}')\n"
        "    except Exception:\n"
        "        pass\n"
        "    try:\n"
        "        _rat_doc = inspect.getdoc(_rat_value)\n"
        "        if _rat_doc:\n"
        "            print()\n"
        "            print(_rat_doc.split('\\n\\n', 1)[0][:500])\n"
        "    except Exception:\n"
        "        pass\n"
    )


def _normalize_run_code(code):
    code = code.removeprefix("\n")
    if "\n" not in code:
        return code
    lines = code.split("\n")
    if len(lines) < 2:
        return code
    if not lines[0].strip() or lines[0].strip().endswith(":"):
        return code
    min_indent = None
    for line in lines[1:]:
        if not line.strip():
            continue
        indent = len(line) - len(line.lstrip(" "))
        if indent == 0:
            return code
        if min_indent is None or indent < min_indent:
            min_indent = indent
    if not min_indent or min_indent <= 0:
        return code
    prefix = " " * min_indent
    for i in range(1, len(lines)):
        if not lines[i].strip():
            continue
        if lines[i].startswith(prefix):
            lines[i] = lines[i][min_indent:]
    return "\n".join(lines)


def handle_run(code):
    if _client is None:
        return {"success": False, "output": "", "error": _init_error or "Jupyter client not initialized", "vars": 0}
    _busy.set()
    try:
        code = _normalize_run_code(code)
        msg_id = _client.execute(code, silent=False, store_history=True, allow_stdin=True)
        output, error = _collect_execute(msg_id, allow_input=True)
        return {"success": not bool(error), "output": output, "error": error, "vars": 0}
    finally:
        _busy.clear()
        _waiting_for_input.clear()


def handle_complete(code, cursor):
    if _client is None:
        return {"text": f"ERROR: {_init_error or 'Jupyter client not initialized'}"}
    try:
        msg_id = _client.complete(code, cursor_pos=cursor)
        reply = _client.get_shell_msg(timeout=5)
        while reply.get("parent_header", {}).get("msg_id") != msg_id:
            reply = _client.get_shell_msg(timeout=5)
        matches = reply.get("content", {}).get("matches", [])
        if not matches:
            return {"text": "No completions."}
        return {"text": "\n".join(f"{m:<20} value" for m in matches[:50])}
    except Exception as exc:
        return {"text": f"ERROR: {exc}"}


def handle_look_at(expr):
    if _client is None:
        return {"text": f"ERROR: {_init_error or 'Jupyter client not initialized'}"}
    try:
        msg_id = _client.inspect(expr, cursor_pos=len(expr), detail_level=0)
        reply = _client.get_shell_msg(timeout=5)
        while reply.get("parent_header", {}).get("msg_id") != msg_id:
            reply = _client.get_shell_msg(timeout=5)
        content = reply.get("content", {})
        data = content.get("data", {})
        text = data.get("text/plain", "").strip()
        if text:
            return {"text": text}
    except Exception:
        pass
    return {"text": _silent_exec(_inspect_code(expr))}


def handle_look_overview():
    if _client is None:
        return {"text": f"ERROR: {_init_error or 'Jupyter client not initialized'}"}
    return {"text": _silent_exec(_overview_code())}


def reader_loop():
    for raw in sys.stdin:
        line = raw.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except Exception as exc:
            send({"success": False, "output": "", "error": f"invalid json: {exc}"})
            continue
        op = req.get("op")
        if op == "input":
            mailbox.provide(req.get("text", ""))
            send({"ok": True})
            continue
        _commands.put(req)
        if op == "shutdown":
            return
    _commands.put({"op": "shutdown"})


def main():
    _connect()
    threading.Thread(target=reader_loop, daemon=True).start()
    if _monitor_client is not None:
        threading.Thread(target=_monitor_iopub, daemon=True).start()

    while True:
        req = _commands.get()
        op = req.get("op")
        try:
            if op == "shutdown":
                break
            if op == "ping":
                if _client is None:
                    send({"ok": False, "error": _init_error or "failed to connect to Jupyter kernel"})
                else:
                    send({"ok": True})
            elif op == "run":
                send(handle_run(req.get("code", "")))
            elif op == "complete":
                send(handle_complete(req.get("code", ""), int(req.get("cursor", -1))))
            elif op == "look_at":
                send(handle_look_at(req.get("at", "")))
            elif op == "look_overview":
                send(handle_look_overview())
            else:
                send({"error": f"unknown op: {op}"})
        except Exception as exc:
            send({"success": False, "output": "", "error": str(exc)})

    _shutdown.set()
    if _monitor_client is not None:
        with contextlib.suppress(Exception):
            _monitor_client.stop_channels()
    if _client is not None:
        with contextlib.suppress(Exception):
            _client.stop_channels()


if __name__ == "__main__":
    main()
