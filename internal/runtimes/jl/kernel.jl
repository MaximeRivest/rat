# rat kernel for Julia.
#
# Speaks the rat kernel protocol over the private connection rat opens
# (transport: socket, see KERNEL-PROTOCOL.md). Whatever the user's code
# prints — println, @info/@warn, C code, programs it runs — goes to this
# process's stdout/stderr, which rat streams live.
#
# Cells behave as in Julia's REPL: the cell's value is displayed unless
# the cell ends with `;`, and `ans` holds it. display() of anything that
# can be a PNG (Plots.jl, Makie, images) writes a file announced as a
# __RAT_PLOT__:<path> line; everything else is shown as text/plain.
# readline() asks whoever runs the cell. Cancel (SIGINT) stops running
# code at Julia's next safepoint; a loop that never allocates cannot be
# interrupted in Julia, and rat stops the kernel on a second cancel.
#
# No package is needed: the kernel speaks JSON itself and uses only the
# standard library (Sockets, REPL for completion).

module RatKernel

using Sockets
import REPL
import REPL.REPLCompletions

# ── JSON (the protocol's subset) ─────────────────────────────

function json(io::IO, x)
    if x === nothing
        print(io, "null")
    elseif x isa Bool
        print(io, x ? "true" : "false")
    elseif x isa Integer
        print(io, x)
    elseif x isa AbstractFloat
        isfinite(x) ? print(io, x) : print(io, "null")
    elseif x isa AbstractString || x isa Symbol
        s = isvalid(String(x)) ? String(x) : String(map(c -> isvalid(c) ? c : '\ufffd', String(x)))
        print(io, '"')
        for c in s
            if c == '"'
                print(io, "\\\"")
            elseif c == '\\'
                print(io, "\\\\")
            elseif c == '\n'
                print(io, "\\n")
            elseif c == '\r'
                print(io, "\\r")
            elseif c == '\t'
                print(io, "\\t")
            elseif c < ' '
                print(io, "\\u", string(UInt16(c), base = 16, pad = 4))
            else
                print(io, c)
            end
        end
        print(io, '"')
    elseif x isa AbstractDict
        print(io, '{')
        first = true
        for (k, v) in x
            first || print(io, ',')
            first = false
            json(io, string(k))
            print(io, ':')
            json(io, v)
        end
        print(io, '}')
    elseif x isa AbstractVector || x isa Tuple
        print(io, '[')
        for (i, v) in enumerate(x)
            i > 1 && print(io, ',')
            json(io, v)
        end
        print(io, ']')
    else
        json(io, string(x))
    end
end

mutable struct Reader
    s::String
    i::Int
end

function skipws(r::Reader)
    while r.i <= ncodeunits(r.s) && r.s[r.i] in (' ', '\t', '\n', '\r')
        r.i = nextind(r.s, r.i)
    end
end

function parse_value(r::Reader)
    skipws(r)
    c = r.s[r.i]
    if c == '{'
        d = Dict{String,Any}()
        r.i += 1
        skipws(r)
        if r.s[r.i] == '}'
            r.i += 1
            return d
        end
        while true
            skipws(r)
            k = parse_value(r)::String
            skipws(r)
            r.s[r.i] == ':' || error("bad JSON")
            r.i += 1
            d[k] = parse_value(r)
            skipws(r)
            c = r.s[r.i]
            r.i += 1
            c == '}' && return d
            c == ',' || error("bad JSON")
        end
    elseif c == '['
        v = Any[]
        r.i += 1
        skipws(r)
        if r.s[r.i] == ']'
            r.i += 1
            return v
        end
        while true
            push!(v, parse_value(r))
            skipws(r)
            c = r.s[r.i]
            r.i += 1
            c == ']' && return v
            c == ',' || error("bad JSON")
        end
    elseif c == '"'
        io = IOBuffer()
        r.i += 1
        while true
            c = r.s[r.i]
            r.i = nextind(r.s, r.i)
            c == '"' && break
            if c == '\\'
                e = r.s[r.i]
                r.i += 1
                if e == 'n'
                    write(io, '\n')
                elseif e == 't'
                    write(io, '\t')
                elseif e == 'r'
                    write(io, '\r')
                elseif e == 'b'
                    write(io, '\b')
                elseif e == 'f'
                    write(io, '\f')
                elseif e == 'u'
                    u = parse(UInt32, r.s[r.i:r.i+3], base = 16)
                    r.i += 4
                    if 0xd800 <= u <= 0xdbff && r.s[r.i] == '\\'   # surrogate pair
                        lo = parse(UInt32, r.s[r.i+2:r.i+5], base = 16)
                        r.i += 6
                        u = 0x10000 + ((u - 0xd800) << 10) + (lo - 0xdc00)
                    end
                    write(io, Char(u))
                else
                    write(io, e)
                end
            else
                write(io, c)
            end
        end
        return String(take!(io))
    elseif startswith(SubString(r.s, r.i), "true")
        r.i += 4
        return true
    elseif startswith(SubString(r.s, r.i), "false")
        r.i += 5
        return false
    elseif startswith(SubString(r.s, r.i), "null")
        r.i += 4
        return nothing
    else
        m = match(r"-?\d+(\.\d+)?([eE][-+]?\d+)?", r.s, r.i)
        m === nothing && error("bad JSON")
        r.i += ncodeunits(m.match)
        return m.captures[1] === nothing && m.captures[2] === nothing ? parse(Int, m.match) : parse(Float64, m.match)
    end
end

parse_json(s::AbstractString) = parse_value(Reader(String(s), 1))

# ── protocol connection ─────────────────────────────────────

const SOCK = Ref{TCPSocket}()

function send(obj)
    io = IOBuffer()
    json(io, obj)
    write(io, '\n')
    write(SOCK[], take!(io))
    flush(SOCK[])
end

function connect_to_rat()
    addr = get(ENV, "RAT_PROTOCOL_TCP_ADDR", "")
    isempty(addr) && error("RAT_PROTOCOL_TCP_ADDR is not set: this kernel is started by rat")
    host, port = rsplit(addr, ':', limit = 2)
    SOCK[] = connect(host, parse(Int, port))
    token = get(ENV, "RAT_PROTOCOL_TOKEN", "")
    delete!(ENV, "RAT_PROTOCOL_TCP_ADDR")
    delete!(ENV, "RAT_PROTOCOL_TOKEN")
    send(Dict("op" => "protocol_hello", "token" => token))
end

# ── the project's environment ───────────────────────────────
# A Julia project (Project.toml in the kernel's directory) is the active
# environment, so `using TheProject` works. The packages a notebook
# declares go into <project>/.rat/julia (`rat ensure`), which is stacked
# on LOAD_PATH right after the active one — also when it appears while
# the kernel runs: a package just installed loads without a restart. The
# default environment (~/.julia/environments/v1.x) stays loadable, as in
# Julia. Without a Project.toml, .rat/julia is also the active one.

const PROJECT_DIR = pwd()
const NOTEBOOK_ENV = joinpath(PROJECT_DIR, ".rat", "julia")

function use_project_environment()
    own = ""
    for f in ("JuliaProject.toml", "Project.toml")
        isfile(joinpath(PROJECT_DIR, f)) && (own = joinpath(PROJECT_DIR, f); break)
    end
    notebook = joinpath(NOTEBOOK_ENV, "Project.toml")
    if !isempty(own)
        Base.active_project() == own || Base.set_active_project(own)
    elseif isfile(notebook)
        Base.active_project() == notebook || Base.set_active_project(notebook)
    end
    if isfile(notebook) && !(NOTEBOOK_ENV in LOAD_PATH)
        insert!(LOAD_PATH, min(2, length(LOAD_PATH) + 1), NOTEBOOK_ENV)
    end
end

# ── display: text, and PNG files for plots ──────────────────

const PLOT_DIR = joinpath(get(ENV, "XDG_CACHE_HOME", joinpath(homedir(), ".cache")), "rat", "plots")
const PLOTS = Ref(0)
const RUN = Ref(0)

struct RatDisplay <: AbstractDisplay end

text_context(io) = IOContext(io, :limit => true, :displaysize => (40, 120), :module => Main)

function show_png(x)
    mkpath(PLOT_DIR)
    path = joinpath(PLOT_DIR, "jl-$(getpid())-$(RUN[])-$(PLOTS[] += 1).png")
    open(io -> show(io, MIME"image/png"(), x), path, "w")
    println(stdout, "__RAT_PLOT__:", path)
end

function show_text(x)
    show(text_context(stdout), MIME"text/plain"(), x)
    println(stdout)
end

Base.displayable(::RatDisplay, ::MIME"image/png") = true
Base.displayable(::RatDisplay, ::MIME"text/plain") = true
Base.display(::RatDisplay, ::MIME"image/png", x) = show_png(x)
Base.display(::RatDisplay, ::MIME"text/plain", x) = show_text(x)
function Base.display(d::RatDisplay, x)
    # Strings, numbers and arrays of numbers are text even when some
    # package taught them to be images.
    if !(x isa Union{AbstractString,Number,AbstractArray{<:Number}}) && showable(MIME"image/png"(), x)
        show_png(x)
    else
        show_text(x)
    end
end

# ── input ───────────────────────────────────────────────────
# stdin is a stream that asks through rat: readline() (and anything built
# on reading bytes) waits for the reader's answer. The answer is echoed,
# as a console shows what was typed.

mutable struct RatStdin <: IO
    buf::Vector{UInt8}
end

function ask!(s::RatStdin)
    send(Dict("op" => "input_request", "prompt" => ""))
    local text
    while true
        line = readline(SOCK[]; keep = true)
        isempty(line) && error("rat went away while waiting for input")
        req = parse_json(line)
        if get(req, "op", "") == "input"
            text = string(get(req, "text", ""))
            break
        end
    end
    send(Dict("op" => "input_delivered"))
    text = replace(text, "\r\n" => "\n")
    endswith(text, "\n") || (text *= "\n")
    print(stdout, text)
    append!(s.buf, codeunits(text))
end

Base.isopen(::RatStdin) = true
Base.eof(s::RatStdin) = (isempty(s.buf) && ask!(s); false)
Base.bytesavailable(s::RatStdin) = length(s.buf)
function Base.read(s::RatStdin, ::Type{UInt8})
    isempty(s.buf) && ask!(s)
    popfirst!(s.buf)
end
function Base.readline(s::RatStdin; keep::Bool = false)
    isempty(s.buf) && ask!(s)
    i = findfirst(==(UInt8('\n')), s.buf)
    while i === nothing
        ask!(s)
        i = findfirst(==(UInt8('\n')), s.buf)
    end
    line = String(s.buf[1:i])
    deleteat!(s.buf, 1:i)
    keep ? line : chomp(line)
end

# ── run ─────────────────────────────────────────────────────

visible_names() = sort!([n for n in names(Main; all = true, imported = false)
                         if !(n in (:Base, :Core, :Main, :ans, :include, :eval, :RatKernel)) &&
                            !startswith(string(n), '#') && isdefined(Main, n)], by = string)

__repl_entry_display(value) = display(value)

# Counted in the latest world: the cell's definitions are newer than the
# request (an older world would not see them, and Julia 1.12 warns).
var_count() = length(Base.invokelatest(visible_names))

function error_text(err, bt)
    while err isa LoadError
        err = err.error
    end
    err isa InterruptException && return "InterruptException"
    # The trace ends at the cell's top-level scope, as in the REPL.
    frames = Base.scrub_repl_backtrace(bt)
    cut = findfirst(f -> f.func === :eval && endswith(string(f.file), "boot.jl"), frames)
    cut === nothing || deleteat!(frames, cut:length(frames))
    io = IOBuffer()
    Base.display_error(IOContext(io, :limit => true), err, frames)
    rstrip(String(take!(io)))
end

# The name matters: Julia's scrub_repl_backtrace cuts an error's stack
# trace at a frame whose function starts with __repl_entry, so a trace
# shows the cell's calls and none of the kernel's.
__repl_entry_eval_cell(code, name) = include_string(Main, code, name)

function run_cell(code::String)
    RUN[] += 1
    PLOTS[] = 0
    local value
    try
        # The latest world: what the cell defines, and the show methods of
        # packages it loads (Plots), are newer than the kernel's loop.
        value = Base.invokelatest(__repl_entry_eval_cell, code, "In[$(RUN[])]")
        Core.eval(Main, Expr(:(=), :ans, QuoteNode(value)))
        if value !== nothing && !REPL.ends_with_semicolon(code)
            Base.invokelatest(__repl_entry_display, value)
        end
    catch err
        return Dict("success" => false, "output" => "", "error" => Base.invokelatest(error_text, err, catch_backtrace()),
                    "vars" => var_count())
    end
    Dict("success" => true, "output" => "", "error" => "", "vars" => var_count())
end

# ── look ────────────────────────────────────────────────────

short_type(v) = replace(sprint(show, typeof(v)), ", " => ",", " " => "")

function preview(v)
    txt = try
        if v isa Function
            "$(length(methods(v))) method(s)"
        elseif v isa Module
            "module"
        elseif v isa AbstractArray || v isa AbstractDict || v isa AbstractSet
            summary(v)
        else
            sprint(show, v; context = :limit => true)
        end
    catch
        ""
    end
    txt = replace(txt, r"\s+" => " ")
    length(txt) > 100 ? first(txt, 99) * "…" : txt
end

function look_overview()
    vars = visible_names()
    head = "julia idle | $(length(vars)) vars"
    isempty(vars) && return head
    rows = [rpad(string(n), 20) * "  " * rpad(short_type(getfield(Main, n)), 12) * "  " * preview(getfield(Main, n)) for n in vars]
    join(vcat([head, ""], rows), "\n")
end

# A name, or a path into one (x, x.field, Mod.name, x[1], x["a"]);
# nothing is called, so looking never runs the user's code.
function safe_path(ex)
    ex isa Symbol && return true
    ex isa Union{Number,String,QuoteNode} && return true
    ex isa Expr || return false
    if ex.head == :. && length(ex.args) == 2
        return safe_path(ex.args[1]) && ex.args[2] isa QuoteNode
    elseif ex.head == :ref
        return all(safe_path, ex.args)
    end
    false
end

function look_at(symbol::String, full::Bool)
    ex = try
        Meta.parse(symbol)
    catch
        nothing
    end
    (ex === nothing || !safe_path(ex)) && return "$symbol: not found"
    v = try
        Core.eval(Main, ex)
    catch
        return "$symbol: not found"
    end
    io = IOBuffer()
    ctx = full ? IOContext(io, :module => Main) : text_context(io)
    show(ctx, MIME"text/plain"(), v)
    body = String(take!(io))
    if v isa Function
        body *= "\n" * sprint(show, MIME"text/plain"(), methods(v))
    end
    "$symbol: $(short_type(v))\n$body"
end

# ── complete ────────────────────────────────────────────────
# Julia's own REPL completion: the range it replaces becomes `start`.

function completion_kind(c)
    c isa REPLCompletions.KeywordCompletion && return "keyword"
    c isa REPLCompletions.PathCompletion && return "file"
    c isa REPLCompletions.PackageCompletion && return "module"
    c isa REPLCompletions.FieldCompletion && return "property"
    if c isa REPLCompletions.ModuleCompletion
        v = try
            getglobal(c.parent, Symbol(c.mod))
        catch
            nothing
        end
        v isa Module && return "module"
        v isa Function && return "function"
        v isa Type && return "class"
        return "variable"
    end
    "value"
end

function complete(code::String, cursor::Int)
    n = length(code)
    cursor = cursor < 0 || cursor > n ? n : cursor
    prefix = first(code, cursor)
    comps, range, should = REPLCompletions.completions(prefix, lastindex(prefix), Main)
    start = first(range) <= 1 ? 0 : length(prefix, 1, prevind(prefix, first(range)))
    matches = Dict{String,String}[]
    if should
        for c in comps[1:min(end, 100)]
            push!(matches, Dict("label" => REPLCompletions.completion_text(c), "kind" => completion_kind(c)))
        end
    end
    text = isempty(matches) ? "No completions." :
           join([rpad(replace(m["label"], " " => ""), 20) * " " * m["kind"] for m in matches], "\n")
    Dict("text" => text, "start" => start, "matches" => matches)
end

# ── main loop ───────────────────────────────────────────────

function handle(req)
    op = get(req, "op", "")
    use_project_environment()
    if op == "ping"
        Dict("ok" => true)
    elseif op == "run"
        run_cell(string(get(req, "code", "")))
    elseif op == "look_overview"
        Dict("text" => look_overview())
    elseif op == "look_at"
        Dict("text" => look_at(string(get(req, "at", "")), get(req, "full", false) === true))
    elseif op == "complete"
        c = get(req, "cursor", nothing)
        complete(string(get(req, "code", "")), c isa Integer ? Int(c) : -1)
    elseif op == "status"
        Dict("text" => "idle\nruntime_version: Julia $(VERSION)")
    else
        Dict("error" => "unknown op: $op")
    end
end

function main()
    Base.exit_on_sigint(false)
    get!(ENV, "GKSwstype", "nul")  # Plots.jl's GR: no window, files only
    connect_to_rat()
    pushdisplay(RatDisplay())
    @eval Base stdin = $(RatStdin(UInt8[]))
    use_project_environment()
    while true
        op = nothing
        replied = false
        try
            # A cancel that arrives between requests (late) is dropped.
            line = try
                readline(SOCK[]; keep = true)
            catch e
                e isa InterruptException ? nothing : rethrow()
            end
            line === nothing && continue
            isempty(line) && break          # rat has gone
            req = try
                parse_json(line)
            catch
                continue
            end
            req isa Dict || continue
            op = get(req, "op", "")
            op == "shutdown" && break
            op == "input" && continue       # an answer that came after its prompt ended
            reply = try
                Base.invokelatest(handle, req)
            catch err
                Dict("success" => false, "error" => error_text(err, catch_backtrace()))
            end
            disable_sigint(() -> send(reply))
            replied = true
        catch e
            e isa InterruptException || rethrow()
            # A cancel reached the kernel's own code: the request still
            # gets its one reply.
            if op !== nothing && op != "" && !replied
                disable_sigint(() -> send(op == "run" ? Dict("success" => false, "output" => "", "error" => "InterruptException") :
                                                     Dict("error" => "InterruptException")))
            end
        end
    end
end

end # module RatKernel

RatKernel.main()
