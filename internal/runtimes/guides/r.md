# Set up R for notebooks

R cells run in rat's R kernel, which needs R itself — the `Rscript`
program — and nothing else. rat installs the R packages a notebook
declares (`rat.r.dependencies` in its front matter): do not install them
by hand. This is done once per computer; install R only.

## macOS

Install rig, the R installation manager, then the current R:

    brew install --cask rig        # with Homebrew; or the .pkg from https://github.com/r-lib/rig/releases/latest
    rig add release

rig asks for the password once, to put R in /Library. On macOS, R
packages come ready-made: installs are quick.

## Windows

    winget install --id Posit.rig
    rig add release

(Or the installer from https://cloud.r-project.org/bin/windows/base/.)
On Windows, R packages come ready-made: installs are quick.

## Linux

Install R from the distribution (this needs the password):

    sudo apt install r-base-core build-essential      # Debian, Ubuntu
    sudo dnf install R-core R-core-devel gcc make     # Fedora

On Linux, rat builds R packages from source: the compilers above make
that work. (rig, https://github.com/r-lib/rig#linux, is another way.)

## NixOS

Add R to the configuration and rebuild; packages from Nix count as
installed, and rat installs any others into the project:

    # configuration.nix (or home.packages in Home Manager)
    environment.systemPackages = [
      (pkgs.rWrapper.override { packages = with pkgs.rPackages; [ jsonlite ]; })
      pkgs.gcc pkgs.gnumake   # for the packages rat builds
    ];

    sudo nixos-rebuild switch

## Check

    Rscript --version
    rat doctor {notebook}
    rat ensure {notebook}

`rat ensure` installs the notebook's R packages (and jsonlite, which the
R kernel uses) into the project, and records their versions in
`.rat/r.lock`. In Chattering, "make it run" does the same.

## For an AI agent doing this

- Install R only: no other language, and no R packages — `rat ensure`
  installs those from the notebook's front matter.
- Use the method above for this system. Before any step that needs the
  person's password or changes the system's configuration, say what it
  changes and ask.
- Verify: `Rscript --version`, then `rat doctor {notebook}` must no
  longer report R missing.
- Report what you installed, where, and anything that failed.
