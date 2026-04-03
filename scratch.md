 ```
   rat py
     │
     ├─ Python not found?
     │    → "Python not found. Try: rat install py"
     │    → exit
     │
     ├─ Find project root (walk up from cwd for .git, pyproject.toml, etc.)
     │    │
     │    ├─ Project found, .venv exists inside project?
     │    │    → Use it. Install IPython/jedi if missing. Start py@project.
     │    │
     │    ├─ Project found, no .venv?
     │    │    → Use system Python. Start py@project.
     │    │    → Hint: "No venv found. To create: uv venv" # comments: (what should we do if uv not found? "No venv found. To create: python -m venv .venv"... but also not using uv in 2026 is really not recommended, but also if user is opiniated about it,  we should respect that.)
     │    │
     │    └─ No project found?
     │         │
     │         ├─ .venv exists in cwd?
     │         │    → Use it. Start global py.
     │         │
     │         └─ No .venv in cwd?
     │              → Use system Python. Start global py.
     │
     └─ IPython not importable?
          → Install it (into venv if available, else --user)
          → Proceed
 ```



 Key design rules

 1. Never create a venv unless the user explicitly asked (rat install py or a future --init) # comment: but teach them about it?
 2. Always prefer the project's venv if it exists # what is there is a project venv and a cwd venv, say we are in a subfolder of the project
 3. Never walk past the project root for venvs (don't find ~/.venv)  # what if there is not project until home?
 4. Always work, even without a venv — system Python is fine for a scratch session # yes!
 5. cancel stays separate from reset — you're right, cancelling a stuck loop is not "start over"

 Revised tier 1

 ```bash
   rat py                    # REPL — just works, always
   rat run py 'x=42'         # one-liner — just works, always
   rat look py [--at x]      # inspect
   rat cancel py             # unstick
   rat restart py            # fresh runtime (i prefer that clearing namespace in python repl is not that reliable)
   rat status                # what's running
 ```