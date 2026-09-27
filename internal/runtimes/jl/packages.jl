# rat's package manager for Julia notebooks: rat.julia.dependencies.
#
#   julia packages.jl check|install|lock <project> [--force] [--update] -- <line>...
#
# The contract with rat (internal/notebook/runtimeenv.go): check prints
# one fact per line, tab-separated; install and lock print their log.
#
# Lines: a registered package (DataFrames, Plots@1.40 — any 1.40.x,
# Plots@1.40.2 — exactly), a Git URL (https://github.com/org/Foo.jl#main,
# Foo=<url> to name it), or a local package (./MyPkg), developed in place.
#
# They go into <project>/.rat/julia, a Julia environment of rat's: a
# project that is itself a Julia package keeps its Project.toml. The
# kernel activates the project's own environment when it has one and
# stacks .rat/julia on LOAD_PATH; the default environment
# (~/.julia/environments/v1.x) stays loadable, and a declared package
# found in any of them will do.
#
# The lock is .rat/julia/Manifest.toml, committed with the project: with
# it, install reproduces its exact versions (Pkg.instantiate); --update
# resolves again.

using TOML

const VERB = ARGS[1]
const PROJECT = abspath(ARGS[2])
const SEP = something(findfirst(==("--"), ARGS), length(ARGS) + 1)
const FLAGS = ARGS[3:SEP-1]
const DECLARED = unique(strip.(ARGS[SEP+1:end]))
const FORCE = "--force" in FLAGS
const UPDATE = "--update" in FLAGS

const ENV_DIR = joinpath(pwd(), ".rat", "julia")
const ENV_PROJECT = joinpath(ENV_DIR, "Project.toml")
const LOCK = joinpath(ENV_DIR, "Manifest.toml")

emit(fields...) = println(join(string.(fields), '\t'))

struct Ref_
    ref::String
    package::String
    version::String
    url::String
    path::String
    error::String
end

function parse_ref(ref::AbstractString)
    ref = String(ref)
    bad(msg) = Ref_(ref, "", "", "", "", msg)
    occursin(r"[\s;|&$`\"']", ref) && return bad("a package reference has no spaces or shell characters")
    name, body = "", ref
    m = match(r"^([A-Za-z_][A-Za-z0-9_]*)=(.+)$", ref)
    m === nothing || ((name, body) = (m[1], m[2]))
    if body == "." || startswith(body, "./") || startswith(body, "../") || startswith(body, "/")
        dir = isabspath(body) ? body : normpath(joinpath(PROJECT, body))
        pf = joinpath(dir, "Project.toml")
        isfile(pf) || return bad("no Julia package (Project.toml) in $dir")
        pname = get(TOML.parsefile(pf), "name", "")
        isempty(pname) && return bad("$pf has no name")
        return Ref_(ref, isempty(name) ? pname : name, "", "", dir, "")
    elseif occursin("://", body) || startswith(body, "git@")
        if isempty(name)
            repo = replace(replace(replace(split(body, '#')[1], r"/+$" => ""), r"\.git$" => ""), r"\.jl$" => "")
            name = String(split(repo, r"[/:]")[end])
            occursin(r"^[A-Za-z_][A-Za-z0-9_]*$", name) || return bad("name the package it installs: <Package>=$ref")
        end
        return Ref_(ref, name, "", body, "", "")
    end
    parts = split(body, '@', limit = 2)
    occursin(r"^[A-Za-z_][A-Za-z0-9_]*$", parts[1]) || return bad("not a Julia package name")
    version = length(parts) > 1 ? String(parts[2]) : ""
    isempty(version) || occursin(r"^[0-9]+(\.[0-9]+){0,2}$", version) || return bad("version must be like @1.6 or @1.6.1")
    Ref_(ref, isempty(name) ? String(parts[1]) : name, version, "", "", "")
end

version_ok(have, want) = isempty(want) || have == want || startswith(have, want * ".")

# name → (uuid, version, source) of a manifest next to a project file.
function manifest(project_file)
    out = Dict{String,Tuple{String,String,String}}()
    isempty(project_file) && return out
    dir = dirname(project_file)
    for f in ("JuliaManifest.toml", "Manifest.toml")
        path = joinpath(dir, f)
        isfile(path) || continue
        m = TOML.parsefile(path)
        deps = get(m, "deps", m)   # format 2, or the older flat one
        for (name, entries) in deps
            entries isa Vector || continue
            e = entries[1]
            src = haskey(e, "path") ? "path:" * e["path"] : haskey(e, "repo-url") ? "url:" * e["repo-url"] : ""
            out[name] = (get(e, "uuid", ""), get(e, "version", ""), src)
        end
        break
    end
    out
end

own_project() = (p = joinpath(pwd(), "JuliaProject.toml"); isfile(p) ? p : (p = joinpath(pwd(), "Project.toml"); isfile(p) ? p : ""))
default_env() = joinpath(first(DEPOT_PATH), "environments", "v$(VERSION.major).$(VERSION.minor)", "Project.toml")

# Is a locked package's source on this machine (a fresh clone has the lock
# but not the packages)?
function present(name, uuid)
    isempty(uuid) && return false
    old = Base.ACTIVE_PROJECT[]
    try
        Base.ACTIVE_PROJECT[] = ENV_PROJECT
        Base.locate_package(Base.PkgId(Base.UUID(uuid), name)) !== nothing
    catch
        false
    finally
        Base.ACTIVE_PROJECT[] = old
    end
end

function inspect()
    refs = parse_ref.(DECLARED)
    problems = filter(r -> !isempty(r.error), refs)
    refs = filter(r -> isempty(r.error), refs)
    locked = UPDATE ? Dict{String,Tuple{String,String,String}}() : manifest(isfile(ENV_PROJECT) ? ENV_PROJECT : "")
    visible = merge(manifest(isfile(default_env()) ? default_env() : ""), manifest(own_project()))
    missing = String[]
    upgrades = false
    instantiate = false
    for r in refs
        if haskey(locked, r.package)
            uuid, have, _ = locked[r.package]
            if !present(r.package, uuid)
                instantiate = true
                push!(missing, r.ref)
            elseif FORCE || !version_ok(have, r.version)
                push!(missing, r.ref)
                upgrades = true
            end
            continue
        end
        found = haskey(visible, r.package)
        ok = found && version_ok(visible[r.package][2], r.version) && isempty(r.url) && isempty(r.path)
        if FORCE || !ok
            push!(missing, r.ref)
            upgrades |= found
        end
    end
    (; refs, problems, missing, upgrades, instantiate, locked)
end

const STATE = inspect()

if VERB == "check"
    emit("version", VERSION)
    emit("environment", ENV_DIR)
    emit("lock", LOCK)
    emit("info", "environment", ENV_DIR)
    isempty(own_project()) || emit("info", "project_environment", own_project())
    for r in DECLARED
        emit("requirement", r)
    end
    for p in STATE.problems
        emit("problem", p.ref, p.error)
    end
    for m in STATE.missing
        emit("missing", m)
    end
    STATE.upgrades && emit("restart", "true")
    if isempty(STATE.missing)
        locked = isfile(LOCK) ? " · locked (Manifest.toml)" : ""
        emit("detail", "$(length(DECLARED)) satisfied · Julia $(VERSION) · $(ENV_DIR)$locked")
    else
        emit("detail", "$(length(STATE.missing)) to install into $(ENV_DIR)$(STATE.instantiate ? " (from the lock)" : ""): $(join(STATE.missing, ", "))")
        emit("summary", "julia --project=$(ENV_DIR) -e 'using Pkg; " *
             (STATE.instantiate ? "Pkg.instantiate(); " : "") * "Pkg.add([" * join(repr.(STATE.missing), ", ") * "])'")
    end
    exit(0)
end

if !isempty(STATE.problems)
    for p in STATE.problems
        println(stderr, p.ref, ": ", p.error)
    end
    exit(2)
end

function install(Pkg)
    mkpath(ENV_DIR)
    Pkg.activate(ENV_DIR)
    if isfile(LOCK) && !UPDATE
        Pkg.instantiate()
    end
    todo = filter(r -> r.ref in STATE.missing, STATE.refs)
    devs = [Pkg.PackageSpec(path = r.path) for r in todo if !isempty(r.path)]
    adds = Pkg.PackageSpec[]
    for r in todo
        isempty(r.path) || continue
        if !isempty(r.url)
            url, rev = occursin('#', r.url) ? split(r.url, '#', limit = 2) : (r.url, "")
            push!(adds, isempty(rev) ? Pkg.PackageSpec(url = String(url)) : Pkg.PackageSpec(url = String(url), rev = String(rev)))
        elseif haskey(STATE.locked, r.package) && !UPDATE && version_ok(STATE.locked[r.package][2], r.version)
            continue   # instantiate brought it back
        else
            push!(adds, isempty(r.version) ? Pkg.PackageSpec(name = r.package) : Pkg.PackageSpec(name = r.package, version = r.version))
        end
    end
    isempty(devs) || Pkg.develop(devs)
    isempty(adds) || Pkg.add(adds)
    UPDATE && Pkg.update([r.package for r in STATE.refs if isempty(r.path)])
    Pkg.precompile()
    println("the lock is ", LOCK)
end

if VERB == "install" && !isempty(STATE.missing)
    # Pkg loads only here: check stays quick.
    Base.invokelatest(install, Base.require(Base.PkgId(Base.UUID("44cfe95a-1eb2-52ea-b672-e2afdf69b78f"), "Pkg")))
end
