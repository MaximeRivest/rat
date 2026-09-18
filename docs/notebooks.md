# Notebooks that just run

A Markdown notebook (`.md` with fenced code cells) should run the same way
from a terminal, VS Code, aiconvo or a coding agent, on any machine. Two
things make that true:

1. **The notebook declares what it needs** — in its front matter.
2. **One engine makes it so** — `rat ensure`.

This is the same idea as RStudio's `.Rproj` + `renv.lock`, generalized: a
project root that is never guessed wrong, one declaration of the
environment, one command that brings the machine to that state.

## Declare the environment

```markdown
---
rat:
  project: ../..            # optional: the project this notebook runs in,
                            # relative to the notebook (or absolute)
  python:
    requires: ">=3.11"      # optional: interpreter version (PEP 440)
    dependencies:           # requirements.txt lines, verbatim
      - -e .                # this project, editable (a local checkout)
      - websockets          # from PyPI
      - lm15 @ git+https://github.com/example/lm15@main   # a branch
---
# My tutorial

```python
import websockets
```
```

`dependencies` are ordinary `requirements.txt` lines, so a local checkout,
a git branch and a published package are each one line. rat does not parse
them; `uv` does.

Python cells may instead (or additionally) carry a
[PEP 723](https://peps.python.org/pep-0723/) block, the standard `uv run`
understands:

```python
# /// script
# requires-python = ">=3.12"
# dependencies = ["rich"]
# ///
import rich
```

Front matter and PEP 723 merge; front matter wins for `requires`.

A notebook with no declaration still runs — on its project's environment.
`ensure` then only makes sure that environment exists.

## Which project?

Without `rat.project`, rat walks up from the notebook's folder and picks
the project by ranked evidence:

1. a folder with its own `.venv` — code runs there, that is the project;
2. a folder with a repository root or package manifest (`.git`,
   `pyproject.toml`, `package.json`, `Cargo.toml`, …);
3. otherwise the nearest folder with a weak hint (`requirements.txt`,
   `Pipfile`, `Makefile`, …) — used only when nothing stronger exists
   above it.

So `repo/docs/tutorials/x.md` belongs to `repo`, even though `repo/docs/`
carries a `requirements.txt` for the documentation build. Pin
`rat.project` when the layout is unusual; the pin wins.

The environment is the project's (`<project>/.venv`, or the nearest venv
between the notebook and the project root). An activated shell venv
(`VIRTUAL_ENV`) is deliberately ignored for notebooks: otherwise a
terminal and a background service would run the same notebook in two
different environments.

## Check, then make it so

```bash
rat doctor docs/tutorial.md        # what is missing, what ensure would do
rat ensure docs/tutorial.md        # do it
rat ensure docs/tutorial.md --json # the same report, for integrations
```

`doctor` changes nothing and exits 1 when something is missing. `ensure`:

- creates `<project>/.venv` if there is none (`uv venv`, honouring
  `requires`; uv fetches a matching interpreter when needed);
- installs what is not yet installed — and only that. A plain name that is
  already installed is left alone; a pinned/URL/editable line is
  reinstalled once, then remembered in a receipt inside the venv
  (`.venv/rat-notebook.json`) so the next `ensure` is a no-op;
- restarts the kernel only when it must: its binding changed (a venv was
  created under a kernel running on another interpreter) or an editable
  package was installed (`.pth` hooks are read at interpreter start).
  Both say so before they happen ("kernel restarts — variables reset").

`rat` adds `ipython` and `jedi` to every notebook environment: the human
REPL (`rat py`) is IPython and the kernel completes with jedi. This is the
same contract `rat install py` establishes.

What `ensure` will **not** do without being told:

- delete an environment whose interpreter does not satisfy `requires` —
  `rat ensure --recreate` does, and says what is lost;
- reinstall everything — `rat ensure --force`;
- install tools for other languages (tmux for shell cells, Rscript, julia,
  node). `doctor` reports them as missing; the Python plan still proceeds.

## Notebooks that build on other notebooks

A notebook may declare that other notebooks must have run first, in the
same kernel:

```yaml
---
rat:
  after:
    - ./01-load-data.md
---
```

This is a **declared** dependency. A notebook must never rely on whatever
the kernel happens to contain — that is what makes notebooks lie. With
`after`, the dependency is visible in the file, `rat doctor` shows whether
each prerequisite has already run, and `rat play` runs the chain first:

```bash
rat play docs/analysis.md                 # ensure → prerequisites (once per kernel) → this notebook's cells
rat play docs/analysis.md --prerequisites # only the chain (a client that runs cells itself uses this)
rat play docs/analysis.md --json
```

Rules the chain keeps:

- prerequisites run once per kernel lifetime (the kernel remembers what it
  played; `rat restart` forgets), depth first, each once;
- every notebook in a chain must belong to the same project — state does
  not flow between kernels — and cycles are refused;
- a chain shares requirements: `ensure` installs the union.

`rat play` prints outputs; it never writes them into the files.

## Run against the notebook

Every rat command takes `--doc <notebook>`: language names then resolve
for that notebook — its folder, or the project it pins — instead of the
current directory. That is how a client reaches the same kernel a person
gets in the terminal:

```bash
rat run --doc docs/tutorial.md py 'import websockets'
rat cancel --doc docs/tutorial.md py
rat resolve --doc docs/tutorial.md py
```

## For coding agents

When asked to write a notebook someone will run:

1. Look at the context: is this a checkout of the package (`-e .`), a
   branch of something (`name @ git+…`), or published (`name==x`)?
2. Write the front matter. Never `pip install` inside a cell — an install
   that only happened here is invisible to the next machine.
3. `rat doctor <notebook>` to read the plan; `rat ensure <notebook>` to
   carry it out. Both are idempotent: run them as often as you like.
4. Run the cells: `rat run --doc <notebook> py '<code>'`.

A failed `import` means the declaration is incomplete: add the line to
`rat.python.dependencies`, `ensure`, rerun.
