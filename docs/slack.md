# Slack runtime

> **Experimental.** The Slack kernel works but the API surface may change.

rat's Slack runtime turns Slack into a REPL. Send messages, read
history, DM users — from your terminal or from an AI agent. With
Socket Mode, inbound messages appear in real time. With the agent
option, an AI kernel auto-replies on your behalf.

---

## Quick start

```bash
rat install slack
```

This opens your browser to create a pre-configured Slack app
(scopes and Socket Mode are pre-filled). You'll:

1. Click **Create** in the Slack app creation page
2. Go to **OAuth & Permissions** → **Install to Workspace** → **Allow**
3. Copy the **Bot User OAuth Token** (`xoxb-...`)
4. Go to **Basic Information** → **App-Level Tokens** → **Generate Token**
   - Name: `rat`, Scope: `connections:write` → Generate
5. Copy the **App-Level Token** (`xapp-...`)
6. Paste both tokens when prompted

After install, invite the bot to a channel:

```
/invite @rat
```

Then:

```bash
rat slack            # REPL
rat run slack 'hello from rat'
rat run slack '/history'
```

---

## REPL commands

| Command | What it does |
|---------|-------------|
| `<text>` | Send a message to the current channel |
| `/dm @user message` | Send a direct message |
| `/history [N]` | Show last N messages (default: 10) |
| `/channels` | List available channels |
| `/switch #channel` | Switch to another channel |
| `/react :emoji:` | React to the last message |
| `/help` | Show commands |

Multi-word display names work: `/dm @Maxime Rivest hey there`

---

## Real-time messages (Socket Mode)

If you provided an app-level token (`xapp-...`) during install,
Socket Mode is enabled automatically. Inbound messages appear
live in the REPL and in `rat tail`:

```
▍Maxime Rivest @#general
▍  hello!

▍Lilly @DM
▍  are you there?
```

No polling. No webhooks. No public URL.

---

## Agent mode

Connect an AI kernel to auto-reply to Slack messages. The Slack
kernel forwards inbound messages to the agent via MCP and sends
the response back to Slack.

### Setup

```bash
# Start a pi kernel (the agent brain)
rat start pi

# Get its MCP URL
rat status -v
# pi@home  running
#   http://127.0.0.1:8724/mcp

# Create the slack bot with the agent option
rat add slack-bot --lang slack --cwd ~ \
  --opt agent=http://127.0.0.1:8724/mcp \
  --env SLACK_BOT_TOKEN=xoxb-... \
  --env SLACK_APP_TOKEN=xapp-...

rat start slack-bot
```

That's it. Messages to the bot in Slack get forwarded to pi.
Pi's response is sent back as a reply. Everything shows in
`rat tail slack-bot`:

```
← Maxime Rivest @DM: what time is it?
→ rat (agent) @DM: I don't have clock access, but you can check with...
```

### How it works

```
Slack user sends DM
  ↓ Socket Mode WebSocket
slack-bot kernel (receives event)
  ↓ MCP tools/call → run(prompt)
pi kernel (generates reply)
  ↓ response text
slack-bot kernel
  ↓ chat.postMessage back to Slack
Slack user sees reply
```

One process. No bridge script. No polling. Managed by
`rat status` / `rat stop` / `rat restart` like any kernel.

### Memory

The agent accumulates context over a conversation. To prevent
hitting the context window limit, the kernel automatically
resets the pi session every 20 exchanges.

On reset, the agent is re-primed with:
- A system prompt defining its behavior
- Persistent memories from `~/.config/rat/slack-bot-memory.md`

**Saving memories:** The agent can include `[REMEMBER]` lines in
its responses to save facts that survive resets:

```
User: my favorite language is R
Bot: Nice! R is great for stats.
     [REMEMBER] Maxime's favorite language is R
```

Users can also send `[REMEMBER]` lines directly:

```
[REMEMBER] standup is at 9am
```

The `[REMEMBER]` lines are stripped before the reply reaches Slack.
Memories persist in a plain text file — view or edit it anytime:

```bash
cat ~/.config/rat/slack-bot-memory.md
```

### Changing the agent

The agent is just any MCP server with a `run` tool. You can
point it at a different pi kernel, a different model, or any
custom runtime:

```bash
# Use a specific model
rat add pi-fast --lang pi --cwd ~ --opt model=claude-sonnet-4-5
rat start pi-fast
# rat status -v → http://127.0.0.1:8725/mcp

# Point slack-bot at it
rat stop slack-bot
rat remove slack-bot --yes
rat add slack-bot --lang slack --cwd ~ \
  --opt agent=http://127.0.0.1:8725/mcp \
  --env SLACK_BOT_TOKEN=xoxb-... \
  --env SLACK_APP_TOKEN=xapp-...
rat start slack-bot
```

### Disabling the agent

Remove the `--opt agent=...` flag. The slack kernel still works
as a passive REPL — you send messages manually, inbound messages
still appear in `rat tail`, but no auto-replies.

---

## Architecture

```
slack-bot kernel (Python subprocess)
├── Slack Web API (urllib, stdlib)
│   ├── chat.postMessage — send messages
│   ├── conversations.history — read history
│   ├── conversations.list — list channels
│   └── users.info — resolve names
├── Socket Mode (stdlib WebSocket client)
│   ├── apps.connections.open — get WSS URL
│   ├── WebSocket connection — receive events
│   └── Envelope acknowledgment
├── Agent (optional, stdlib MCP client)
│   ├── MCP initialize → tools/call run(prompt)
│   ├── Memory extraction ([REMEMBER] lines)
│   └── Session reset after N exchanges
└── Kernel protocol (JSON lines on stdin/stdout)
    ├── run — send message / handle commands
    ├── look — history, user search, completions
    ├── ctl — status, reset
    └── event — inbound messages (pushed to Go)
```

Zero dependencies beyond Python stdlib. No pip packages needed.

---

## Scopes

The Slack app needs these bot token scopes:

| Scope | Why |
|-------|-----|
| `channels:history` | Read channel messages |
| `channels:join` | Auto-join channels |
| `channels:read` | List channels |
| `chat:write` | Send messages |
| `groups:history` | Read private channel messages |
| `groups:read` | List private channels |
| `im:history` | Read DM history |
| `im:read` | List DMs |
| `im:write` | Open DM conversations |
| `mpim:history` | Read group DM history |
| `mpim:read` | List group DMs |
| `reactions:read` | Read reactions |
| `reactions:write` | Add reactions |
| `users:read` | Resolve user names |

Plus one app-level token scope for Socket Mode:
- `connections:write`

`rat install slack` pre-fills all of these via the app manifest.

---

## Troubleshooting

**`not_in_channel`** — The bot needs to join the channel. It
auto-joins on startup, but if that fails, invite it manually:
`/invite @rat`

**"Sending messages to this app has been turned off"** — In your
Slack app settings: **App Home** → **Messages Tab** → check
**"Allow users to send Slash commands and messages from the
messages tab"**. Refresh Slack.

**No inbound messages** — Check that the app-level token
(`xapp-...`) is set and Socket Mode is enabled in the app
settings.

**Agent not replying** — Verify the pi kernel is running:
`rat status -v`. Check the agent URL matches.

**Token errors after restart** — Tokens are stored in the
runtime config (`rat status -v` shows them). If you need to
update them, `rat remove slack-bot --yes` and re-add.
