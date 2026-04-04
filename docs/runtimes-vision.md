# Runtimes vision

rat's kernel protocol — `run`/`look`/`ctl` — isn't a code execution protocol.
It's an **agency protocol**. Anything you can interact with fits into
do/see/control.

This document catalogs the runtimes that would make rat a universal
interface for humans and agents.

---

## The protocol is universal

```
run   = act      (execute code, send message, create resource, trigger action)
look  = perceive (list variables, read messages, show schema, inspect state)
ctl   = manage   (reset, cancel, reconnect, switch context, status)
```

Every runtime below maps to these three operations. Every one is a
~200–400 line kernel script. Every one gives Claude a new capability
AND gives the human a REPL or CLI.

---

## Coding runtimes

### Python, R, Bash, Julia, TypeScript (done or in progress)

The core. Persistent namespace, shared between human and agent.

### SQL

`rat sql@mydb` drops into a SQL REPL. Claude can query your database.

```
run(code)            → execute SQL, return results
look()               → show tables, schema summary
look(at="users")     → describe table (columns, types, row count)
look(code="SEL",.)   → SQL completions (tables, columns, keywords)
ctl(status)          → connection info, current database
ctl(reset)           → reconnect
```

Setup: `rat add pg-analytics --lang sql --env DATABASE_URL=postgres://...`

### Git

`rat git` as a REPL. Claude can stage, commit, push. The human sees it all.

```
run('status')               → git status
run('diff HEAD~3')          → show diff
run('commit -m "fix auth"') → commit
look()                      → repo state (branch, dirty files, recent commits)
look(at="main..feature")    → branch diff
look(code="check",.)        → git subcommand completions
ctl(reset)                  → reset to clean state
```

### Docker / Kubernetes

`rat k8s@staging`. Claude can debug pods, check logs, scale.

```
run('get pods')             → kubectl get pods
run('logs deploy/api')      → kubectl logs
look()                      → cluster state (pods, services, nodes)
look(at="pod/api-xyz")      → describe pod
ctl(status)                 → cluster connection info
```

### SSH

`rat ssh@prod-server`. Tmux pattern. Shared remote shell.

```
kernel.type: tmux
command: ssh user@host
bridge: ssh-bridge.sh        # PROMPT_COMMAND on the remote
frontend.type: tmux
```

The human gets a real shell on the remote host. Claude can run
commands through it. Shared session on a remote machine.

### Make / Task runner

`rat make@myproject`. Claude can build and test.

```
run('build')           → make build
run('test')            → make test
look()                 → available targets + last results
look(at="build")       → build target dependencies and recipe
```

### Jupyter bridge

`rat jupyter@notebook.ipynb`. Connects to a running Jupyter kernel.

```
run(code)              → execute cell in the Jupyter kernel
look()                 → cell outputs, kernel state
ctl(reset)             → restart Jupyter kernel
```

Bridge existing notebooks — Claude can drive them, human keeps Jupyter UI.

---

## Communication runtimes

The agent becomes **social** — it can read, respond, and act on
messages across all your channels.

### The universal messaging pattern

```
run(text)              → send message
run('/reply ...')       → reply to thread/conversation
run('/react 👍')       → react
look()                 → recent messages
look(at="@alice")      → messages from person
look(at="unread")      → unread only
ctl(status)            → connection info, unread count
ctl(reset)             → reconnect / switch conversation
```

### Slack (built)

`rat slack-eng`. Channels as kernels. Messages as run/look.
`/switch #channel`, `/history N`, `/react :emoji:`.

### Email (IMAP/SMTP)

`rat email@work`. Claude can triage your inbox, draft replies.

```
look()                 → inbox (last 10)
look(at="unread")      → unread messages
look(at="@alice")      → messages from alice
run('/reply sounds good, I'll review today')   → reply to last thread
run('/compose alice@co.com: Here's the report...') → new email
run('/search invoice from acme')               → search
run('/forward 1 to accounting@co.com')         → forward
```

### WhatsApp

`rat wa@family`. Via WhatsApp Business API or bridge (whatsmeow).

### Discord

`rat discord@server`. Channels, threads, reactions. Same as Slack pattern.

### Telegram

`rat telegram@group`. Bot API. Same kernel shape.

### Microsoft Teams

`rat teams@engineering`. Microsoft Graph API.

### SMS

`rat sms@+15551234`. Via Twilio. Same pattern.

### Cross-channel workflows

Claude can **read Slack → draft email → send → confirm in Slack**:

```bash
rat run email@work '/search invoice from acme'
→ Found: "Invoice #4521" from billing@acme.co

rat run email@work '/forward 1 to accounting@myco.com: Please process'
→ ✓ forwarded

rat run slack-eng 'Invoice from Acme forwarded to accounting'
→ ✓ sent to #engineering
```

---

## Knowledge runtimes

### Web browser

`rat web`. Claude can research. The human can browse.

```
run('https://docs.python.org/3/...')   → fetch, return readable text
run('/search python asyncio patterns') → web search
look()                                 → open tabs / recent fetches
look(at="tab-3")                       → content of a tab
```

### File search / code search

`rat search@myproject`. Semantic or ripgrep search.

```
run('async handler')                   → search codebase
look()                                 → recent results
look(at="src/auth.py")                 → show file
```

### RAG / documentation

`rat docs@langchain`. Search and retrieve documentation.

```
run('how do I chain prompts?')         → search docs, return sections
look()                                 → available doc sources
look(at="retriever")                   → specific topic
```

### Wikipedia

`rat wiki`. Quick knowledge lookup.

```
run('quantum entanglement')            → summary + key facts
look(at="references")                  → sources
```

---

## Commerce runtimes

The agent becomes your personal buyer, seller, price watcher.

### Shopping (Amazon, etc.)

`rat shop`. Search, compare, add to cart. Human approves purchase.

```
run('/search mechanical keyboard under $100')  → search results
run('/add B09XYZ to cart')                     → add to cart
run('/checkout')                               → ⚠ requires human approval
look()                                         → cart, recent orders
look(at="order-12345")                         → track package
```

The kernel enforces gates — `/add` is fine, `/checkout` requires
human confirmation.

### Stripe

`rat stripe@myapp`. Every SaaS founder wants this.

```
look()                                 → recent payments, MRR, failed charges
look(at="customer cus_xyz")            → customer history
run('/refund pi_abc123')               → process refund
run('/search failed last 7d')          → find issues
```

### Shopify

`rat shopify@mystore`. Run your store from the terminal.

```
look()                                 → orders, inventory
run('/fulfill order 1234')             → ship it
run('/discount 20% off SUMMER2026')    → create promo
look(at="product-xyz")                 → stock levels
```

### Expenses / Receipts

`rat expenses`. Spending intelligence.

```
run('/snap receipt.jpg')               → OCR + categorize
look()                                 → this month's spending
look(at="subscriptions")               → recurring charges
run('/report march')                   → generate expense report
```

### Price watching

`rat prices`. Agent-driven deal finding.

```
run('/watch "RTX 5090" under $800')    → set price alert
look()                                 → active watches
# Agent checks periodically, notifies via slack/email kernel
```

### Banking (read-only)

`rat bank@checking` via Plaid or open banking.

```
look()                                 → balance, recent transactions
look(at="recurring")                   → subscriptions
run('/search coffee last 30d')         → spending patterns
```

Read-only — the agent can analyze but not move money.

---

## Infrastructure runtimes

### AWS / GCP / Cloudflare

`rat aws@prod`. Cloud management from the terminal.

```
run('ec2 describe-instances')          → list instances
run('s3 ls my-bucket/')                → list files
look()                                 → account overview, costs
look(at="lambda my-function")          → function config + recent invocations
```

### Google Drive / Dropbox / S3

`rat gdrive`. File management.

```
look()                                 → recent files
look(at="shared-with-me")             → shared files
run('/download report.pdf')            → fetch file
run('/upload ./results.csv')           → upload
run('/share report.pdf with alice@')   → share
```

### Calendar

`rat gcal`. Schedule management.

```
look()                                 → today's events
look(at="next week")                   → upcoming
run('/create "Team sync" tomorrow 2pm 30min')  → create event
run('/cancel meeting-id')              → cancel
```

---

## Task management runtimes

### Linear / Jira / GitHub Issues

`rat linear@myteam`. Project management as REPL.

```
run('/create bug: Auth fails on Safari')   → create issue
run('/assign LIN-123 to @alice')           → assign
look()                                     → my issues, sprint board
look(at="LIN-123")                         → issue detail
run('/close LIN-123 fixed in PR #45')      → close with comment
```

### Todoist

`rat todo`. Personal task management.

```
run('Buy groceries @errands #p2')      → add task
look()                                 → today's tasks
look(at="@work")                       → work context
run('/done 12345')                     → complete task
```

---

## LLM router

`rat llm` — models as tools. Chain agents.

```bash
rat add llm-claude --lang llm --env LLM_MODEL=claude-sonnet-4
rat add llm-gpt --lang llm --env LLM_MODEL=gpt-5.4
rat add llm-local --lang llm --env LLM_MODEL=ollama/llama3

# Any agent can use any model as a tool
rat run llm-claude 'review this code: ...'
rat run llm-gpt 'generate test cases for: ...'

# Chain: GPT generates, Claude reviews
code=$(rat run llm-gpt 'write a sort function in Python')
rat run llm-claude "review this code: $code"
```

---

## The pattern

Every runtime in this document:
- Is a ~200–400 line kernel script
- Speaks JSON on stdin/stdout (or uses tmux + bridge)
- Maps to `run`/`look`/`ctl`
- Works as a human REPL, a CLI tool, AND an MCP server for agents
- Requires zero Go code — just drop files in a directory

The kernel protocol isn't about code execution.
It's about **agency** — giving humans and AI a shared interface
to everything they interact with.

```
rat = Run AnyThing
```
