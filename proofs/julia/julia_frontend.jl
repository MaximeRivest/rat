#=
Julia REPL frontend that routes execution to a shared MCP kernel.

Usage:
    julia julia_frontend.jl [--server http://127.0.0.1:8719/mcp]

Uses Julia's real REPL — modes (?, ;, ]), Unicode completion, syntax
highlighting. Only the eval step is redirected to the MCP server.

For this proof-of-concept, execution runs locally but through an
interception layer that proves the hook works. Replace `local_eval`
with `mcp_eval` to connect to a real MCP server.
=#

using REPL
using REPL.LineEdit
import REPL: LineEdit

# ── MCP client (minimal) ────────────────────────────────────────

function mcp_call(server_url::String, method::String, params::Dict=Dict())
    payload = Dict(
        "jsonrpc" => "2.0",
        "id" => rand(1:100000),
        "method" => method,
        "params" => params,
    )

    try
        io = IOBuffer()
        body = JSON_encode(payload)
        cmd = `curl -s -X POST -H "Content-Type: application/json" -d $body $server_url`
        response = read(cmd, String)
        result = JSON_decode(response)
        return get(result, "result", Dict())
    catch e
        return Dict("error" => string(e))
    end
end

# Minimal JSON encode/decode (no dependencies)
function JSON_encode(obj)
    if obj isa Dict
        pairs = join(["\"$(k)\":$(JSON_encode(v))" for (k, v) in obj], ",")
        return "{$pairs}"
    elseif obj isa String
        return "\"$(escape_string(obj))\""
    elseif obj isa Number
        return string(obj)
    elseif obj isa Vector
        return "[$(join(map(JSON_encode, obj), ","))]"
    elseif obj isa Nothing
        return "null"
    else
        return "\"$(escape_string(string(obj)))\""
    end
end

function JSON_decode(s::String)
    # For the proof, shell out to Julia's built-in JSON-like parsing
    # In production, use JSON3.jl
    return Meta.parse("Dict(" * s * ")") |> eval
end

# ── Intercepted evaluation ───────────────────────────────────────

# Shared state (simulates what the MCP kernel would hold)
const RAT_MODULE = Module(:RatNamespace)

function rat_eval(code::String; server_url::String="")
    if !isempty(server_url)
        # MCP mode — send to server
        result = mcp_call(server_url, "tools/call", Dict(
            "name" => "run",
            "arguments" => Dict("code" => code),
        ))
        content = get(result, "content", [])
        for item in content
            if item isa Dict && get(item, "type", "") == "text"
                text = get(item, "text", "")
                if !isempty(text)
                    println(text)
                end
            end
        end
        return nothing
    else
        # Local mode — execute in shared module (proof that interception works)
        try
            result = Core.eval(RAT_MODULE, Meta.parse(code))
            return result
        catch e
            showerror(stderr, e, catch_backtrace())
            println(stderr)
            return nothing
        end
    end
end

# ── REPL setup ───────────────────────────────────────────────────

function start_rat_repl(; server_url::String="")
    println("rat ju | Julia $(VERSION)")
    if isempty(server_url)
        println("Local proof mode — interception active, executing in shared module")
    else
        println("MCP mode — executing on $server_url")
    end
    println("Shared namespace — other MCP clients see your variables.")
    println()

    # Get the active REPL
    repl = Base.active_repl

    if repl === nothing
        @warn "No active REPL found. Run this from `julia -i julia_frontend.jl`"
        return
    end

    # Get the main (julia) mode
    main_mode = repl.interface.modes[1]

    # Override the on_done callback to intercept execution
    original_on_done = main_mode.on_done

    main_mode.on_done = function(s, buf, ok)
        if !ok
            return original_on_done(s, buf, ok)
        end

        code = String(take!(copy(buf)))
        if isempty(strip(code))
            return original_on_done(s, buf, ok)
        end

        # Execute through rat (intercepted)
        result = rat_eval(code; server_url=server_url)

        # Display result (Julia REPL style)
        if result !== nothing
            display(result)
        end

        # Reset for next input
        return true
    end

    println("[rat] REPL interception active. Try: x = 42")
end

# ── Entry point ──────────────────────────────────────────────────

# Parse args
server_url = ""
for (i, arg) in enumerate(ARGS)
    if arg == "--server" && i < length(ARGS)
        server_url = ARGS[i + 1]
    end
end

# Hook into the REPL at startup
atreplinit() do repl
    start_rat_repl(; server_url=server_url)
end

# If running interactively, hook now
if isdefined(Base, :active_repl) && Base.active_repl !== nothing
    start_rat_repl(; server_url=server_url)
end
