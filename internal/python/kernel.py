import ast
import builtins
import contextlib
import getpass as getpass_module
import inspect
import io
import json
import keyword
import os
import pkgutil
import queue
import rlcompleter
import socket
import sys
import threading
import time
import traceback

try:
    import jedi  # type: ignore
except Exception:
    jedi = None

namespace = {
    "__name__": "__main__",
    "__package__": None,
    "__builtins__": builtins,
}

# ── Optional IPython (for %magic support in the kernel) ─────

_ipython_shell = None


def _init_ipython():
    """Set up a headless IPython so magics like %timeit, %who work."""
    global _ipython_shell
    try:
        from IPython.core.interactiveshell import InteractiveShell
        # Suppress any output during init (protect JSON protocol on stdout)
        with contextlib.redirect_stdout(io.StringIO()), \
             contextlib.redirect_stderr(io.StringIO()):
            shell = InteractiveShell.instance()
        shell.user_ns = namespace
        shell.user_global_ns = namespace
        _ipython_shell = shell
    except Exception:
        pass


_init_ipython()


def _open_protocol():
    """Open Rat's private control channel.

    User code is allowed to use normal stdin/stdout/stderr. The Rat protocol
    must therefore live on a dedicated channel so subprocesses, os.write(1),
    native libraries, audio players, etc. cannot corrupt JSON control messages.

    Preferred mode is a localhost socket provided by the Go parent. FD mode is
    also supported for embedders that pass private fds as RAT_PROTOCOL_*_FD.
    A stdio fallback remains only for manual/debug launches of this file.
    """
    tcp_addr = os.environ.pop("RAT_PROTOCOL_TCP_ADDR", "")
    tcp_token = os.environ.pop("RAT_PROTOCOL_TOKEN", "")
    if tcp_addr:
        host, port_s = tcp_addr.rsplit(":", 1)
        sock = socket.create_connection((host, int(port_s)), timeout=10)
        proto_in = sock.makefile("r", buffering=1, encoding="utf-8")
        proto_out = sock.makefile("w", buffering=1, encoding="utf-8")
        proto_out.write(json.dumps({"op": "protocol_hello", "token": tcp_token}) + "\n")
        proto_out.flush()
        return proto_in, proto_out

    in_fd = os.environ.pop("RAT_PROTOCOL_IN_FD", "")
    out_fd = os.environ.pop("RAT_PROTOCOL_OUT_FD", "")
    if in_fd and out_fd:
        try:
            os.set_inheritable(int(in_fd), False)
            os.set_inheritable(int(out_fd), False)
        except Exception:
            pass
        return (
            os.fdopen(int(in_fd), "r", buffering=1, encoding="utf-8"),
            os.fdopen(int(out_fd), "w", buffering=1, encoding="utf-8"),
        )

    return sys.stdin, sys.stdout


_proto_in, _proto_out = _open_protocol()
_write_lock = threading.Lock()


def send(obj):
    """Write a JSON message to the Go parent via the private protocol."""
    with _write_lock:
        _proto_out.write(json.dumps(obj, ensure_ascii=False) + "\n")
        _proto_out.flush()


class InputMailbox:
    def __init__(self):
        self._lock = threading.Lock()
        self._event = threading.Event()
        self._text = ""
        self._waiting = False

    def begin(self):
        with self._lock:
            self._text = ""
            self._waiting = True
            self._event = threading.Event()

    def provide(self, text):
        with self._lock:
            if not self._waiting:
                return False
            self._text = text
            self._waiting = False
            self._event.set()
            return True

    def waiting(self):
        with self._lock:
            return self._waiting

    def wait(self):
        while True:
            if self._event.wait(0.1):
                break
        with self._lock:
            text = self._text
            self._text = ""
            self._waiting = False
            self._event = threading.Event()
            return text


class KernelState:
    def __init__(self):
        self._lock = threading.Lock()
        self._executing = False
        self._waiting = False

    def set_executing(self, value):
        with self._lock:
            self._executing = value
            if not value:
                self._waiting = False

    def set_waiting(self, value):
        with self._lock:
            self._waiting = value

    def status(self):
        with self._lock:
            if self._waiting:
                return "waiting_for_input"
            if self._executing:
                return "busy"
            return "idle"


mailbox = InputMailbox()
state = KernelState()
commands = queue.Queue()
shutdown = threading.Event()


def truncate(text, n):
    if n <= 0 or len(text) <= n:
        return text
    if n <= 3:
        return text[:n]
    return text[: n - 3] + "..."


def safe_repr(value, max_len=1000):
    try:
        text = repr(value)
    except Exception as exc:
        text = f"<repr failed: {exc}>"
    text = text.replace("\r", "").replace("\n", "\\n")
    return truncate(text, max_len)


def visible_items():
    items = []
    for name, value in namespace.items():
        if name == "__builtins__":
            continue
        if name.startswith("__") and name.endswith("__"):
            continue
        if name.startswith("_"):
            continue
        items.append((name, type(value).__name__, _short_preview(value)))
    items.sort(key=lambda item: item[0])
    return items


def _short_preview(value):
    """One-line preview for the overview table."""
    kind = type(value).__name__
    try:
        # DataFrame / Series
        if kind == "DataFrame":
            return f"({value.shape[0]} rows × {value.shape[1]} cols)"
        if kind == "Series":
            return f"({len(value)},) dtype={value.dtype}"
        if kind == "ndarray":
            return f"shape={value.shape} dtype={value.dtype}"
        if kind == "module":
            f = getattr(value, "__file__", None)
            return f"<module '{value.__name__}'>" if not f else f
    except Exception:
        pass
    return safe_repr(value, 60)


def look_overview():
    items = visible_items()
    if not items:
        return "python idle | 0 vars"

    name_width = max(4, max(len(name) for name, _, _ in items))
    type_width = max(4, max(len(kind) for _, kind, _ in items))
    lines = [f"python idle | {len(items)} vars", ""]
    for name, kind, preview in items[:200]:
        lines.append(f"{name:<{name_width}}  {kind:<{type_width}}  {preview}")
    return "\n".join(lines)


def resolve_expr(expr):
    if expr in namespace:
        return namespace[expr], None
    try:
        return eval(expr, namespace, namespace), None
    except Exception as exc:
        return None, exc


# ── Rich inspection ──────────────────────────────────────────

def look_at(expr, full=False):
    value, err = resolve_expr(expr)
    if err is not None:
        return f"{expr}: not found"
    return _inspect(expr, value, full=full)


def _inspect(name, value, full=False):
    kind = type(value).__name__
    lines = []

    # ── header ──
    header = f"{name}: {kind}"
    hint = _size_hint(value, kind)
    if hint:
        header += f" {hint}"
    lines.append(header)

    # ── signature (callables) ──
    if callable(value):
        try:
            lines.append(f"  {inspect.signature(value)}")
        except (ValueError, TypeError):
            pass

    # ── value preview (skip for callables / modules) ──
    if not (inspect.isfunction(value) or inspect.ismethod(value)
            or inspect.isbuiltin(value) or inspect.ismodule(value)):
        preview = safe_repr(value, 300)
        if preview:
            lines.append(f"  = {preview}")

    # ── source location ──
    loc = _source_loc(value)
    if loc:
        lines.append(f"  Defined in: {loc}")

    # ── docstring ──
    doc = _safe_doc(value)
    if doc:
        lines.append("")
        lines.append(doc)

    # ── children / drill-down ──
    children = _children(name, value, kind, full=full)
    if children:
        lines.append("")
        lines.extend(children)

    # ── methods (for non-trivial objects) ──
    methods = _public_methods(value, kind)
    if methods:
        lines.append("")
        shown = methods if full else methods[:20]
        m = ", ".join(shown)
        lines.append(f"  Methods: {m}")
        if not full and len(methods) > 20:
            lines.append(f"  ... {len(methods) - 20} more")

    return "\n".join(lines)


def _size_hint(value, kind):
    try:
        if kind == "DataFrame":
            return f"({value.shape[0]} rows × {value.shape[1]} columns)"
        if kind == "Series":
            return f"({len(value)},) dtype={value.dtype}"
        if kind == "ndarray":
            return f"shape={value.shape} dtype={value.dtype}"
        if isinstance(value, dict):
            return f"({len(value)} items)"
        if isinstance(value, (list, tuple)):
            return f"({len(value)} items)"
        if isinstance(value, set):
            return f"({len(value)} items)"
        if isinstance(value, str) and len(value) > 60:
            return f"(len={len(value)})"
    except Exception:
        pass
    return ""


def _source_loc(value):
    try:
        f = inspect.getfile(value)
    except (TypeError, OSError):
        return None
    try:
        _, line = inspect.getsourcelines(value)
        return f"{f}:{line}"
    except (OSError, TypeError):
        return f


_SKIP_DOC_TYPES = (int, float, str, bool, bytes, list, tuple,
                   dict, set, frozenset, type(None), type)


def _safe_doc(value):
    # Skip docs for trivial built-in types — they're well-known.
    if isinstance(value, _SKIP_DOC_TYPES):
        return None
    try:
        doc = inspect.getdoc(value)
        if not doc:
            return None
        doc = doc.strip()
        max_chars = int(os.environ.get("RAT_DOCSTRING_MAX_CHARS", "20000"))
        if max_chars > 0 and len(doc) > max_chars:
            return doc[:max_chars - 3] + "..."
        return doc
    except Exception:
        return None


def _children(name, value, kind, full=False):
    """Return drill-down lines for containers and special types."""
    try:
        # ── DataFrame ──
        if kind == "DataFrame":
            return _df_children(value)
        # ── Series ──
        if kind == "Series":
            return _series_children(value)
        # ── ndarray ──
        if kind == "ndarray":
            return _ndarray_children(value)
        # ── dict ──
        if isinstance(value, dict):
            return _dict_children(value, full=full)
        # ── list / tuple ──
        if isinstance(value, (list, tuple)):
            return _seq_children(value, full=full)
        # ── module ──
        if inspect.ismodule(value):
            return _module_children(value, full=full)
        # ── generic object with attributes ──
        if not isinstance(value, (int, float, str, bool, bytes,
                                  type(None), set, frozenset)):
            if inspect.isclass(value):
                return None  # methods list is enough
            return _object_children(value, full=full)
    except Exception:
        pass
    return None


def _fmt_child(marker, name, kind, preview, nw=12, tw=10):
    return f"  {marker} {name:<{nw}}  {kind:<{tw}}  {preview}"


def _df_children(df):
    lines = []
    try:
        cols = list(df.columns)
        cols_str = str(cols) if len(cols) <= 8 else str(cols[:6]) + f" ... ({len(cols)} total)"
        lines.append(_fmt_child("▸", "columns", "Index", cols_str))
        dtypes = {str(c): str(df[c].dtype) for c in cols[:10]}
        lines.append(_fmt_child("▸", "dtypes", "dict", str(dtypes)))
        lines.append(_fmt_child(" ", "shape", "tuple", str(df.shape)))
        try:
            mem = df.memory_usage(deep=True).sum()
            if mem > 1_000_000:
                lines.append(_fmt_child(" ", "memory", "", f"{mem / 1_000_000:.1f} MB"))
            elif mem > 1_000:
                lines.append(_fmt_child(" ", "memory", "", f"{mem / 1_000:.1f} KB"))
        except Exception:
            pass
        # head preview
        lines.append("")
        head_str = str(df.head(5))
        for hl in head_str.split("\n")[:7]:
            lines.append(f"  {hl}")
    except Exception:
        pass
    return lines


def _series_children(s):
    lines = []
    try:
        lines.append(_fmt_child(" ", "dtype", "", str(s.dtype)))
        lines.append(_fmt_child(" ", "shape", "", str(s.shape)))
        lines.append("")
        head_str = str(s.head(8))
        for hl in head_str.split("\n")[:10]:
            lines.append(f"  {hl}")
    except Exception:
        pass
    return lines


def _ndarray_children(arr):
    lines = []
    try:
        lines.append(_fmt_child(" ", "shape", "", str(arr.shape)))
        lines.append(_fmt_child(" ", "dtype", "", str(arr.dtype)))
        if arr.size > 0:
            flat = arr.ravel()
            preview = str(flat[:10])
            if arr.size > 10:
                preview += " ..."
            lines.append(_fmt_child(" ", "values", "", preview))
    except Exception:
        pass
    return lines


def _dict_children(d, full=False):
    lines = []
    all_items = list(d.items())
    items = all_items if full else all_items[:15]
    if not items:
        return lines
    kw = max(4, max(len(truncate(repr(k), 20)) for k, _ in items))
    tw = max(4, max(len(type(v).__name__) for _, v in items))
    for k, v in items:
        kr = truncate(repr(k), 20)
        lines.append(_fmt_child("▸", kr, type(v).__name__, safe_repr(v, 50), kw, tw))
    if not full and len(d) > 15:
        lines.append(f"  ... {len(d) - 15} more")
    return lines


def _seq_children(seq, full=False):
    lines = []
    items = list(seq) if full else list(seq[:15])
    if not items:
        return lines
    iw = len(str(max(0, len(items) - 1))) + 2  # [0] width
    tw = max(4, max(len(type(v).__name__) for v in items))
    for i, v in enumerate(items):
        idx = f"[{i}]"
        lines.append(_fmt_child(" ", idx, type(v).__name__, safe_repr(v, 50), iw, tw))
    if not full and len(seq) > 15:
        lines.append(f"  ... {len(seq) - 15} more")
    return lines


def _module_children(mod, full=False):
    lines = []
    try:
        names = [n for n in dir(mod) if not n.startswith("_")]
        if not names:
            return lines
        classes = []
        constants = []
        scan_names = names if full else names[:60]
        for n in scan_names:
            try:
                v = getattr(mod, n)
                if inspect.isclass(v):
                    classes.append(n)
                elif not callable(v):
                    constants.append(n)
            except Exception:
                pass
        if classes:
            shown = classes if full else classes[:15]
            lines.append(f"  Classes: {', '.join(shown)}")
        if constants:
            shown = constants if full else constants[:15]
            lines.append(f"  Constants: {', '.join(shown)}")
    except Exception:
        pass
    return lines


def _object_children(value, full=False):
    lines = []
    try:
        all_attrs = [(k, v) for k, v in vars(value).items() if not k.startswith("_")]
        if not all_attrs:
            return lines
        attrs = all_attrs if full else all_attrs[:15]
        nw = max(4, max(len(k) for k, _ in attrs))
        tw = max(4, max(len(type(v).__name__) for _, v in attrs))
        for k, v in attrs:
            lines.append(_fmt_child("▸", k, type(v).__name__, safe_repr(v, 50), nw, tw))
        total = len(all_attrs)
        if not full and total > 15:
            lines.append(f"  ... {total - 15} more")
    except Exception:
        pass
    return lines


def _public_methods(value, kind):
    """Return public method names, skipping trivial built-in types."""
    # Don't list methods for basic types — they're well-known.
    if isinstance(value, (int, float, str, bool, bytes, list, tuple,
                          dict, set, frozenset, type(None))):
        return []
    try:
        meths = []
        for name in dir(value):
            if name.startswith("_"):
                continue
            try:
                if callable(getattr(value, name)):
                    meths.append(name)
            except Exception:
                pass
        return sorted(meths)
    except Exception:
        return []


def fallback_complete(code, cursor):
    prefix = code[:cursor] if cursor >= 0 else code
    stripped = prefix.lstrip()
    token = prefix.split()[-1] if prefix.split() else ""

    results = []
    seen = set()

    def add(label, kind):
        if not label or label in seen:
            return
        seen.add(label)
        results.append((label, kind))

    if stripped.startswith("import ") or stripped.startswith("from "):
        mod_prefix = token
        for _, name, _ in pkgutil.iter_modules():
            if name.startswith(mod_prefix):
                add(name, "module")
        for kw in keyword.kwlist:
            if kw.startswith(mod_prefix):
                add(kw, "keyword")
        return results[:50]

    completer = rlcompleter.Completer(namespace)
    state_idx = 0
    while True:
        match = completer.complete(token, state_idx)
        if match is None:
            break
        kind = "value"
        base = match.rstrip("(")
        try:
            if base in namespace:
                obj = namespace[base]
                if inspect.ismodule(obj):
                    kind = "module"
                elif inspect.isfunction(obj) or inspect.ismethod(obj) or callable(obj):
                    kind = "function"
                else:
                    kind = "variable"
            elif base in keyword.kwlist:
                kind = "keyword"
        except Exception:
            pass
        add(match, kind)
        state_idx += 1

    if "." not in token:
        for name, value in namespace.items():
            if name.startswith(token):
                kind = "module" if inspect.ismodule(value) else "variable"
                if callable(value):
                    kind = "function"
                add(name, kind)
        for kw in keyword.kwlist:
            if kw.startswith(token):
                add(kw, "keyword")
        for _, name, _ in pkgutil.iter_modules():
            if name.startswith(token):
                add(name, "module")

    return results[:50]


def complete(code, cursor):
    code = code[:cursor] if cursor >= 0 else code
    items = []

    if jedi is not None:
        try:
            line = code.count("\n") + 1
            column = len(code.rsplit("\n", 1)[-1])
            script = jedi.Interpreter(code, [namespace])
            for comp in script.complete(line, column):
                kind = getattr(comp, "type", None) or "value"
                items.append((comp.name, kind))
        except Exception:
            items = []

    if not items:
        items = fallback_complete(code, len(code))
    if not items:
        return "No completions."

    seen = set()
    lines = []
    for label, kind in items:
        if label in seen:
            continue
        seen.add(label)
        lines.append(f"{label:<20} {kind}")
        if len(lines) == 50:
            break
    return "\n".join(lines) if lines else "No completions."


# ── Plot capture ─────────────────────────────────────────────

_PLOT_DIR = os.path.join(
    os.environ.get('XDG_CACHE_HOME', os.path.expanduser('~/.cache')),
    'rat', 'plots'
)
_matplotlib_patched = False


def _do_patch_matplotlib():
    """Patch plt.show() to save PNGs and print __RAT_PLOT__ markers."""
    global _matplotlib_patched
    if _matplotlib_patched:
        return
    _matplotlib_patched = True
    try:
        import matplotlib
        import matplotlib.pyplot as plt
        if matplotlib.get_backend().lower() not in ('agg', 'pdf', 'svg'):
            matplotlib.use('Agg')

        os.makedirs(_PLOT_DIR, exist_ok=True)

        def _rat_show(*args, **kwargs):
            figs = [plt.figure(n) for n in plt.get_fignums()]
            for i, fig in enumerate(figs):
                name = f"fig-{int(time.time() * 1000)}-{i}.png"
                filepath = os.path.join(_PLOT_DIR, name)
                fig.savefig(filepath, dpi=150, bbox_inches='tight', facecolor='white')
                print(f"__RAT_PLOT__:{filepath}")
            plt.close('all')

        plt.show = _rat_show
    except Exception:
        pass


def _maybe_patch_matplotlib():
    """Called after each exec. If user imported matplotlib, patch it."""
    if not _matplotlib_patched and 'matplotlib.pyplot' in sys.modules:
        _do_patch_matplotlib()


# Install an import hook so plt.show() is patched the instant
# matplotlib.pyplot is imported — before user code calls show().
import importlib
import importlib.abc
import importlib.machinery


class _MatplotlibFinder(importlib.abc.MetaPathFinder):
    def find_module(self, fullname, path=None):
        if fullname == 'matplotlib.pyplot':
            return _MatplotlibLoader()
        return None


class _MatplotlibLoader(importlib.abc.Loader):
    def load_module(self, fullname):
        # Remove ourselves temporarily to avoid infinite recursion
        sys.meta_path[:] = [f for f in sys.meta_path if not isinstance(f, _MatplotlibFinder)]
        try:
            mod = importlib.import_module(fullname)
        finally:
            # Re-add ourselves so we catch future reimports
            if not any(isinstance(f, _MatplotlibFinder) for f in sys.meta_path):
                sys.meta_path.insert(0, _MatplotlibFinder())
        _do_patch_matplotlib()
        return mod


sys.meta_path.insert(0, _MatplotlibFinder())


def run_code(code, allow_stdin):
    # Normal stdout/stderr are now safe user-output streams. The Rat protocol
    # lives on _proto_in/_proto_out, so Python prints, os.write(1/2), native
    # libraries, and subprocess children can all write to fd 1/2 without
    # corrupting control messages. Go captures those pipes and exposes them as
    # live/final cell output.
    original_input = builtins.input
    original_getpass = getpass_module.getpass
    original_stdout = sys.stdout
    original_stderr = sys.stderr

    def hooked_input(prompt=""):
        if not allow_stdin:
            raise RuntimeError("stdin is only supported when the client handles input")
        sys.stdout.write(prompt)
        sys.stdout.flush()
        # Flush stdout so Go sees the prompt text, then tell Go we are blocked
        # on input() so it can relay to the VS Code extension or CLI.
        sys.stdout.flush()
        send({"op": "input_request", "prompt": prompt})
        state.set_waiting(True)
        mailbox.begin()
        try:
            text = mailbox.wait()
        finally:
            state.set_waiting(False)
            send({"op": "input_delivered"})
        display = text.replace("\r\n", "\n").replace("\r", "\n")
        if display.endswith("\n"):
            sys.stdout.write(display)
        else:
            sys.stdout.write(display + "\n")
        sys.stdout.flush()
        return display.rstrip("\n")

    def hooked_getpass(prompt="Password: ", stream=None):
        if not allow_stdin:
            raise RuntimeError("stdin is only supported when the client handles input")
        sys.stdout.write(prompt)
        sys.stdout.flush()
        send({"op": "input_request", "prompt": prompt})
        state.set_waiting(True)
        mailbox.begin()
        try:
            text = mailbox.wait()
        finally:
            state.set_waiting(False)
            send({"op": "input_delivered"})
        sys.stdout.write("\n")
        sys.stdout.flush()
        return text.replace("\r\n", "\n").replace("\r", "\n").rstrip("\n")

    builtins.input = hooked_input
    getpass_module.getpass = hooked_getpass

    # Transform IPython magics (%timeit, %who, etc.) to executable Python
    if _ipython_shell is not None:
        try:
            code = _ipython_shell.transform_cell(code)
        except Exception:
            pass

    try:
        tree = ast.parse(code, mode="exec")
        if tree.body and isinstance(tree.body[-1], ast.Expr):
            body = tree.body[:-1]
            if body:
                module = ast.Module(body=body, type_ignores=[])
                exec(compile(module, "<rat>", "exec"), namespace, namespace)
            expr = ast.Expression(tree.body[-1].value)
            result = eval(compile(expr, "<rat>", "eval"), namespace, namespace)
            namespace["_"] = result
            if result is not None:
                print(repr(result))
        else:
            exec(compile(tree, "<rat>", "exec"), namespace, namespace)
        _maybe_patch_matplotlib()
        sys.stdout.flush()
        sys.stderr.flush()
        return {"success": True, "output": "", "error": "", "vars": len(visible_items())}
    except KeyboardInterrupt:
        sys.stdout.flush()
        sys.stderr.flush()
        return {"success": False, "output": "", "error": "KeyboardInterrupt", "vars": len(visible_items())}
    except Exception:
        sys.stdout.flush()
        sys.stderr.flush()
        return {"success": False, "output": "", "error": traceback.format_exc(), "vars": len(visible_items())}
    finally:
        builtins.input = original_input
        getpass_module.getpass = original_getpass
        sys.stdout = original_stdout
        sys.stderr = original_stderr
        state.set_waiting(False)


def reader_loop():
    for raw in _proto_in:
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
            continue
        if op == "shutdown":
            shutdown.set()
            commands.put(req)
            return
        commands.put(req)

    shutdown.set()
    commands.put({"op": "shutdown"})


def main():
    threading.Thread(target=reader_loop, daemon=True).start()

    while True:
        req = commands.get()
        op = req.get("op")
        try:
            if op == "shutdown":
                return
            if op == "ping":
                send({"ok": True})
            elif op == "run":
                state.set_executing(True)
                try:
                    send(run_code(req.get("code", ""), bool(req.get("allow_stdin", True))))
                finally:
                    state.set_executing(False)
            elif op == "look_overview":
                send({"text": look_overview()})
            elif op == "look_at":
                send({"text": look_at(req.get("at", ""), bool(req.get("full", False)))})
            elif op == "complete":
                send({"text": complete(req.get("code", ""), int(req.get("cursor", -1)))})
            elif op == "status":
                send({"state": state.status()})
            else:
                send({"error": f"unknown op: {op}"})
        except KeyboardInterrupt:
            state.set_executing(False)
            state.set_waiting(False)
            send({"success": False, "output": "", "error": "KeyboardInterrupt"})
        except Exception:
            state.set_executing(False)
            state.set_waiting(False)
            send({"success": False, "output": "", "error": traceback.format_exc()})


if __name__ == "__main__":
    main()
