# User config: `~/.config/rat/config.yaml`

The config file is **optional**. If it's missing, rat uses the defaults
documented below. It controls REPL presentation and history behaviour
for every language — Python, R, Julia, Bash, and generic runtimes all
read from the same file.

The file lives at the OS-native config dir:

- Linux / BSD: `~/.config/rat/config.yaml`
- macOS:       `~/Library/Application Support/rat/config.yaml`
- Windows:     `%AppData%\rat\config.yaml`

## Shape

Hierarchical: a top-level `repl:` block sets defaults that apply to
every language. Per-language sections (`py`, `r`, `jl`, `js`, `sh`, or
the full names `python`, `julia`, `bash`, `shell`) override the
defaults for that language only.

```yaml
# Defaults for every rat REPL.
repl:
  activity:
    # How many code lines to show per "activity" entry when another
    # client runs code. 0 = unlimited.
    max_code_lines: 0
    # How many output lines to show. 0 = unlimited.
    max_output_lines: 100
  history:
    # When true, the up-arrow cycles through every execution in this
    # kernel (not just the ones typed in the current REPL). The REPL
    # seeds its history from ~/.cache/rat/kernels/<name>/activity.jsonl
    # at startup.
    seed_from_runtime: true
    # Max entries to seed from the activity log. 0 = unlimited.
    seed_limit: 0

# Per-language overrides — only the fields you list take effect, the
# rest inherit from `repl:` above.
py:
  activity:
    max_output_lines: 200   # show more output for Python specifically

r:
  history:
    seed_from_runtime: false
```

## Built-in defaults

| Field                         | Default |
|-------------------------------|---------|
| `activity.max_code_lines`     | `0` (unlimited) |
| `activity.max_output_lines`   | `100` |
| `history.seed_from_runtime`   | `true` |
| `history.seed_limit`          | `0` (unlimited) |

## Notes

- A missing file, a malformed file, or a missing field all fall back
  to the built-in default. A malformed file prints one warning to
  stderr and continues.
- Language keys accept both canonical rat names (`py`, `r`, `jl`,
  `js`, `sh`) and the long forms (`python`, `julia`, `bash`,
  `shell`). If you write both, the canonical name wins.
- Values are re-read each time you launch a REPL — no restart needed
  after editing.

## Debug escape hatch

Inside any `rat py` REPL, typing `:ratdebug` dumps the frontend-side
state: which IPython shell is running, what's in the per-kernel
SQLite history, and what prompt_toolkit's up-arrow buffer currently
holds. The dump is also written to `/tmp/rat-debug.txt` (override
with `:ratdebug /path/to/file`).

Use it when history seeding, completions, or toolbar state look wrong.
The command runs locally in the frontend process, never touches the
kernel, and is not saved to user history.
