#!/usr/bin/env python3
"""Mock kernel for testing the generic JSON kernel driver.

Speaks the rat kernel protocol (JSON lines on stdin/stdout).
Supports run, look_overview, look_at, complete, status, ping, shutdown.
Maintains a simple namespace dict for state persistence.
Can emit events when triggered by special code patterns.
"""
import json
import sys
import traceback

namespace = {}


def send(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def visible_items():
    return [(k, type(v).__name__, repr(v)) for k, v in namespace.items()
            if not k.startswith("_")]


for raw in sys.stdin:
    line = raw.strip()
    if not line:
        continue
    try:
        req = json.loads(line)
    except Exception as exc:
        send({"success": False, "error": f"bad json: {exc}"})
        continue

    op = req.get("op")

    if op == "ping":
        send({"ok": True})

    elif op == "shutdown":
        break

    elif op == "run":
        code = req.get("code", "")
        # Special: emit an event before running
        if code.startswith("__emit_event__"):
            send({"op": "event", "type": "test_event", "data": {"msg": "hello"}})
            send({"success": True, "output": "event emitted", "error": "", "vars": len(visible_items())})
            continue
        # Special: emit output_chunk streaming
        if code.startswith("__stream__"):
            send({"op": "output_chunk", "text": "chunk1\n"})
            send({"op": "output_chunk", "text": "chunk2\n"})
            send({"success": True, "output": "chunk1\nchunk2\n", "error": "", "vars": len(visible_items())})
            continue
        # Special: slow (emit output_chunk, then result)
        if code == "__input_test__":
            send({"op": "input_request", "prompt": "name: "})
            send({"op": "input_delivered"})
            send({"success": True, "output": "got input", "error": "", "vars": 0})
            continue
        try:
            import ast
            tree = ast.parse(code, mode="exec")
            import io
            import contextlib
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                if tree.body and isinstance(tree.body[-1], ast.Expr):
                    body = tree.body[:-1]
                    if body:
                        mod = ast.Module(body=body, type_ignores=[])
                        exec(compile(mod, "<mock>", "exec"), namespace)
                    expr = ast.Expression(tree.body[-1].value)
                    result = eval(compile(expr, "<mock>", "eval"), namespace)
                    if result is not None:
                        print(repr(result))
                else:
                    exec(compile(tree, "<mock>", "exec"), namespace)
            output = buf.getvalue()
            send({"success": True, "output": output, "error": "", "vars": len(visible_items())})
        except Exception:
            send({"success": False, "output": "", "error": traceback.format_exc(), "vars": len(visible_items())})

    elif op == "look_overview":
        items = visible_items()
        if not items:
            send({"text": "mock idle | 0 vars"})
        else:
            lines = [f"mock idle | {len(items)} vars", ""]
            for name, kind, preview in items:
                lines.append(f"{name}  {kind}  {preview}")
            send({"text": "\n".join(lines)})

    elif op == "look_at":
        at = req.get("at", "")
        if at in namespace:
            val = namespace[at]
            send({"text": f"{at}: {type(val).__name__} = {repr(val)}"})
        else:
            send({"text": f"{at}: not found"})

    elif op == "complete":
        code = req.get("code", "")
        matches = [k for k in namespace if k.startswith(code)]
        if matches:
            send({"text": "\n".join(f"{m:<20} variable" for m in matches[:50])})
        else:
            send({"text": "No completions."})

    elif op == "status":
        send({"text": "idle\nruntime_version: MockKernel 1.0"})

    elif op == "input":
        # Fire-and-forget for mock
        pass

    else:
        send({"error": f"unknown op: {op}"})
