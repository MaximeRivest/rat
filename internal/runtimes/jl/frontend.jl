# rat's Julia frontend: Julia's own REPL — its prompt, modes (? help,
# ; shell, ] Pkg), history, Unicode completion (\alpha<Tab>) — with the
# code of julia> evaluated in rat's shared kernel, where every client
# (notebooks, agents, other terminals) sees the same variables.
#
#   julia -i --startup-file=no --banner=no frontend.jl <mcp_url> <name>
#
# Output streams as the kernel prints it; readline() in the kernel asks
# here; Ctrl-C interrupts the kernel's code (twice, 3 s apart, stops a
# kernel whose code does not listen); Tab completes from the kernel's
# live namespace. Help, shell and Pkg modes run here: they read files
# and docs, and Pkg changes the same project environment on disk.

module RatFrontend

using Downloads

include(joinpath(@__DIR__, "json.jl"))

const URL = ARGS[1]
const NAME = length(ARGS) > 1 ? ARGS[2] : "jl"
const SESSION = Ref("")
const NEXT_ID = Ref(0)

# ── MCP over streamable HTTP ────────────────────────────────

# Receives the response as it arrives: JSON, or server-sent events whose
# notifications (rat/output, rat/input_request) are handled at once.
mutable struct Sink <: IO
    buf::Vector{UInt8}
    raw::IOBuffer          # everything, for a plain JSON answer
    on_message::Function
end
Base.isopen(::Sink) = true
Base.write(s::Sink, b::UInt8) = (push!(s.buf, b); write(s.raw, b); b == UInt8('\n') && drain!(s); 1)
function Base.unsafe_write(s::Sink, p::Ptr{UInt8}, n::UInt)
    bytes = copy(unsafe_wrap(Array, p, n))
    append!(s.buf, bytes)
    write(s.raw, bytes)
    drain!(s)
    Int(n)
end
function drain!(s::Sink)
    while (i = findfirst(==(UInt8('\n')), s.buf)) !== nothing
        line = String(s.buf[1:i-1])
        deleteat!(s.buf, 1:i)
        if startswith(line, "data: ")
            msg = try parse_json(line[7:end]) catch; nothing end
            msg isa Dict && s.on_message(msg)
        end
    end
end

function post(method, params; on_message = m -> nothing, notify = false)
    body = IOBuffer()
    msg = Dict{String,Any}("jsonrpc" => "2.0", "method" => method, "params" => params)
    id = 0
    if !notify
        id = (NEXT_ID[] += 1)
        msg["id"] = id
    end
    json(body, msg)
    headers = ["Content-Type" => "application/json", "Accept" => "application/json, text/event-stream"]
    isempty(SESSION[]) || push!(headers, "Mcp-Session-Id" => SESSION[])
    result = Ref{Any}(nothing)
    got = function (m)
        if get(m, "id", nothing) == id
            result[] = m
        else
            on_message(m)
        end
    end
    sink = Sink(UInt8[], IOBuffer(), got)
    resp = Downloads.request(URL; method = "POST", headers = headers, input = IOBuffer(take!(body)), output = sink, throw = false)
    push!(sink.buf, UInt8('\n'))
    drain!(sink)
    if resp isa Downloads.Response
        for (k, v) in resp.headers
            lowercase(k) == "mcp-session-id" && (SESSION[] = v)
        end
    end
    if result[] === nothing
        raw = strip(String(take!(sink.raw)))
        if startswith(raw, "{")                  # a plain JSON answer
            m = try parse_json(raw) catch; nothing end
            m isa Dict && (result[] = m)
        end
    end
    result[]
end

tool(name, args; kw...) = post("tools/call", Dict("name" => name, "arguments" => args); kw...)

function text_of(r)
    r isa Dict || return ""
    res = get(r, "result", Dict())
    join([string(get(c, "text", "")) for c in get(res, "content", []) if c isa Dict], "\n")
end
is_error(r) = r isa Dict && (haskey(r, "error") || get(get(r, "result", Dict()), "isError", false) == true)

function connect()
    r = post("initialize", Dict("protocolVersion" => "2025-03-26", "capabilities" => Dict(),
                                "clientInfo" => Dict("name" => "julia@" * gethostname(), "version" => "0.1.0")))
    r === nothing && error("rat does not answer at $URL")
    post("notifications/initialized", Dict(); notify = true)
    r
end

# ── evaluation ──────────────────────────────────────────────

const STATUS = r"\n*[✓✗] \d+(?:\.\d+)?m?s(?: \| \d+ vars?)?\s*$"

"""The code of one julia> prompt, run in the kernel. Output streams;
prompts are answered from this terminal; Ctrl-C interrupts."""
function remote(code::String)
    seen = IOBuffer()
    inputs = Channel{String}(8)
    echo = Ref("")   # the kernel echoes an answer; this terminal showed it already
    on_message = function (m)
        method = get(m, "method", "")
        params = get(m, "params", Dict())
        if method == "rat/output"
            t = string(get(params, "text", ""))
            print(seen, t)
            if !isempty(echo[]) && startswith(t, echo[])
                t = t[nextind(t, lastindex(echo[])):end]
                echo[] = ""
            end
            print(stdout, t)
            flush(stdout)
        elseif method == "rat/input_request"
            put!(inputs, string(get(params, "prompt", "")))
        end
    end
    # The request runs on another thread: Ctrl-C (SIGINT) reaches the
    # main thread, which waits here, and never cuts the stream.
    task = Threads.nthreads(:default) > 0 && Threads.threadpoolsize(:default) > 0 && Threads.nthreads() > 1 ?
        Threads.@spawn(:default, tool("run", Dict("code" => code); on_message)) :
        @async tool("run", Dict("code" => code); on_message)
    answering = @async for _ in inputs
        text = readline(stdin)
        echo[] = text * "\n"
        tool("run", Dict("input" => text * "\n"))
    end
    local r
    while true
        try
            r = fetch(task)
            break
        catch e
            interrupted = e isa InterruptException || (e isa TaskFailedException && e.task.result isa InterruptException)
            interrupted || rethrow()
            if istaskdone(task) && e isa TaskFailedException
                # The interrupt landed in the request itself: the run goes
                # on in the kernel; say so and stop waiting here.
                tool("ctl", Dict("op" => "cancel"))
                printstyled("\n[interrupted — the kernel was asked to stop]\n"; color = :light_black)
                close(inputs)
                return nothing
            end
            printstyled("\n[interrupting…]\n"; color = :light_black)
            Threads.@spawn tool("ctl", Dict("op" => "cancel"))
        end
    end
    close(inputs)
    streamed = String(take!(seen))
    full = replace(text_of(r), STATUS => "")
    rest = startswith(full, streamed) ? full[nextind(full, lastindex(streamed)):end] : (isempty(streamed) ? full : "")
    rest = replace(rest, r"^\n+" => "")
    if is_error(r)
        printstyled(rstrip(rest), "\n"; color = :red)
    elseif !isempty(strip(rest))
        println(rstrip(rest))
    elseif !isempty(streamed) && !endswith(streamed, "\n")
        println()
    end
    nothing
end

# ── completion from the kernel ──────────────────────────────

# Tab: the kernel's completions for the code before the cursor, replacing
# the text the kernel says they replace. Returns (names, bytes from, to).
function kernel_complete(full::String, pos::Int)
    prefix = String(codeunits(full)[1:pos])
    r = tool("look", Dict("code" => prefix, "cursor" => length(prefix)))
    sc = r isa Dict ? get(get(r, "result", Dict()), "structuredContent", nothing) : nothing
    (sc isa Dict && haskey(sc, "start")) || return String[], pos, pos
    start = Int(sc["start"])
    startbyte = start == 0 ? 0 : ncodeunits(first(prefix, start))
    String[string(get(m, "label", "")) for m in get(sc, "matches", []) if m isa Dict], startbyte, pos
end

# ── the REPL ────────────────────────────────────────────────

function install(repl)
    if !hasfield(typeof(repl), :interface)
        # A terminal Julia calls "not fully functional" (TERM=dumb) gets
        # its basic REPL, which has no modes to hook: say so, rather than
        # evaluate here where no other client sees it.
        printstyled("[rat] this terminal is not fully functional (TERM=", get(ENV, "TERM", ""), "): ",
                    "the julia> prompt would run here, not in the shared kernel. Use `rat run jl` or a full terminal.\n"; color = :red)
        exit(1)
    end
    # The REPL module of the running REPL (a script's `import REPL` may
    # load another copy, whose types it would not accept).
    R = parentmodule(typeof(repl))
    LE = R.LineEdit
    # Completion: a provider of that module's kind (defined, then made,
    # in the latest world).
    provider = Core.eval(@__MODULE__, quote
        struct KernelCompletions <: $(LE).CompletionProvider end
        function $(LE).complete_line(::KernelCompletions, s::$(LE).PromptState, mod::Module; hint::Bool = false)
            hint && return $(LE).NamedCompletion[], 0 => 0, false   # not on every keystroke
            names, from, to = kernel_complete($(LE).input_string(s), position(s))
            $(LE).NamedCompletion[$(LE).NamedCompletion(n) for n in names], from => to, !isempty(names)
        end
        KernelCompletions()
    end)
    # The REPL builds its interface only when there is none yet: build
    # it here, change the julia> mode, and the REPL uses it.
    repl.interface = R.setup_interface(repl)
    main = repl.interface.modes[1]
    main.on_done = R.respond(repl, main) do line
        :(Main.RatFrontend.remote($line))
    end
    main.complete = provider
end

function start()
    r = connect()
    status = text_of(tool("ctl", Dict("op" => "status")))
    version = match(r"runtime_version: (.+)", status)
    printstyled("rat "; color = :blue, bold = true)
    println(NAME, " — ", version === nothing ? "Julia" : version[1], " on ", URL)
    printstyled("Shared namespace · other clients see your variables · Ctrl-D to detach\n\n"; color = :light_black)
    atreplinit(install)
    atexit(() -> println("\nDetached. Kernel still running. Reconnect: rat ", NAME))
end

end # module

RatFrontend.start()
