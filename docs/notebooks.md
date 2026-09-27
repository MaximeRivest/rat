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

### R packages

```markdown
---
rat:
  r:
    dependencies:           # pak package references
      - dplyr               # CRAN; an installed copy anywhere will do
      - ggplot2@3.5.1       # exactly this version (@>=3.5 for at least)
      - tidyverse/dplyr@main   # GitHub (owner/repo[@ref])
      - bioc::DESeq2        # Bioconductor
      - local::.            # this project, as an R package
      - thing=url::https://example.org/thing_1.0.tar.gz   # name what a URL installs
---
```

Each line is a [pak package reference](https://pak.r-lib.org/reference/pak_package_sources.html).
Packages go into the project's own library,
`<project>/.rat/r-library/<platform>/R-<x.y>` (one per R minor version:
packages built for R 4.5 do not load in 4.6), which the R kernel puts
first on `.libPaths()` — including when it appears while the kernel
runs, so a package just installed loads without a restart. Packages
installed elsewhere (the site library, Nix, your user library) stay
visible, and a plain name found there counts as installed. A project
that uses renv keeps its renv library, and rat installs with
`renv::install()`.

rat installs with pak, which it keeps in its own cache
(`~/.cache/rat/r-tools`), not in the project. On Linux without
ready-made binaries (NixOS), pak builds from source and needs a C
compiler and `make`; there, prefer R packages from Nix and let `ensure`
see them as installed. `.rat/` carries its own `.gitignore`.

`jsonlite` is added to every notebook with R cells: rat's R kernel needs
it.

### Julia packages

```markdown
---
rat:
  julia:
    dependencies:
      - DataFrames          # registered; installed anywhere will do
      - Plots@1.40          # any 1.40.x (@1.40.2: exactly)
      - https://github.com/org/Foo.jl#main   # Git, at a branch/tag/commit
      - Baz=https://example.org/repo          # name what a URL installs
      - ./MyPkg             # a local package, developed in place (like -e)
---
```

They go into `<project>/.rat/julia`, a Julia environment of rat's: a
project that is itself a Julia package keeps its `Project.toml` as its
authors wrote it. The Julia kernel activates the project's own
environment when it has one (so `using TheProject` works) and stacks
`.rat/julia` on `LOAD_PATH` right after it — also when it appears while
the kernel runs. The default environment (`~/.julia/environments/v1.x`)
stays loadable, and a declared package found in any of these counts as
installed (rat reads their `Manifest.toml`s; Julia is not started).
`ensure` runs `Pkg.add`/`Pkg.develop` and precompiles. The Julia kernel
itself needs no package.

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

For R, `ensure` installs what is missing (a GitHub, path or URL line
once, then remembered in `.rat-receipt.json` inside the library; a
`local::` package again whenever its DESCRIPTION version changes), and
restarts the R kernel only when a package changes version.

What `ensure` will **not** do without being told:

- delete an environment whose interpreter does not satisfy `requires` —
  `rat ensure --recreate` does, and says what is lost;
- reinstall everything — `rat ensure --force`;
- install runtimes (tmux for shell cells, R itself, julia, node). `doctor`
  reports them as missing; the rest of the plan still proceeds.

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
`rat.python.dependencies`, `ensure`, rerun. In R, "there is no package
called 'x'" means the same for `rat.r.dependencies`; in Julia, "Package X
not found in current path" for `rat.julia.dependencies`.
