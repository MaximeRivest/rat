"""
rat_frontend — shared styling helpers for rat REPL frontends.

Terminal-safe color scheme using ANSI 16 colors + attributes.
Terminal themes control the actual RGB values, so these are readable
on any background (dark, light, Solarized, Dracula, etc.).

Usage in a custom frontend:

    from rat_frontend import style, ansi, format_activity

    # prompt_toolkit style dict — merge into your Style.from_dict()
    session = PromptSession(style=Style.from_dict(style.PROMPT_TOOLKIT))

    # ANSI escape helpers for raw print()
    print(f"{ansi.BLUE}hello{ansi.R}")
    print(f"{ansi.err('something failed')}")
    print(f"{ansi.dim('muted text')}")

    # Format activity entries from the watcher
    print(format_activity(entries))

Design rules for extension builders:
    1. Never use RGB escapes (\\033[38;2;R;G;Bm). They bypass the
       terminal theme and break on some backgrounds.
    2. Use the ANSI 16 palette: red, green, yellow, blue, magenta,
       cyan, white, and their bright variants. The terminal theme
       maps these to readable colors.
    3. Use attributes for emphasis: bold, dim, italic, underline,
       reverse. These work everywhere.
    4. For prompt_toolkit styles, use ansi* names: ansiblue, ansired,
       ansigray, etc. Never hex (#rrggbb) for semantic elements.
    5. reverse video is the safest way to highlight — it always
       contrasts with the background, whatever it is.
"""


# ── ANSI 16 escape sequences ────────────────────────────────

class ansi:
    """ANSI 16-color escapes. Terminal theme controls the actual shades."""

    # Attributes
    BOLD      = "\033[1m"
    DIM       = "\033[2m"
    ITALIC    = "\033[3m"
    UNDERLINE = "\033[4m"
    REVERSE   = "\033[7m"
    R         = "\033[0m"    # reset

    # Foreground (normal)
    BLACK   = "\033[30m"
    RED     = "\033[31m"
    GREEN   = "\033[32m"
    YELLOW  = "\033[33m"
    BLUE    = "\033[34m"
    MAGENTA = "\033[35m"
    CYAN    = "\033[36m"
    WHITE   = "\033[37m"

    # Foreground (bright)
    BRIGHT_BLACK   = "\033[90m"  # often used as "gray"
    BRIGHT_RED     = "\033[91m"
    BRIGHT_GREEN   = "\033[92m"
    BRIGHT_YELLOW  = "\033[93m"
    BRIGHT_BLUE    = "\033[94m"
    BRIGHT_MAGENTA = "\033[95m"
    BRIGHT_CYAN    = "\033[96m"
    BRIGHT_WHITE   = "\033[97m"

    # Compound helpers
    DIM_RED     = "\033[2;31m"
    DIM_MAGENTA = "\033[2;35m"
    DIM_WHITE   = "\033[2;37m"

    @staticmethod
    def err(text):
        """Wrap text in red."""
        return f"\033[31m{text}\033[0m"

    @staticmethod
    def dim(text):
        """Wrap text in dim."""
        return f"\033[2m{text}\033[0m"

    @staticmethod
    def bold(text):
        """Wrap text in bold."""
        return f"\033[1m{text}\033[0m"

    @staticmethod
    def accent(text):
        """Wrap text in the accent color (blue)."""
        return f"\033[34m{text}\033[0m"


# ── prompt_toolkit style dict ────────────────────────────────

class style:
    """prompt_toolkit style constants using ANSI palette names."""

    PROMPT_TOOLKIT = {
        # Prompt
        "prompt":           "ansiblue bold",
        "prompt.cont":      "ansigray bold",
        # Toolbar — reverse video adapts to any background
        "bottom-toolbar":   "reverse",
        "bottom-toolbar.text": "",
        "bottom-toolbar.key":  "ansiblue bold",
        # Completion menu
        "completion-menu.completion":             "bg:ansiblack ansiwhite",
        "completion-menu.completion.current":     "bg:ansiblue ansiwhite bold",
        "completion-menu.meta.completion":         "bg:ansiblack ansigray",
        "completion-menu.meta.completion.current": "bg:ansiblue ansigray",
    }


# ── Activity formatting ─────────────────────────────────────

def format_activity(entries):
    """Format activity entries from the watcher as a printable string.

    Uses dim + ANSI colors for a subtle left-border style that
    doesn't distract from the user's own work.

    Args:
        entries: list of dicts with keys: code, output, ok, n
    Returns:
        String with ANSI escapes, ready to print.
    """
    a = ansi
    lines = []
    for e in entries:
        code = e.get("code", "")
        output = e.get("output", "")
        ok = e.get("ok", True)
        mark = "✓" if ok else "✗"
        mark_color = a.DIM_WHITE if ok else a.DIM_RED

        # Filter blank code lines.
        code_lines = [cl for cl in code.split("\n") if cl.strip()][:6]
        out_lines = [ol for ol in output.split("\n") if ol.strip()][:6]

        # Header: thin bar + label.
        lines.append(f"{a.DIM_MAGENTA}\u258d{mark_color} rat {mark}{a.R}")
        for cl in code_lines:
            lines.append(f"{a.DIM_MAGENTA}\u258d {a.DIM}{cl}{a.R}")
        if out_lines:
            for ol in out_lines:
                lines.append(f"{a.DIM_MAGENTA}\u258d {a.DIM}{ol}{a.R}")
        lines.append("")  # blank line between entries
    return "\n".join(lines)
