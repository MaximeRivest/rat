# JSON for rat's Julia kernel and frontend: the subset the protocol and
# MCP use, with the standard library only. Included into a module.

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
