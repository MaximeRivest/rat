# Set up Julia for notebooks

Julia cells run in rat's Julia kernel, which needs Julia itself — the
`julia` program — and nothing else. rat installs the Julia packages a
notebook declares (`rat.julia.dependencies` in its front matter): do not
add them by hand. This is done once per computer; install Julia only.

## macOS

Install juliaup, Julia's official installer (no password; it goes in
your home folder and keeps Julia up to date):

    curl -fsSL https://install.julialang.org | sh -s -- --yes

## Linux

Install juliaup, Julia's official installer (no password; it goes in
your home folder and keeps Julia up to date):

    curl -fsSL https://install.julialang.org | sh -s -- --yes

## Windows

    winget install --name Julia --id 9NJNWW8PVKMN -e -s msstore

## NixOS

Add Julia to the configuration and rebuild. Julia packages ship
ready-made binaries, which run on NixOS through nix-ld:

    # configuration.nix (or home.packages in Home Manager for julia-bin)
    environment.systemPackages = [ pkgs.julia-bin ];
    programs.nix-ld.enable = true;

    sudo nixos-rebuild switch

## Check

    julia --version
    rat doctor {notebook}
    rat ensure {notebook}

rat finds a Julia that juliaup installed even before a new terminal
puts it on the PATH. `rat ensure` installs the notebook's Julia packages
into the project (`.rat/julia`) and records their versions in its
`Manifest.toml`. In Chattering, "make it run" does the same.

## For an AI agent doing this

- Install Julia only: no other language, and no Julia packages —
  `rat ensure` installs those from the notebook's front matter.
- Use the method above for this system. Before any step that needs the
  person's password or changes the system's configuration, say what it
  changes and ask.
- Verify: `julia --version`, then `rat doctor {notebook}` must no longer
  report Julia missing.
- Report what you installed, where, and anything that failed.
