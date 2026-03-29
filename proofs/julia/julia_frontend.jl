#=
Julia REPL frontend — proof that eval can be intercepted.

Usage:
    julia -i julia_frontend.jl

Uses Julia's REAL REPL — 100% untouched. We add an ast_transform
hook that intercepts every expression before evaluation. The REPL
handles everything: modes (?, ;, ]), Unicode completion (\alpha→α),
syntax highlighting, history, multiline. We just see what's about
to be evaluated.

In production, the ast_transform would send code to the MCP server
instead of letting Julia eval it locally. For this proof, we log
the interception and let normal eval proceed.
=#

atreplinit() do repl
    # Wait for REPL to fully initialize, then patch
    @async begin
        while !isdefined(repl, :interface) || isnothing(repl.interface)
            sleep(0.05)
        end
        sleep(0.1)

        # ast_transforms is Julia's official hook for intercepting
        # expressions before they're evaluated. The REPL is untouched.
        push!(repl.ast_transforms, function(ast)
            # ── INTERCEPTION POINT ──
            # In production: send `string(ast)` to MCP server,
            # get result back, return a quoted result expression.
            # For proof: just log and pass through.
            printstyled("[rat] ", color=:blue, bold=true)
            printstyled("intercepted: ", color=:light_black)
            printstyled(string(ast), "\n", color=:light_black)

            return ast  # pass through — normal Julia eval handles it
        end)

        println()
        printstyled("rat ju", bold=true)
        println(" | Julia $(VERSION)")
        println("Proof mode — ast_transforms interception active")
        println("REPL is 100% native Julia. Every eval is intercepted.")
        println()
        println("Try:")
        println("  x = 42           — intercepted, then evaluated normally")
        println("  x + 100          — sees x from previous eval")
        println("  ?sqrt            — help mode (native, not intercepted)")
        println("  ;ls              — shell mode (native, not intercepted)")
        println("  \\alpha<tab>      — Unicode completion (native)")
        println("  ↑                — history (native)")
        println()
    end
end
