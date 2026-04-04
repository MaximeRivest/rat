#!/usr/bin/env Rscript
# Minimal rat kernel for R.
# Speaks the rat kernel protocol: JSON lines over stdin/stdout.

library(jsonlite)

env <- new.env(parent = globalenv())

send <- function(obj) {
  cat(toJSON(obj, auto_unbox = TRUE), "\n", sep = "", file = stdout())
  flush(stdout())
}

visible_vars <- function() {
  nms <- ls(env)
  nms <- nms[!grepl("^\\.", nms)]
  nms
}

look_overview <- function() {
  vars <- visible_vars()
  if (length(vars) == 0) return("R idle | 0 vars")

  lines <- paste0("R idle | ", length(vars), " vars\n")
  for (nm in vars) {
    val <- get(nm, envir = env)
    cls <- paste(class(val), collapse = ", ")
    preview <- tryCatch(
      paste(capture.output(str(val, max.level = 0, give.attr = FALSE))[1]),
      error = function(e) "<error>"
    )
    lines <- paste0(lines, sprintf("%-20s  %-12s  %s\n", nm, cls, preview))
  }
  trimws(lines)
}

look_at <- function(symbol) {
  if (!exists(symbol, envir = env)) return(paste0(symbol, ": not found"))
  val <- get(symbol, envir = env)
  out <- capture.output(str(val))
  paste0(symbol, ": ", paste(class(val), collapse = ", "), "\n", paste(out, collapse = "\n"))
}

con <- file("stdin", "r")

while (TRUE) {
  line <- readLines(con, n = 1, warn = FALSE)
  if (length(line) == 0) break
  if (nchar(trimws(line)) == 0) next

  req <- tryCatch(fromJSON(line), error = function(e) NULL)
  if (is.null(req)) {
    send(list(success = FALSE, output = "", error = "invalid json"))
    next
  }

  op <- req$op

  if (op == "ping") {
    send(list(ok = TRUE))

  } else if (op == "run") {
    code <- req$code
    result <- tryCatch({
      out <- capture.output(eval(parse(text = code), envir = env))
      list(success = TRUE, output = paste(out, collapse = "\n"), error = "", vars = length(visible_vars()))
    }, error = function(e) {
      list(success = FALSE, output = "", error = conditionMessage(e), vars = length(visible_vars()))
    })
    send(result)

  } else if (op == "look_overview") {
    send(list(text = look_overview()))

  } else if (op == "look_at") {
    send(list(text = look_at(req$at)))

  } else if (op == "complete") {
    # Basic completions using utils::apropos
    prefix <- req$code
    if (!is.null(req$cursor) && req$cursor >= 0) {
      prefix <- substr(req$code, 1, req$cursor)
    }
    # Extract last token
    tokens <- strsplit(prefix, "[^a-zA-Z0-9._]")[[1]]
    token <- if (length(tokens) > 0) tail(tokens, 1) else ""
    matches <- apropos(paste0("^", token), mode = "function")
    if (length(matches) == 0) {
      send(list(text = "No completions."))
    } else {
      lines <- sapply(head(matches, 50), function(m) sprintf("%-20s function", m))
      send(list(text = paste(lines, collapse = "\n")))
    }

  } else if (op == "status") {
    v <- R.version
    ver <- paste0("R ", v$major, ".", v$minor)
    send(list(text = paste0("idle\nruntime_version: ", ver)))

  } else if (op == "shutdown") {
    break

  } else {
    send(list(error = paste0("unknown op: ", op)))
  }
}
