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
  SLACK_CHANNEL      Channel name or ID (default: general)
  SLACK_HISTORY      Number of messages to show (default: 10)
"""

import json
import os
import sys
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

def send(obj):
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
