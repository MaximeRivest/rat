#!/bin/bash
#
# Bash REPL "frontend" proof — shows the PTY passthrough concept.
#
# For bash, we don't intercept eval. We DO PTY passthrough:
# the kernel owns a bash process in a PTY, and we attach
# the user's terminal to it. This is how tmux/screen work.
#
# This proof demonstrates the interception concept with a simpler
# approach: a custom REPL loop that wraps eval. The real
# implementation (in Go) will use creack/pty for true PTY
# passthrough — which is already proven in mrmd-bash.
#
# Usage:
#     bash bash_frontend.sh

echo "rat sh | bash ${BASH_VERSION}"
echo "Local proof mode — interception active"
echo "Shared namespace — commands execute in this shell."
echo ""

# Track execution count
_RAT_EXEC_COUNT=0

while true; do
    # Read with prompt
    read -e -p "rat\$ " _RAT_LINE

    # EOF (Ctrl+D)
    if [ $? -ne 0 ]; then
        echo ""
        break
    fi

    # Empty line
    [ -z "$_RAT_LINE" ] && continue

    # Exit
    [ "$_RAT_LINE" = "exit" ] && break

    # Add to history (bash readline history works with read -e)
    history -s "$_RAT_LINE"

    # Multiline: check for trailing backslash or unclosed quotes
    while [[ "$_RAT_LINE" == *\\ ]]; do
        read -e -p "> " _RAT_CONT
        _RAT_LINE="${_RAT_LINE%\\}
$_RAT_CONT"
    done

    # ── INTERCEPTION POINT ──
    # In the real implementation, this is where we'd send to MCP.
    # For the proof, we execute locally but prove we can intercept.
    _RAT_EXEC_COUNT=$((_RAT_EXEC_COUNT + 1))

    # Execute and capture exit code
    eval "$_RAT_LINE"
    _RAT_EXIT=$?

    if [ $_RAT_EXIT -ne 0 ]; then
        echo "[exit: $_RAT_EXIT]"
    fi
done

echo "Goodbye. $_RAT_EXEC_COUNT commands executed."
