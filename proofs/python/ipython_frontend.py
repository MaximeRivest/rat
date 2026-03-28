"""
IPython frontend that routes execution to a shared MCP kernel.

Usage:
    python ipython_frontend.py --server http://127.0.0.1:8717/mcp

Everything is real IPython — syntax highlighting, multiline editing,
history, %magics, !shell commands. Only run_cell and completions are
redirected to the MCP server.
"""

import argparse
import http.client
import json
import sys
import urllib.error
import urllib.parse
import urllib.request

# ── MCP client (minimal, no dependencies beyond stdlib) ─────────

_MSG_ID = 0
_SESSION_ID = None


def mcp_call(server_url, method, params=None):
    """Send a JSON-RPC request to the MCP server (streamable HTTP)."""
    global _MSG_ID, _SESSION_ID
    _MSG_ID += 1
    msg_id = _MSG_ID
    payload = {
        "jsonrpc": "2.0",
        "id": msg_id,
        "method": method,
        "params": params or {},
    }

    parsed = urllib.parse.urlparse(server_url)
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
    }
    if _SESSION_ID:
        headers["Mcp-Session-Id"] = _SESSION_ID

    try:
        conn = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=60)
        conn.request("POST", parsed.path, json.dumps(payload).encode(), headers)
        resp = conn.getresponse()

        # Capture session ID
        sid = resp.getheader("Mcp-Session-Id")
        if sid:
            _SESSION_ID = sid

        content_type = resp.getheader("Content-Type", "")

        # Read response — handle both SSE and plain JSON
        raw = resp.read().decode()
        conn.close()

        if "text/event-stream" in content_type:
            # Parse SSE events
            for line in raw.split("\n"):
                if line.startswith("data: "):
                    data = line[6:]
                    try:
                        body = json.loads(data)
                        if body.get("id") == msg_id:
                            if "error" in body:
                                return {"error": body["error"]}
                            return body.get("result", {})
                    except json.JSONDecodeError:
                        pass
            return {"error": "No matching response in SSE stream"}

        # Plain JSON
        body = json.loads(raw)
        if "error" in body:
            return {"error": body["error"]}
        return body.get("result", {})
    except Exception as e:
        return {"error": str(e)}


def mcp_notify(server_url, method, params=None):
    """Send a JSON-RPC notification (no id, no response expected)."""
    global _SESSION_ID
    payload = {
        "jsonrpc": "2.0",
        "method": method,
        "params": params or {},
    }
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
    }
    if _SESSION_ID:
        headers["Mcp-Session-Id"] = _SESSION_ID

    req = urllib.request.Request(
        server_url,
        data=json.dumps(payload).encode(),
        headers=headers,
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            sid = resp.headers.get("Mcp-Session-Id")
            if sid:
                _SESSION_ID = sid
    except Exception:
        pass


def mcp_initialize(server_url):
    """Initialize the MCP session."""
    result = mcp_call(server_url, "initialize", {
        "protocolVersion": "2025-03-26",
        "capabilities": {},
        "clientInfo": {"name": "rat-py-repl", "version": "0.1.0"},
    })
    # Must send initialized notification after successful init
    if "error" not in result:
        mcp_notify(server_url, "notifications/initialized")
    return result


def mcp_tool(server_url, tool_name, arguments):
    """Call an MCP tool."""
    return mcp_call(server_url, "tools/call", {
        "name": tool_name,
        "arguments": arguments,
    })


# ── IPython shell with MCP execution backend ────────────────────

def make_rat_shell(server_url):
    """Create an IPython shell class that executes via MCP."""

    from IPython.terminal.interactiveshell import TerminalInteractiveShell
    from IPython.core.interactiveshell import ExecutionResult

    class RatShell(TerminalInteractiveShell):
        """Real IPython. Only execution and completions go through MCP."""

        def __init__(self, *args, **kwargs):
            super().__init__(*args, **kwargs)
            self._mcp_url = server_url
            self._mcp_initialized = False

        def _ensure_mcp(self):
            if not self._mcp_initialized:
                result = mcp_initialize(self._mcp_url)
                if "error" in result:
                    print(f"[rat] Warning: MCP init failed: {result['error']}")
                else:
                    self._mcp_initialized = True

        def run_cell(self, raw_cell, store_history=True, silent=False,
                     shell_futures=True, cell_id=None):
            """Execute code on the shared MCP kernel instead of locally."""
            raw_cell = raw_cell.strip()
            if not raw_cell:
                return ExecutionResult(None)

            # Handle exit locally
            if raw_cell in ("exit", "exit()", "quit", "quit()"):
                self.ask_exit()
                return ExecutionResult(None)

            # Let IPython handle magics and shell commands locally
            if raw_cell.startswith("%") or raw_cell.startswith("!"):
                return super().run_cell(
                    raw_cell, store_history=store_history,
                    silent=silent, shell_futures=shell_futures,
                    cell_id=cell_id,
                )

            # Handle ? and ?? (inspection) via MCP look
            if raw_cell.endswith("??") or raw_cell.endswith("?"):
                symbol = raw_cell.rstrip("?").strip()
                if symbol:
                    result = mcp_tool(self._mcp_url, "look", {"at": symbol})
                    self._display_mcp_result(result)
                    return ExecutionResult(None)

            self._ensure_mcp()

            # Execute on shared kernel
            result = mcp_tool(self._mcp_url, "run", {"code": raw_cell})
            self._display_mcp_result(result)

            if store_history:
                self.execution_count += 1

            return ExecutionResult(None)

        def _display_mcp_result(self, result):
            """Display the MCP tool result."""
            if "error" in result:
                print(f"[rat] MCP error: {result['error']}")
                return

            # MCP tools/call returns { content: [{ type: "text", text: "..." }] }
            content = result.get("content", [])
            for item in content:
                if isinstance(item, dict) and item.get("type") == "text":
                    text = item.get("text", "")
                    if text:
                        print(text)

        def complete(self, text, line=None, cursor_pos=None):
            """Get completions from the shared kernel."""
            if line is None:
                line = text
            if cursor_pos is None:
                cursor_pos = len(line)

            self._ensure_mcp()

            result = mcp_tool(self._mcp_url, "look", {
                "code": line,
                "cursor": cursor_pos,
            })

            content = result.get("content", [])
            completions = []
            for item in content:
                if isinstance(item, dict) and item.get("type") == "text":
                    # Parse completion text — each line is a completion
                    for comp_line in item.get("text", "").split("\n"):
                        comp_line = comp_line.strip()
                        if comp_line and not comp_line.startswith("No completions"):
                            # First word is the completion label
                            label = comp_line.split()[0] if comp_line else ""
                            if label:
                                completions.append(label)

            return completions

    return RatShell


# ── Main ─────────────────────────────────────────────────────────

def main():
    parser = argparse.ArgumentParser(description="IPython frontend for rat-py")
    parser.add_argument("--server", default="http://127.0.0.1:8717/mcp",
                        help="MCP server URL")
    args = parser.parse_args()

    server_url = args.server

    # Test connection
    print(f"Connecting to {server_url}...")
    result = mcp_initialize(server_url)
    if "error" in result:
        print(f"Cannot connect to MCP server: {result['error']}")
        print(f"\nStart the server first:")
        print(f"  cd /home/maxime/Projects/mrmd-packages/mrmd-python")
        print(f"  .venv/bin/python -m rat_py --http --port 8717")
        sys.exit(1)

    server_info = result.get("serverInfo", {})
    server_name = server_info.get("name", "unknown")
    print(f"Connected to {server_name}")
    print()

    # Launch IPython with our custom shell
    from IPython.terminal.ipapp import TerminalIPythonApp

    RatShell = make_rat_shell(server_url)

    app = TerminalIPythonApp.instance()
    app.interact = True
    app.interactive_shell_class = RatShell

    # Custom banner
    app.display_banner = False

    app.initialize(["--no-banner"])

    # Print our own banner
    print(f"rat py | {server_name} @ {server_url}")
    print(f"Shared namespace — other MCP clients see your variables.")
    print()

    app.start()


if __name__ == "__main__":
    main()
