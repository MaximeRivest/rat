"""
R REPL frontend using radian's stack (rchitect + prompt_toolkit).

Usage:
    python r_radian_frontend.py [--server http://127.0.0.1:8718/mcp]

Uses rchitect for R execution and prompt_toolkit for the REPL UI —
same foundation as radian. Syntax highlighting, multiline editing,
bracket matching, history. Only eval is intercepted.

Requires: pip install rchitect prompt_toolkit pygments
"""

import argparse
import os
import sys
import textwrap

# ── R initialization via rchitect ────────────────────────────────

def init_r():
    """Initialize R via rchitect."""
    import rchitect
    rchitect.init()
    return rchitect


def r_eval(rchitect_mod, code):
    """Execute R code and capture output."""
    from rchitect import rcall, rcopy, rparse

    code = code.strip()
    if not code:
        return None

    try:
        # Capture output
        rcall("sink", rcall("textConnection", "rat_output", "w", local=True))

        # Parse and eval
        parsed = rparse(code)
        result = rcall("eval", parsed)

        # Get captured output
        rcall("sink")
        try:
            output = rcopy(rcall("get", "rat_output"))
        except Exception:
            output = None

        # Clean up
        try:
            rcall("rm", "rat_output")
        except Exception:
            pass

        # Print output if any
        if output:
            if isinstance(output, list):
                for line in output:
                    print(line)
            elif isinstance(output, str):
                print(output)

        # Also try to print the result (for expressions like x + 1)
        if result is not None:
            try:
                result_str = rcopy(rcall("capture.output", rcall("print", result)))
                if result_str:
                    if isinstance(result_str, list):
                        for line in result_str:
                            print(line)
                    elif isinstance(result_str, str) and result_str.strip():
                        print(result_str)
            except Exception:
                pass

        return result
    except Exception as e:
        # Make sure sink is reset
        try:
            rcall("sink")
        except Exception:
            pass
        print(f"Error: {e}", file=sys.stderr)
        return None


def r_complete(rchitect_mod, text, cursor):
    """Get R completions."""
    from rchitect import rcall, rcopy

    try:
        # Use R's built-in completion
        rcall(("utils", "rc.settings"), ipck=True)
        completions = rcall(("utils", ".completeToken"))
        result = rcopy(rcall(("utils", "rc.status")))

        if result and "comps" in result:
            comps = result["comps"]
            if isinstance(comps, str):
                return [c for c in comps.split("\t") if c]
            elif isinstance(comps, list):
                return comps
    except Exception:
        pass
    return []


# ── REPL with prompt_toolkit ────────────────────────────────────

def is_complete_r(code):
    """Check if R code is a complete expression."""
    import rchitect
    try:
        from rchitect.interface import parse_text_complete
        return parse_text_complete(code)
    except Exception:
        # Fallback: check for balanced brackets
        parens = code.count("(") - code.count(")")
        braces = code.count("{") - code.count("}")
        brackets = code.count("[") - code.count("]")
        if parens > 0 or braces > 0 or brackets > 0:
            return False
        if code.rstrip().endswith((",", "+", "-", "*", "/", "|", "&", "=")):
            return False
        return True


def start_repl(server_url=""):
    """Start the R REPL with prompt_toolkit."""
    from prompt_toolkit import PromptSession
    from prompt_toolkit.history import InMemoryHistory
    from prompt_toolkit.lexers import PygmentsLexer
    from prompt_toolkit.auto_suggest import AutoSuggestFromHistory
    from prompt_toolkit.completion import Completer, Completion

    try:
        from pygments.lexers.r import SLexer
    except ImportError:
        SLexer = None

    # Initialize R
    print("Initializing R...")
    rchitect_mod = init_r()
    from rchitect import rcopy, rcall
    r_version = rcopy(rcall("get", "R.version.string"))
    print()
    print(f"rat r | {r_version}")

    if server_url:
        print(f"MCP mode — executing on {server_url}")
    else:
        print("Local proof mode — interception active")

    print("Shared namespace — other MCP clients would see your variables.")
    print()

    # Custom completer that queries R
    class RatRCompleter(Completer):
        def get_completions(self, document, complete_event):
            text = document.text_before_cursor
            if not text.strip():
                return

            # Find the token being completed
            word = ""
            for i in range(len(text) - 1, -1, -1):
                if text[i] in " \t\n(,={[":
                    break
                word = text[i] + word

            if not word:
                return

            completions = r_complete(rchitect_mod, text, len(text))
            for comp in completions:
                if comp.startswith(word):
                    yield Completion(comp, start_position=-len(word))

    # Prompt session with R syntax highlighting
    lexer = PygmentsLexer(SLexer) if SLexer else None
    session = PromptSession(
        history=InMemoryHistory(),
        lexer=lexer,
        auto_suggest=AutoSuggestFromHistory(),
        completer=RatRCompleter(),
        complete_while_typing=False,
        multiline=False,  # we handle multiline ourselves
    )

    buffer = ""

    while True:
        try:
            prompt = "r$> " if not buffer else "  + "
            line = session.prompt(prompt)
        except EOFError:
            print("\nGoodbye.")
            break
        except KeyboardInterrupt:
            buffer = ""
            print()
            continue

        # Accumulate for multiline
        buffer = (buffer + "\n" + line) if buffer else line

        # Check if expression is complete
        if not is_complete_r(buffer):
            continue

        code = buffer.strip()
        buffer = ""

        if not code:
            continue

        if code in ("q()", "quit()"):
            print("Goodbye.")
            break

        # ── INTERCEPTION POINT ──
        # In production: mcp_call("run", {"code": code})
        r_eval(rchitect_mod, code)


# ── Entry point ──────────────────────────────────────────────────

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="R REPL frontend (radian-style)")
    parser.add_argument("--server", default="", help="MCP server URL")
    args = parser.parse_args()

    start_repl(server_url=args.server)
