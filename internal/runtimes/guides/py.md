# Set up Python for notebooks

Python cells run in rat's Python kernel, inside an environment of the
project (`.venv`) that `rat ensure` creates. All it needs on this
computer is uv, a small program that makes environments and installs
packages — and that downloads a matching Python itself when the computer
has none. You do not need to install Python. Do not install the
notebook's packages by hand: they come from its front matter
(`rat.python.dependencies`).

## macOS

    curl -LsSf https://astral.sh/uv/install.sh | sh

No password: uv goes in your home folder (`~/.local/bin`).

## Linux

    curl -LsSf https://astral.sh/uv/install.sh | sh

No password: uv goes in your home folder (`~/.local/bin`).

## Windows

    powershell -ExecutionPolicy ByPass -c "irm https://astral.sh/uv/install.ps1 | iex"

## NixOS

Add uv to the configuration and rebuild (Python itself comes from uv or
from Nix):

    environment.systemPackages = [ pkgs.uv ];   # or home.packages
    sudo nixos-rebuild switch

## Check

    uv --version
    rat doctor {notebook}
    rat ensure {notebook}

rat finds uv in `~/.local/bin` even before a new terminal puts it on the
PATH. `rat ensure` creates `.venv`, installs the notebook's packages and
records their versions in `.rat/python.lock`. In Chattering, "make it
run" does the same.

## For an AI agent doing this

- Install uv only: no Python, no packages — `rat ensure` does the rest
  from the notebook's front matter.
- Use the method above for this system. Before any step that needs the
  person's password or changes the system's configuration, say what it
  changes and ask.
- Verify: `uv --version`, then `rat doctor {notebook}` must no longer
  report a missing environment tool.
- Report what you installed, where, and anything that failed.
