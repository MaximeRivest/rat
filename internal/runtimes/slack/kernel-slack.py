#!/usr/bin/env python3
"""
rat kernel for Slack — messaging as a REPL.

run(code)           → send message to channel (or /command)
look()              → recent messages + channel info
look(at="@alice")   → messages from a user
look(code="gen",.)  → channel/user completions
ctl(reset)          → switch channel (reads SLACK_CHANNEL again)
ctl(status)         → channel info

Environment:
  SLACK_BOT_TOKEN    Required. Bot token (xoxb-...)
  SLACK_APP_TOKEN    Optional. App-level token (xapp-...) for Socket Mode
  RAT_SLACK_AGENT    Optional. MCP URL of agent kernel for auto-replies
  SLACK_CHANNEL      Channel name or ID (default: general)
  SLACK_HISTORY      Number of messages to show (default: 10)
"""

import hashlib
import base64
import json
import os
import socket
import ssl
import struct
import sys
import threading
import time
import urllib.request
import urllib.error
import urllib.parse
from datetime import datetime

# ── Slack API ────────────────────────────────────────────────

TOKEN = os.environ.get("SLACK_BOT_TOKEN", "")
BASE = "https://slack.com/api"


def _api(method, **kwargs):
    """Call Slack Web API. Returns parsed JSON."""
    url = f"{BASE}/{method}"
    data = urllib.parse.urlencode({k: v for k, v in kwargs.items() if v is not None})
    req = urllib.request.Request(
        url, data=data.encode() if data else None,
        headers={
            "Authorization": f"Bearer {TOKEN}",
            "Content-Type": "application/x-www-form-urlencoded",
        },
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read())
    except Exception as e:
        return {"ok": False, "error": str(e)}


APP_TOKEN = os.environ.get("SLACK_APP_TOKEN", "")
AGENT_URL = os.environ.get("RAT_SLACK_AGENT", "")


# ── Minimal WebSocket client (stdlib only) ───────────────────

class SimpleWebSocket:
    """Minimal RFC 6455 WebSocket client. Supports text frames only."""

    def __init__(self, url, timeout=30):
        self.timeout = timeout
        self._sock = None
        self._connect(url)

    def _connect(self, url):
        # Parse wss://host/path
        assert url.startswith("wss://"), f"only wss:// supported: {url}"
        rest = url[6:]
        slash = rest.find("/")
        if slash == -1:
            host, path = rest, "/"
        else:
            host, path = rest[:slash], rest[slash:]
        port = 443

        raw = socket.create_connection((host, port), timeout=self.timeout)
        ctx = ssl.create_default_context()
        self._sock = ctx.wrap_socket(raw, server_hostname=host)

        # WebSocket handshake
        key = base64.b64encode(os.urandom(16)).decode()
        handshake = (
            f"GET {path} HTTP/1.1\r\n"
            f"Host: {host}\r\n"
            f"Upgrade: websocket\r\n"
            f"Connection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\n"
            f"Sec-WebSocket-Version: 13\r\n"
            f"\r\n"
        )
        self._sock.sendall(handshake.encode())

        # Read HTTP response headers
        resp = b""
        while b"\r\n\r\n" not in resp:
            chunk = self._sock.recv(4096)
            if not chunk:
                raise ConnectionError("WebSocket handshake failed")
            resp += chunk

        status_line = resp.split(b"\r\n")[0]
        if b"101" not in status_line:
            raise ConnectionError(f"WebSocket handshake rejected: {status_line}")

    def recv(self):
        """Read one text frame. Returns str or None on close."""
        try:
            header = self._recv_exact(2)
        except (ConnectionError, OSError):
            return None
        if header is None:
            return None

        opcode = header[0] & 0x0F
        masked = bool(header[1] & 0x80)
        length = header[1] & 0x7F

        if length == 126:
            length = struct.unpack("!H", self._recv_exact(2))[0]
        elif length == 127:
            length = struct.unpack("!Q", self._recv_exact(8))[0]

        if masked:
            mask = self._recv_exact(4)
            data = self._recv_exact(length)
            data = bytes(b ^ mask[i % 4] for i, b in enumerate(data))
        else:
            data = self._recv_exact(length)

        if opcode == 0x8:  # close
            return None
        if opcode == 0x9:  # ping
            self._send_frame(0xA, data)  # pong
            return self.recv()
        if opcode == 0x1:  # text
            return data.decode("utf-8", errors="replace")
        # Binary or other — skip
        return self.recv()

    def send(self, text):
        """Send a text frame (masked, per RFC 6455 client requirement)."""
        self._send_frame(0x1, text.encode())

    def _send_frame(self, opcode, data):
        mask = os.urandom(4)
        masked = bytes(b ^ mask[i % 4] for i, b in enumerate(data))
        header = bytes([0x80 | opcode])
        length = len(data)
        if length < 126:
            header += bytes([0x80 | length])
        elif length < 65536:
            header += bytes([0x80 | 126]) + struct.pack("!H", length)
        else:
            header += bytes([0x80 | 127]) + struct.pack("!Q", length)
        self._sock.sendall(header + mask + masked)

    def close(self):
        try:
            self._send_frame(0x8, b"")
            self._sock.close()
        except Exception:
            pass

    def _recv_exact(self, n):
        buf = b""
        while len(buf) < n:
            chunk = self._sock.recv(n - len(buf))
            if not chunk:
                raise ConnectionError("connection closed")
            buf += chunk
        return buf


# ── Socket Mode ──────────────────────────────────────────────

_socket_mode_thread = None
_socket_mode_ws = None


def _start_socket_mode():
    """Connect to Slack Socket Mode and listen for events in a background thread."""
    global _socket_mode_thread, _socket_mode_ws
    if not APP_TOKEN:
        return  # No app-level token — skip Socket Mode.

    def _run():
        global _socket_mode_ws
        while True:
            try:
                # Get WebSocket URL.
                req = urllib.request.Request(
                    f"{BASE}/apps.connections.open",
                    data=b"",
                    headers={"Authorization": f"Bearer {APP_TOKEN}",
                             "Content-Type": "application/x-www-form-urlencoded"},
                )
                with urllib.request.urlopen(req, timeout=15) as resp:
                    result = json.loads(resp.read())
                if not result.get("ok"):
                    _emit_event("error", {"msg": f"Socket Mode connect failed: {result.get('error', 'unknown')}"})
                    time.sleep(10)
                    continue

                ws_url = result["url"]
                ws = SimpleWebSocket(ws_url, timeout=60)
                _socket_mode_ws = ws

                while True:
                    msg = ws.recv()
                    if msg is None:
                        break  # Disconnected — reconnect.
                    _handle_socket_event(msg)

            except Exception as e:
                _emit_event("error", {"msg": f"Socket Mode error: {e}"})
            finally:
                _socket_mode_ws = None

            # Reconnect after a brief pause.
            time.sleep(3)

    _socket_mode_thread = threading.Thread(target=_run, daemon=True)
    _socket_mode_thread.start()


def _handle_socket_event(raw):
    """Process a Socket Mode envelope."""
    try:
        envelope = json.loads(raw)
    except json.JSONDecodeError:
        return

    # Acknowledge the envelope immediately (required by Socket Mode).
    envelope_id = envelope.get("envelope_id")
    if envelope_id and _socket_mode_ws:
        try:
            _socket_mode_ws.send(json.dumps({"envelope_id": envelope_id}))
        except Exception:
            pass

    evt_type = envelope.get("type")
    if evt_type == "events_api":
        payload = envelope.get("payload", {})
        event = payload.get("event", {})
        _handle_slack_event(event)
    elif evt_type == "disconnect":
        # Slack asks us to reconnect.
        if _socket_mode_ws:
            _socket_mode_ws.close()


def _handle_slack_event(event):
    """Convert a Slack event into a rat kernel event."""
    event_type = event.get("type", "")

    if event_type == "message":
        # Skip bot's own messages and message_changed/deleted subtypes.
        subtype = event.get("subtype", "")
        if subtype in ("bot_message", "message_changed", "message_deleted"):
            return
        if event.get("bot_id"):
            return
        # Skip our own messages.
        if event.get("user") == bot_user_id:
            return

        user = _resolve_user(event.get("user", ""))
        text = event.get("text", "")
        ch = event.get("channel", "")

        # Resolve channel name.
        ch_name = ch
        if ch.startswith("C"):
            for name, cid in channels_cache.items():
                if cid == ch:
                    ch_name = f"#{name}"
                    break
        elif ch.startswith("D"):
            ch_name = "DM"

        # Replace user mentions.
        import re
        def replace_mention(m):
            uid = m.group(1)
            return f"@{_resolve_user(uid)}"
        text = re.sub(r"<@(U[A-Z0-9]+)>", replace_mention, text)

        _emit_event("message", {
            "from": user,
            "text": text,
            "channel": ch_name,
        })

        # Forward to agent if configured.
        if AGENT_URL:
            _forward_to_agent(user, text, ch_name, ch)


# ── Agent memory ────────────────────────────────────────────

_agent_exchange_count = 0
_AGENT_MAX_EXCHANGES = 20  # reset pi session after this many exchanges
_MEMORY_PATH = os.path.expanduser("~/.config/rat/slack-bot-memory.md")

_AGENT_SYSTEM_PROMPT = """You are a Slack bot powered by rat. You receive messages from Slack users and reply concisely.

Rules:
- Be helpful, concise, and friendly.
- If a message doesn't need a reply (e.g. "ok", "thanks", reactions), respond with exactly: NO_REPLY
- You can remember things about users. To save a memory, include a line like:
  [REMEMBER] Alice prefers morning meetings
  [REMEMBER] Project X deadline is March 15
- Your memories persist across conversation resets.

{memory}"""


def _load_memory():
    """Load persistent memory from disk."""
    try:
        with open(_MEMORY_PATH, "r") as f:
            return f.read().strip()
    except (FileNotFoundError, OSError):
        return ""


def _save_memory_items(text):
    """Extract [REMEMBER] lines from agent output and append to memory file."""
    lines = text.split("\n")
    new_memories = []
    for line in lines:
        stripped = line.strip()
        if stripped.startswith("[REMEMBER]"):
            fact = stripped[len("[REMEMBER]"):].strip()
            if fact:
                new_memories.append(fact)
    if not new_memories:
        return
    os.makedirs(os.path.dirname(_MEMORY_PATH), exist_ok=True)
    with open(_MEMORY_PATH, "a") as f:
        for fact in new_memories:
            f.write(f"- {fact}\n")
    _emit_event("progress", {"msg": f"remembered {len(new_memories)} fact(s)"})


def _strip_remember_lines(text):
    """Remove [REMEMBER] lines from text before sending to Slack."""
    return "\n".join(
        line for line in text.split("\n")
        if not line.strip().startswith("[REMEMBER]")
    ).strip()


def _agent_reset():
    """Reset the agent session and re-prime with system prompt + memory."""
    global _agent_exchange_count
    _agent_exchange_count = 0

    # Clear MCP session so we get a fresh one.
    _mcp_sessions.pop(AGENT_URL, None)

    try:
        _mcp_ensure_session(AGENT_URL)
        # Reset the pi kernel.
        _mcp_ctl(AGENT_URL, "reset")
        time.sleep(1)
        # Re-prime with system prompt + memory.
        memory = _load_memory()
        memory_section = f"Your memories:\n{memory}" if memory else "No memories yet."
        system = _AGENT_SYSTEM_PROMPT.format(memory=memory_section)
        _mcp_run(AGENT_URL, system)
        _emit_event("progress", {"msg": "agent session reset (memory preserved)"})
    except Exception as e:
        _emit_event("error", {"msg": f"agent reset failed: {e}"})


def _forward_to_agent(from_user, text, ch_name, raw_channel):
    """Forward an inbound message to the agent kernel and send the reply back."""
    global _agent_exchange_count

    # Reset if we've hit the exchange limit.
    if _agent_exchange_count >= _AGENT_MAX_EXCHANGES:
        _agent_reset()

    # Prime on first exchange.
    if _agent_exchange_count == 0 and AGENT_URL not in _mcp_sessions:
        _agent_reset()

    # Extract [REMEMBER] from user input too (explicit memory requests).
    _save_memory_items(text)

    prompt = (
        f'Slack message from {from_user} in {ch_name}: "{text}" '
        f'\u2014 Write a short reply. To save a fact for later, include a line starting with [REMEMBER]. '
        f'If no reply is needed, respond with exactly: NO_REPLY'
    )
    try:
        reply = _mcp_run(AGENT_URL, prompt)
    except Exception as e:
        _emit_event("error", {"msg": f"agent error: {e}"})
        return

    _agent_exchange_count += 1

    if not reply or "NO_REPLY" in reply:
        return

    # Extract and save any [REMEMBER] lines from agent output.
    _save_memory_items(reply)
    reply = _strip_remember_lines(reply)

    if not reply:
        return

    # Send reply back to Slack.
    if raw_channel.startswith("D"):
        ok, err = _send_message(reply, target=raw_channel)
    else:
        ok, err = _send_message(reply, target=raw_channel)

    if ok:
        _emit_event("message", {
            "from": "rat (agent)",
            "text": reply,
            "channel": ch_name,
        })
    else:
        _emit_event("error", {"msg": f"agent reply failed: {err}"})


# ── Minimal MCP client ──────────────────────────────────────

_mcp_sessions = {}  # url → session_id (after initialize)


def _mcp_call(url, method, params=None):
    """Make a JSON-RPC call to an MCP server."""
    import random
    req_id = random.randint(1, 999999)
    body = {
        "jsonrpc": "2.0",
        "id": req_id,
        "method": method,
        "params": params or {},
    }
    data = json.dumps(body).encode()
    headers = {"Content-Type": "application/json", "Accept": "application/json"}

    # Include session ID if we have one.
    session_id = _mcp_sessions.get(url)
    if session_id:
        headers["Mcp-Session-Id"] = session_id

    req = urllib.request.Request(url, data=data, headers=headers)
    with urllib.request.urlopen(req, timeout=120) as resp:
        # Capture session ID from response.
        sid = resp.headers.get("Mcp-Session-Id")
        if sid:
            _mcp_sessions[url] = sid
        return json.loads(resp.read())


def _mcp_ensure_session(url):
    """Initialize an MCP session if we don't have one."""
    if url in _mcp_sessions:
        return
    result = _mcp_call(url, "initialize", {
        "protocolVersion": "2025-03-26",
        "capabilities": {},
        "clientInfo": {"name": "rat-slack-agent", "version": "0.1.0"},
    })
    # Send initialized notification.
    notif = {"jsonrpc": "2.0", "method": "notifications/initialized"}
    data = json.dumps(notif).encode()
    headers = {"Content-Type": "application/json"}
    session_id = _mcp_sessions.get(url)
    if session_id:
        headers["Mcp-Session-Id"] = session_id
    req = urllib.request.Request(url, data=data, headers=headers)
    try:
        urllib.request.urlopen(req, timeout=10)
    except Exception:
        pass  # Notifications don't return a response body.


def _mcp_run(url, code):
    """Call the 'run' tool on an MCP server. Returns the text output."""
    _mcp_ensure_session(url)
    result = _mcp_call(url, "tools/call", {
        "name": "run",
        "arguments": {"code": code},
    })
    return _extract_mcp_text(result)


def _mcp_ctl(url, op):
    """Call the 'ctl' tool on an MCP server."""
    _mcp_ensure_session(url)
    result = _mcp_call(url, "tools/call", {
        "name": "ctl",
        "arguments": {"op": op},
    })
    return _extract_mcp_text(result)


def _extract_mcp_text(result):
    """Extract text content from an MCP tool result."""
    content = result.get("result", {}).get("content", [])
    texts = []
    for item in content:
        if item.get("type") == "text":
            texts.append(item.get("text", ""))
    return "\n".join(texts).strip()


def _emit_event(event_type, data):
    """Emit a kernel event on stdout (picked up by Go's reader loop)."""
    send({"op": "event", "type": event_type, "data": data})


# ── State ────────────────────────────────────────────────────

channel_id = ""
channel_name = ""
channels_cache = {}      # name → id
users_cache = {}         # id → display_name
bot_user_id = ""
message_count = 0


def resolve_channel(name_or_id):
    """Resolve a channel name or ID to (id, name)."""
    if name_or_id.startswith("C") and len(name_or_id) > 8:
        # Looks like a channel ID already.
        info = _api("conversations.info", channel=name_or_id)
        if info.get("ok"):
            ch = info["channel"]
            return ch["id"], ch.get("name", name_or_id)
        return name_or_id, name_or_id

    # Strip leading #.
    name = name_or_id.lstrip("#")

    # Search in cache first.
    if name in channels_cache:
        return channels_cache[name], name

    # List channels and find it.
    _refresh_channels()
    if name in channels_cache:
        return channels_cache[name], name

    return "", name


def _refresh_channels():
    """Refresh the channel list cache."""
    cursor = ""
    for _ in range(10):  # max 10 pages
        resp = _api("conversations.list",
                     types="public_channel,private_channel",
                     exclude_archived="true",
                     limit="200",
                     cursor=cursor or None)
        if not resp.get("ok"):
            break
        for ch in resp.get("channels", []):
            channels_cache[ch["name"]] = ch["id"]
        cursor = resp.get("response_metadata", {}).get("next_cursor", "")
        if not cursor:
            break


def _resolve_user(user_id):
    """Resolve user ID to display name."""
    if user_id in users_cache:
        return users_cache[user_id]
    resp = _api("users.info", user=user_id)
    if resp.get("ok"):
        u = resp["user"]
        name = u.get("profile", {}).get("display_name") or u.get("real_name") or u.get("name", user_id)
        users_cache[user_id] = name
        return name
    users_cache[user_id] = user_id
    return user_id


def _format_ts(ts_str):
    """Format Slack timestamp to human-readable."""
    try:
        ts = float(ts_str)
        dt = datetime.fromtimestamp(ts)
        now = datetime.now()
        diff = now - dt
        if diff.total_seconds() < 60:
            return f"{int(diff.total_seconds())}s ago"
        elif diff.total_seconds() < 3600:
            return f"{int(diff.total_seconds() / 60)}m ago"
        elif diff.total_seconds() < 86400:
            return f"{int(diff.total_seconds() / 3600)}h ago"
        else:
            return dt.strftime("%b %d %H:%M")
    except (ValueError, TypeError):
        return ts_str


def _format_message(msg):
    """Format a single Slack message for display."""
    user_id = msg.get("user", "")
    user = _resolve_user(user_id) if user_id else msg.get("username", "bot")
    text = msg.get("text", "")
    ts = _format_ts(msg.get("ts", ""))

    # Replace user mentions <@U123> with names.
    import re
    def replace_mention(m):
        uid = m.group(1)
        return f"@{_resolve_user(uid)}"
    text = re.sub(r"<@(U[A-Z0-9]+)>", replace_mention, text)

    # Replace channel mentions <#C123|name> with #name.
    text = re.sub(r"<#C[A-Z0-9]+\|([^>]+)>", r"#\1", text)

    # Replace URLs.
    text = re.sub(r"<(https?://[^|>]+)(?:\|[^>]*)?>", r"\1", text)

    # Attachments.
    attachments = []
    for att in msg.get("attachments", []):
        if att.get("text"):
            attachments.append(f"  📎 {att['text'][:100]}")
        elif att.get("fallback"):
            attachments.append(f"  📎 {att['fallback'][:100]}")

    # Files.
    for f in msg.get("files", []):
        attachments.append(f"  📁 {f.get('name', 'file')} ({f.get('mimetype', '')})")

    # Reactions.
    reactions = ""
    if msg.get("reactions"):
        emojis = " ".join(f":{r['name']}:{r['count']}" for r in msg["reactions"])
        reactions = f"  [{emojis}]"

    line = f"  @{user} ({ts}): {text}{reactions}"
    if attachments:
        line += "\n" + "\n".join(attachments)
    return line


def _get_history(count=None):
    """Get recent messages from the channel."""
    n = count or int(os.environ.get("SLACK_HISTORY", "10"))
    resp = _api("conversations.history", channel=channel_id, limit=str(n))
    if not resp.get("ok"):
        return f"Error: {resp.get('error', 'unknown')}"
    messages = resp.get("messages", [])
    messages.reverse()  # oldest first
    if not messages:
        return f"#{channel_name} — no messages"
    lines = [f"#{channel_name} — last {len(messages)} messages\n"]
    for msg in messages:
        lines.append(_format_message(msg))
    return "\n".join(lines)


def _send_message(text, target=None):
    """Send a message to a channel or DM."""
    global message_count
    ch = target or channel_id
    resp = _api("chat.postMessage", channel=ch, text=text)
    if resp.get("ok"):
        message_count += 1
        return True, "sent"
    err = resp.get("error", "unknown error")
    if err == "not_in_channel":
        # Try to join the channel first, then retry.
        join = _api("conversations.join", channel=ch)
        if join.get("ok"):
            resp = _api("chat.postMessage", channel=ch, text=text)
            if resp.get("ok"):
                message_count += 1
                return True, "sent"
            err = resp.get("error", "unknown error")
    return False, err


def _open_dm(user_identifier):
    """Open a DM with a user. Accepts @name or user ID."""
    user_id = _resolve_user_id(user_identifier)
    if not user_id:
        return "", f"user not found: {user_identifier}"
    resp = _api("conversations.open", users=user_id)
    if resp.get("ok"):
        return resp["channel"]["id"], ""
    return "", resp.get("error", "unknown error")


def _resolve_user_id(name_or_id):
    """Resolve a display name or @mention to a user ID.

    Tries exact match first, then case-insensitive prefix/substring.
    Works with full names ('Maxime Rivest') and partial ('Maxime').
    """
    name_or_id = name_or_id.lstrip("@")
    # If it looks like a user ID already, return it.
    if name_or_id.startswith("U") and len(name_or_id) > 8 and name_or_id.isalnum():
        return name_or_id

    def _search(query):
        q = query.lower()
        # Exact match.
        for uid, uname in users_cache.items():
            if uname.lower() == q:
                return uid
        # Prefix match (first name or start of display name).
        for uid, uname in users_cache.items():
            if uname.lower().startswith(q):
                return uid
        # Substring match.
        for uid, uname in users_cache.items():
            if q in uname.lower():
                return uid
        return ""

    result = _search(name_or_id)
    if result:
        return result
    # Fetch user list and retry.
    _refresh_users()
    return _search(name_or_id)


def _parse_dm_args(arg):
    """Parse '/dm @User Name message text' into (user, message).

    Strategy: try progressively longer user names (greedy) until one
    resolves. Fall back to first-word split.
    """
    words = arg.split()
    if len(words) < 2:
        return arg, ""

    # Ensure user cache is populated.
    if not users_cache:
        _refresh_users()

    # Try longest-first: "/dm @Maxime Rivest hello" → try "Maxime Rivest",
    # then "Maxime". The remaining words are the message.
    for i in range(len(words) - 1, 0, -1):
        candidate = " ".join(words[:i]).lstrip("@")
        uid = _resolve_user_id(candidate)
        if uid:
            return candidate, " ".join(words[i:])

    # Fall back to first word = user, rest = message.
    return words[0], " ".join(words[1:])


def _refresh_users():
    """Refresh the users cache."""
    cursor = ""
    for _ in range(10):
        resp = _api("users.list", limit="200", cursor=cursor or None)
        if not resp.get("ok"):
            break
        for u in resp.get("members", []):
            if u.get("deleted") or u.get("is_bot"):
                continue
            name = u.get("profile", {}).get("display_name") or u.get("real_name") or u.get("name", "")
            if name:
                users_cache[u["id"]] = name
        cursor = resp.get("response_metadata", {}).get("next_cursor", "")
        if not cursor:
            break


# ── Protocol ─────────────────────────────────────────────────

_send_lock = threading.Lock()


def send(obj):
    with _send_lock:
        sys.stdout.write(json.dumps(obj) + "\n")
        sys.stdout.flush()


def handle_run(code):
    """Send a message or handle /commands."""
    code = code.strip()
    if not code:
        return {"success": True, "output": "", "vars": message_count}

    # /commands
    if code.startswith("/"):
        return handle_command(code)

    # Regular text → send as message.
    ok, msg = _send_message(code)
    if ok:
        return {"success": True, "output": f"✓ sent to #{channel_name}", "vars": message_count}
    return {"success": False, "output": "", "error": f"send failed: {msg}", "vars": message_count}


def _switch_channel(new_id, new_name):
    global channel_id, channel_name
    channel_id, channel_name = new_id, new_name
    history = _get_history(5)
    return {"success": True, "output": f"Switched to #{new_name}\n\n{history}", "vars": message_count}


def handle_command(code):
    """Handle /commands."""
    parts = code.split(None, 1)
    cmd = parts[0].lower()
    arg = parts[1].strip() if len(parts) > 1 else ""

    if cmd in ("/history", "/h"):
        count = int(arg) if arg.isdigit() else None
        return {"success": True, "output": _get_history(count), "vars": message_count}

    elif cmd in ("/channels", "/ch"):
        _refresh_channels()
        if not channels_cache:
            return {"success": True, "output": "No channels found", "vars": message_count}
        lines = ["Available channels:\n"]
        for name, cid in sorted(channels_cache.items()):
            marker = " ◀" if cid == channel_id else ""
            lines.append(f"  #{name}{marker}")
        return {"success": True, "output": "\n".join(lines), "vars": message_count}

    elif cmd in ("/switch", "/join", "/j"):
        if not arg:
            return {"success": False, "error": "usage: /switch #channel", "vars": message_count}
        new_id, new_name = resolve_channel(arg)
        if not new_id:
            return {"success": False, "error": f"channel not found: {arg}", "vars": message_count}
        return _switch_channel(new_id, new_name)

    elif cmd in ("/dm",):
        # DM a user: /dm @Maxime Rivest hey there
        # Greedy match: try longest possible user name, shortest message.
        if not arg:
            return {"success": False, "error": "usage: /dm @user message", "vars": message_count}
        user_target, dm_text = _parse_dm_args(arg)
        if not dm_text:
            return {"success": False, "error": "usage: /dm @user message", "vars": message_count}
        dm_id, err = _open_dm(user_target)
        if not dm_id:
            return {"success": False, "error": f"could not open DM: {err}", "vars": message_count}
        ok, msg = _send_message(dm_text, target=dm_id)
        if ok:
            return {"success": True, "output": f"✓ DM sent to {user_target}", "vars": message_count}
        return {"success": False, "error": f"DM failed: {msg}", "vars": message_count}

    elif cmd in ("/thread", "/t"):
        # TODO: thread support
        return {"success": False, "error": "/thread not yet implemented", "vars": message_count}

    elif cmd in ("/react", "/r"):
        # React to the last message.
        if not arg:
            return {"success": False, "error": "usage: /react :emoji:", "vars": message_count}
        emoji = arg.strip(":")
        resp = _api("conversations.history", channel=channel_id, limit="1")
        if resp.get("ok") and resp.get("messages"):
            ts = resp["messages"][0]["ts"]
            r = _api("reactions.add", channel=channel_id, name=emoji, timestamp=ts)
            if r.get("ok"):
                return {"success": True, "output": f":{emoji}: added", "vars": message_count}
            return {"success": False, "error": r.get("error", "unknown"), "vars": message_count}
        return {"success": False, "error": "no messages to react to", "vars": message_count}

    elif cmd in ("/help", "/?"):
        return {"success": True, "output": """Slack kernel commands:

  <text>              Send a message to current channel
  /dm @user message   Send a direct message
  /history [N]        Show last N messages (default: 10)
  /channels           List available channels
  /switch #channel    Switch to another channel
  /react :emoji:      React to the last message
  /help               This help""", "vars": message_count}

    else:
        return {"success": False, "error": f"unknown command: {cmd}. Try /help", "vars": message_count}


def handle_look_overview():
    """Show recent messages."""
    return {"text": _get_history()}


def handle_look_at(target):
    """Show messages from a user or about a topic."""
    target = target.lstrip("@")
    # Get recent history and filter.
    resp = _api("conversations.history", channel=channel_id, limit="50")
    if not resp.get("ok"):
        return {"text": f"Error: {resp.get('error', 'unknown')}"}
    messages = resp.get("messages", [])
    messages.reverse()

    # Filter by user.
    matching = []
    for msg in messages:
        user = _resolve_user(msg.get("user", ""))
        if target.lower() in user.lower():
            matching.append(msg)

    if not matching:
        return {"text": f"No messages from @{target} in recent history"}

    lines = [f"Messages from @{target} in #{channel_name}:\n"]
    for msg in matching[-10:]:
        lines.append(_format_message(msg))
    return {"text": "\n".join(lines)}


def handle_complete(code, cursor):
    """Complete channel names or @mentions."""
    # Find the word being typed.
    text_before = code[:cursor]
    word_start = cursor
    while word_start > 0 and code[word_start - 1] not in " \t\n":
        word_start -= 1
    word = text_before[word_start:]

    if word.startswith("#"):
        # Channel completion.
        prefix = word[1:]
        _refresh_channels()
        matches = [f"#{n}" for n in channels_cache if n.startswith(prefix)]
        if matches:
            return {"text": "\n".join(f"{m:20s} channel" for m in matches[:20])}

    elif word.startswith("@"):
        # User completion (from cached users).
        prefix = word[1:].lower()
        matches = [f"@{name}" for name in users_cache.values()
                   if name.lower().startswith(prefix)]
        if matches:
            return {"text": "\n".join(f"{m:20s} user" for m in matches[:20])}

    elif word.startswith("/"):
        # Command completion.
        cmds = ["/history", "/channels", "/switch", "/react", "/help"]
        matches = [c for c in cmds if c.startswith(word)]
        if matches:
            return {"text": "\n".join(f"{c:20s} command" for c in matches)}

    return {"text": "No completions."}


# ── Main loop ────────────────────────────────────────────────

_initialized = False
_init_error = ""


def ensure_init():
    """Lazy init: connect to Slack on first real operation, not on ping."""
    global _initialized, _init_error, channel_id, channel_name, bot_user_id

    if _initialized:
        if _init_error:
            return {"success": False, "error": _init_error}
        return None
    _initialized = True

    if not TOKEN:
        _init_error = "SLACK_BOT_TOKEN not set. Export it and restart: rat restart slack"
        return {"success": False, "error": _init_error}

    # Verify token and get bot user ID.
    auth = _api("auth.test")
    if not auth.get("ok"):
        _init_error = f"auth failed: {auth.get('error', 'invalid token')}"
        return {"success": False, "error": _init_error}
    bot_user_id = auth.get("user_id", "")

    # Resolve channel.
    ch = os.environ.get("SLACK_CHANNEL", "general")
    cid, cname = resolve_channel(ch)
    if not cid:
        # Try to find any channel we can access.
        _refresh_channels()
        if "general" in channels_cache:
            cid, cname = channels_cache["general"], "general"
        elif channels_cache:
            cname = next(iter(channels_cache))
            cid = channels_cache[cname]
        else:
            _init_error = f"channel '{ch}' not found and no channels accessible"
            return {"success": False, "error": _init_error}

    channel_id, channel_name = cid, cname

    # Auto-join the channel so we can post without manual /invite.
    _api("conversations.join", channel=channel_id)

    # Start Socket Mode for real-time inbound messages.
    _start_socket_mode()

    return None


for line in sys.stdin:
    line = line.strip()
    if not line:
        continue

    try:
        req = json.loads(line)
    except json.JSONDecodeError:
        send({"success": False, "error": "invalid json"})
        continue

    op = req.get("op", "")

    if op == "ping":
        send({"ok": True})

    elif op == "run":
        err = ensure_init()
        if err:
            send(err)
        else:
            send(handle_run(req.get("code", "")))

    elif op == "look_overview":
        err = ensure_init()
        if err:
            send({"text": err["error"]})
        else:
            send(handle_look_overview())

    elif op == "look_at":
        err = ensure_init()
        if err:
            send({"text": err["error"]})
        else:
            send(handle_look_at(req.get("at", "")))

    elif op == "complete":
        err = ensure_init()
        if err:
            send({"text": "No completions."})
        else:
            send(handle_complete(req.get("code", ""), req.get("cursor", 0)))

    elif op == "status":
        if _initialized and not _init_error:
            state = f"idle\nruntime_version: Slack #{channel_name}"
        elif _init_error:
            state = f"error\n{_init_error}"
        else:
            state = "idle\nruntime_version: Slack (not connected)"
        send({"text": state})

    elif op == "shutdown":
        break

    else:
        send({"error": f"unknown op: {op}"})
