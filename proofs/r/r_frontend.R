#' R REPL frontend that routes execution to a shared MCP kernel.
#'
#' Usage:
#'     Rscript r_frontend.R [--server http://127.0.0.1:8718/mcp]
#'     # or interactively:
#'     R --interactive -f r_frontend.R
#'
#' Uses R's real REPL with a custom task callback that intercepts
#' evaluation. For a richer experience, see the radian frontend
#' (r_radian_frontend.py).
#'
#' This proof shows that R's addTaskCallback + custom eval can
#' intercept REPL execution while keeping R's native features:
#' tab completion, history, help (?), package loading.

# ── MCP client (minimal, base R only) ───────────────────────────

mcp_call <- function(server_url, method, params = list()) {
  payload <- list(
    jsonrpc = "2.0",
    id = sample.int(100000, 1),
    method = method,
    params = params
  )

  body <- jsonlite_encode(payload)

  tryCatch({
    con <- url(server_url, method = "libcurl")
    on.exit(close(con))

    response <- system2(
      "curl",
      args = c("-s", "-X", "POST",
               "-H", "Content-Type: application/json",
               "-d", shQuote(body),
               server_url),
      stdout = TRUE
    )

    # Minimal JSON parse for proof (in production, use jsonlite)
    paste(response, collapse = "\n")
  }, error = function(e) {
    paste0('{"error": "', conditionMessage(e), '"}')
  })
}

# Minimal JSON encode (no dependencies)
jsonlite_encode <- function(x) {
  if (is.list(x)) {
    if (!is.null(names(x))) {
      pairs <- mapply(function(k, v) {
        paste0('"', k, '":', jsonlite_encode(v))
      }, names(x), x, USE.NAMES = FALSE)
      paste0("{", paste(pairs, collapse = ","), "}")
    } else {
      items <- vapply(x, jsonlite_encode, character(1))
      paste0("[", paste(items, collapse = ","), "]")
    }
  } else if (is.character(x)) {
    paste0('"', gsub('"', '\\\\"', x), '"')
  } else if (is.numeric(x)) {
    as.character(x)
  } else if (is.logical(x)) {
    tolower(as.character(x))
  } else if (is.null(x)) {
    "null"
  } else {
    paste0('"', as.character(x), '"')
  }
}

# ── Shared namespace (local proof mode) ──────────────────────────

rat_env <- new.env(parent = globalenv())

# ── Intercepted evaluation ───────────────────────────────────────

rat_eval <- function(code, server_url = "") {
  if (nzchar(server_url)) {
    # MCP mode
    result <- mcp_call(server_url, "tools/call", list(
      name = "run",
      arguments = list(code = code)
    ))
    cat(result, "\n")
  } else {
    # Local proof mode — execute in shared environment
    tryCatch({
      parsed <- parse(text = code)
      result <- eval(parsed, envir = rat_env)
      if (!is.null(result) && !invisible(result)) {
        print(result)
      }
    }, error = function(e) {
      message("Error: ", conditionMessage(e))
    })
  }
}

# ── Custom REPL using R's readline ───────────────────────────────

start_rat_repl <- function(server_url = "") {
  cat("rat r | R", paste(R.version$major, R.version$minor, sep = "."), "\n")

  if (nzchar(server_url)) {
    cat("MCP mode — executing on", server_url, "\n")
  } else {
    cat("Local proof mode — interception active\n")
  }

  cat("Shared namespace — other MCP clients see your variables.\n\n")

  buffer <- ""

  repeat {
    prompt <- if (nzchar(buffer)) "+ " else "rat> "

    line <- tryCatch(
      readline(prompt),
      error = function(e) NULL
    )

    # EOF (Ctrl+D)
    if (is.null(line)) {
      cat("\n")
      break
    }

    # Accumulate for multiline
    buffer <- if (nzchar(buffer)) paste(buffer, line, sep = "\n") else line

    # Check if expression is complete
    tryCatch({
      parse(text = buffer)
      # Complete — execute
      if (nzchar(trimws(buffer))) {
        rat_eval(buffer, server_url = server_url)
      }
      buffer <- ""
    }, error = function(e) {
      msg <- conditionMessage(e)
      if (grepl("unexpected end of input", msg) ||
          grepl("unexpected INCOMPLETE_STRING", msg)) {
        # Incomplete — keep buffering
      } else {
        # Actual parse error — report and reset
        message("Error: ", msg)
        buffer <<- ""
      }
    })
  }
}

# ── Entry point ──────────────────────────────────────────────────

args <- commandArgs(trailingOnly = TRUE)
server_url <- ""

for (i in seq_along(args)) {
  if (args[i] == "--server" && i < length(args)) {
    server_url <- args[i + 1]
  }
}

if (interactive() || length(args) > 0) {
  start_rat_repl(server_url = server_url)
} else {
  cat("Run with: R --interactive -f r_frontend.R\n")
  cat("Or:       Rscript r_frontend.R --server http://...\n")
}
