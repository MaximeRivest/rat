# REPL Interception Proofs

Prove that we can launch each language's native REPL and redirect execution to
an MCP server — without breaking the REPL experience.

The test: if you can't tell the difference from running the REPL directly
(except that an MCP server holds the namespace), it works.

## How to run each proof

### Python (against real rat-py MCP server)

Terminal 1 — start the MCP server:
```bash
cd /home/maxime/Projects/mrmd-packages/mrmd-python
.venv/bin/python -m rat_py --http --port 8717
```

Terminal 2 — run the IPython frontend:
```bash
cd /home/maxime/Projects/mrmd-packages/rat/proofs/python
pip install ipython requests
python ipython_frontend.py --server http://127.0.0.1:8717/mcp
```

### Julia

```bash
cd /home/maxime/Projects/mrmd-packages/rat/proofs/julia
julia julia_frontend.jl
```

### R

```bash
cd /home/maxime/Projects/mrmd-packages/rat/proofs/r
Rscript r_frontend.R
```

### Bash

```bash
cd /home/maxime/Projects/mrmd-packages/rat/proofs/bash
# Already proven by mrmd-bash PTY — this just demonstrates the concept
bash bash_frontend.sh
```

### Deno

```bash
cd /home/maxime/Projects/mrmd-packages/rat/proofs/deno
deno run --allow-net deno_frontend.ts --server http://127.0.0.1:8720/mcp
```

## What to check

For each REPL, verify:
- [ ] Syntax highlighting works
- [ ] Tab completion works (from shared namespace)
- [ ] Multiline editing works
- [ ] History (↑/↓) works
- [ ] Ctrl+C interrupts execution
- [ ] `x = 42` in the REPL is visible from another MCP client
- [ ] Code executed by another MCP client is visible in the REPL
- [ ] Language-specific features work (IPython magics, Julia modes, etc.)
