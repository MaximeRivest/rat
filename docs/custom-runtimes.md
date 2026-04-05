# Custom runtimes

rat ships with Python and bash built-in. R ships as a built-in
generic runtime. You can add any language by dropping two files
in a directory.

## How it works

A runtime is a directory with:

```
runtime.yaml     # tells rat how to find and start the runtime
kernel.R         # script that speaks the rat kernel protocol
```

rat looks for runtimes in this order:

1. **User-defined:** `~/.config/rat/runtimes/<lang>/runtime.yaml`
2. **Built-in:** embedded in the rat binary

User-defined runtimes win — you can override built-ins.

## Quick start

### 1. Create the directory

```bash
mkdir -p ~/.config/rat/runtimes/ruby
```

### 2. Write runtime.yaml

```yaml
name: ruby
display: Ruby

detect:
  commands: [ruby]      # search PATH for these
  env: RAT_RUBY         # env var override

kernel:
  script: kernel.rb     # path relative to this file
  args: []              # extra args before the script
```

### 3. Write the kernel script

Your kernel reads JSON lines from stdin, writes JSON lines to
stdout. It must handle these operations:

| Op | What to do |
|---|---|
| `ping` | Respond `{"ok": true}` |
| `run` | Execute code, respond with `{success, output, error, vars}` |
| `look_overview` | List variables, respond with `{"text": "..."}` |
| `look_at` | Inspect a symbol, respond with `{"text": "..."}` |
| `complete` | Code completions, respond with `{"text": "..."}` |
| `status` | Respond `{"text": "idle"}` (or `busy`, `waiting_for_input`) |
| `shutdown` | Exit cleanly |

See [KERNEL-PROTOCOL.md](../KERNEL-PROTOCOL.md) for the full spec.

A minimal kernel:

```ruby
#!/usr/bin/env ruby
require 'json'

binding_ctx = binding

STDIN.each_line do |line|
  req = JSON.parse(line.strip)
  case req["op"]
  when "ping"
    puts JSON.generate({ ok: true })
  when "run"
    begin
      result = eval(req["code"], binding_ctx)
      output = result.nil? ? "" : result.inspect
      puts JSON.generate({ success: true, output: output, error: "" })
    rescue => e
      puts JSON.generate({ success: false, output: "", error: e.message })
    end
  when "look_overview"
    puts JSON.generate({ text: "ruby idle | 0 vars" })
  when "look_at"
    puts JSON.generate({ text: "#{req['at']}: not found" })
  when "complete"
    puts JSON.generate({ text: "No completions." })
  when "status"
    puts JSON.generate({ text: "idle\nruntime_version: Ruby #{RUBY_VERSION}" })
  when "shutdown"
    exit 0
  end
  STDOUT.flush
end
```

### 4. Use it

```bash
rat run ruby 'puts "hello from rat"'
# hello from rat
# ✓ 3ms
```

That's it. No Go code. No recompilation. Same MCP server, same
CLI, same Claude Desktop integration as Python.

## runtime.yaml reference

```yaml
name: ruby                    # language identifier (used in rat <lang>)
display: Ruby                 # human-readable name (used in errors, status)

detect:
  commands: [ruby, ruby3.2]   # binaries to search PATH, first match wins
  env: RAT_RUBY               # env var that overrides PATH detection

kernel:
  script: kernel.rb           # kernel script, relative to runtime.yaml
  args: ["--disable-gems"]    # args passed to binary BEFORE the script

# Future (not yet implemented):
frontend:
  command: irb                # REPL command for rat <lang>
  fallback: ruby -e "..."    # fallback if command not found

install:
  deps: [irb, solargraph]    # packages to install
  env_manager: bundler        # environment type
```

## Overriding built-in runtimes

rat's built-in R runtime ships in the binary. To customize it:

```bash
# Extract the built-in as a starting point
cp -r ~/.cache/rat/runtimes/r ~/.config/rat/runtimes/r

# Edit to taste
vim ~/.config/rat/runtimes/r/kernel.R
```

Your version in `~/.config/` takes priority over the built-in.

## Tips

- **stdout is the protocol channel.** Your kernel must only write
  JSON lines to stdout. Use stderr for debug logging.

- **State persists.** Variables from one `run` must be visible in
  the next. Use a persistent namespace/environment.

- **`vars` is optional.** If you can count user-visible variables,
  return it in `run` responses. Otherwise omit it.

- **Completions are optional.** Returning `"No completions."` is
  fine. Add real completions when you want a better REPL experience.

- **Test with `rat serve`:**
  ```bash
  rat serve ruby --http --port 9000
  # Then in another terminal:
  rat run ruby 'puts 1 + 1'
  ```

## Frontend styling guide

If you write a custom REPL frontend (using prompt_toolkit, etc.),
follow these rules so your colors work on any terminal theme:

### Use ANSI 16 colors, not RGB

```python
# ✗ Bad — hardcoded RGB bypasses the terminal theme.
#   Invisible on some backgrounds, wrong on others.
BLUE = "\033[38;2;108;158;248m"

# ✓ Good — terminal theme maps this to a readable blue.
BLUE = "\033[34m"
```

The 16 ANSI colors (black, red, green, yellow, blue, magenta,
cyan, white × normal/bright) are remapped by every terminal
theme. Solarized picks Solarized shades. Dracula picks Dracula
shades. Light themes pick dark-enough-on-white shades.

### Use attributes for emphasis

```python
BOLD = "\033[1m"       # emphasis
DIM  = "\033[2m"       # de-emphasis (activity, hints)
R    = "\033[0m"       # reset
```

`bold`, `dim`, `italic`, `underline`, and `reverse` work on
every terminal and adapt to the theme.

### prompt_toolkit: use ansi* names

```python
Style.from_dict({
    # ✗ Bad
    "prompt": "#6c9ef8 bold",
    # ✓ Good
    "prompt": "ansiblue bold",
    # ✓ reverse video always contrasts with the background
    "bottom-toolbar": "reverse",
})
```

### Helper module

rat ships `rat_frontend.py` with pre-built constants and helpers:

```python
from rat_frontend import ansi, style, format_activity

# ANSI escapes
print(f"{ansi.BLUE}hello{ansi.R}")
print(ansi.err("something failed"))   # red
print(ansi.dim("muted hint"))          # dim

# prompt_toolkit style dict
from prompt_toolkit.styles import Style
session = PromptSession(style=Style.from_dict(style.PROMPT_TOOLKIT))

# Format activity entries from the shared session watcher
print(format_activity(entries))
```

See `internal/runtimes/rat_frontend.py` for the full API.
