#!/usr/bin/env python3
"""Mock kernel for the socket transport and signal interrupts.

The protocol runs on the connection rat asks for (RAT_PROTOCOL_TCP_ADDR);
stdout and stderr are the user's output. `ask(prompt)` reads an answer
through the protocol. SIGINT stops running code only.
"""
import json
import os
import signal
import socket
import sys
import traceback

addr = os.environ["RAT_PROTOCOL_TCP_ADDR"]
host, port = addr.rsplit(":", 1)
sock = socket.create_connection((host, int(port)))
proto_in = sock.makefile("r", encoding="utf-8")
proto_out = sock.makefile("w", encoding="utf-8")

running = False


def send(obj):
    proto_out.write(json.dumps(obj) + "\n")
    proto_out.flush()


def on_interrupt(signum, frame):
    if running:
        raise KeyboardInterrupt


signal.signal(signal.SIGINT, on_interrupt)
send({"op": "protocol_hello", "token": os.environ["RAT_PROTOCOL_TOKEN"]})


def ask(prompt):
    sys.stdout.write(prompt)
    sys.stdout.flush()
    send({"op": "input_request", "prompt": prompt})
    while True:
        req = json.loads(proto_in.readline())
        if req.get("op") == "input":
            send({"op": "input_delivered"})
            return req.get("text", "").rstrip("\n")


namespace = {"ask": ask}

for line in proto_in:
    if not line.strip():
        continue
    req = json.loads(line)
    op = req.get("op")
    if op == "ping":
        send({"ok": True})
    elif op == "shutdown":
        break
    elif op == "run":
        code = req.get("code", "")
        running = True
        try:
            try:
                value = eval(compile(code, "<cell>", "eval"), namespace)
                if value is not None:
                    print(repr(value), flush=True)
            except SyntaxError:
                exec(compile(code, "<cell>", "exec"), namespace)
            sys.stdout.flush()
            result = {"success": True, "output": "", "error": ""}
        except KeyboardInterrupt:
            result = {"success": False, "output": "", "error": "KeyboardInterrupt"}
        except Exception:
            result = {"success": False, "output": "", "error": traceback.format_exc()}
        finally:
            running = False
        send(result)
    elif op == "look_at" and req.get("at") == "__slow__":
        import time
        time.sleep(0.5)
        send({"text": "slow reply"})
    elif op == "look_overview":
        names = [k for k in namespace if not k.startswith("_") and k != "ask"]
        send({"text": "mocksock idle | %d vars" % len(names)})
    elif op == "status":
        send({"text": "idle"})
    elif op == "input":
        pass
    else:
        send({"text": ""})
