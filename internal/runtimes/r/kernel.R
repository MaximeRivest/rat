#!/usr/bin/env Rscript
# rat kernel for R.
#
# Speaks the rat kernel protocol over the private connection rat opens
# (transport: socket, see KERNEL-PROTOCOL.md). Whatever the user's code
# prints — cat, print, message, warnings, C code, programs started with
# system() — goes to this process's stdout/stderr, which rat streams live.
#
# Cells behave as in R's console and in R Markdown: every top-level
# expression is evaluated in turn and its visible value printed; a
# warning is reported after the expression that raised it; an error stops
# the cell. R runs non-interactively (interactive() is FALSE), as knitr
# does. readline() asks the notebook's reader. Plots go to PNG files, one
# per page, announced as __RAT_PLOT__:<path> lines where the page ends.
#
# The kernel's own code lives in an environment whose parent is base R,
# outside the global environment: rm(list = ls(all.names = TRUE)) cannot
# remove it, and a user's `cat <- ...` cannot change what it calls.

local({
  if (!requireNamespace("jsonlite", quietly = TRUE)) {
    stop("rat's R kernel needs the R package jsonlite: for a notebook, `rat ensure <notebook>` (Chattering: \"make it run\") installs it; otherwise `rat install r`")
  }

  toJSON <- jsonlite::toJSON
  fromJSON <- jsonlite::fromJSON
  user <- globalenv()
  run_count <- 0L

  # ── protocol connection ────────────────────────────────────

  addr <- Sys.getenv("RAT_PROTOCOL_TCP_ADDR")
  if (!nzchar(addr)) stop("RAT_PROTOCOL_TCP_ADDR is not set: this kernel is started by rat")
  host <- sub(":[0-9]+$", "", addr)
  port <- as.integer(sub("^.*:", "", addr))
  token <- Sys.getenv("RAT_PROTOCOL_TOKEN")
  Sys.unsetenv(c("RAT_PROTOCOL_TCP_ADDR", "RAT_PROTOCOL_TOKEN"))
  # A socket read returns nothing both when it times out and when rat
  # closes the connection; with a year-long timeout, an early empty read
  # means rat has gone.
  read_timeout <- 365L * 24L * 3600L
  con <- socketConnection(host, port, blocking = TRUE, open = "r+b",
                                timeout = read_timeout, encoding = "UTF-8")

  send <- function(obj) {
    writeLines(as.character(toJSON(obj, auto_unbox = TRUE, null = "null", na = "null", digits = NA)),
                     con, useBytes = TRUE)
    flush(con)
  }

  # Next request line, or NULL once rat is gone. An interrupt while
  # waiting is a cancel that came late: nothing runs, so it is dropped.
  read_line <- function() {
    repeat {
      started <- Sys.time()
      line <- tryCatch(readLines(con, n = 1L, encoding = "UTF-8", warn = FALSE),
                             interrupt = function(e) NA_character_)
      if (length(line) == 1L && is.na(line)) next
      if (length(line) == 1L) return(line)
      waited <- as.numeric(difftime(Sys.time(), started, units = "secs"))
      if (waited < read_timeout - 60) return(NULL)
    }
  }

  send(list(op = "protocol_hello", token = token))

  # ── the project's R library ────────────────────────────────
  # `rat ensure` installs a notebook's declared packages into
  # <project>/.rat/r-library/<platform>/R-<x.y> (unless the project uses
  # renv). It goes first on the library path, also when it appears while
  # the kernel runs: a package just installed loads without a restart.

  project_library <- file.path(getwd(), ".rat", "r-library", R.version$platform,
                               paste0("R-", R.version$major, ".", sub("[.].*", "", R.version$minor)))
  use_project_library <- function() {
    if (nzchar(Sys.getenv("RENV_PROJECT")) || !dir.exists(project_library)) return(invisible())
    if (!normalizePath(project_library) %in% normalizePath(.libPaths())) .libPaths(c(project_library, .libPaths()))
    invisible()
  }
  use_project_library()

  # ── output helpers ─────────────────────────────────────────

  out <- function(...) cat(..., sep = "", file = stdout())
  err <- function(...) cat(..., sep = "", file = stderr())

  visible_vars <- function() {
    nms <- ls(user)
    nms[!startsWith(nms, ".")]
  }

  # R's own wording of a condition's call, without the kernel's frame.
  call_text <- function(call) {
    if (is.null(call)) return(NULL)
    txt <- paste(deparse(call, nlines = 1L, width.cutoff = 60L), collapse = "")
    if (startsWith(txt, "eval(expr, user)") || startsWith(txt, "eval(expr, envir")) return(NULL)
    txt
  }

  # "Error in f(x) : msg", or "Error: msg", as R's console writes it.
  format_error <- function(e) {
    msg <- conditionMessage(e)
    ct <- call_text(conditionCall(e))
    if (is.null(ct)) return(paste0("Error: ", msg))
    head <- paste0("Error in ", ct, " : ")
    first <- strsplit(msg, "\n", fixed = TRUE)[[1]][1]
    if (is.na(first)) first <- ""
    if (grepl("\n", msg, fixed = TRUE) || nchar(head) + nchar(first) > 77L) {
      paste0(head, "\n  ", msg)
    } else {
      paste0(head, msg)
    }
  }

  format_warning <- function(w, numbered = NULL) {
    ct <- call_text(conditionCall(w))
    msg <- conditionMessage(w)
    lead <- if (is.null(numbered)) "" else paste0(numbered, ": ")
    if (is.null(ct)) paste0(lead, msg) else paste0(lead, "In ", ct, " : ", msg)
  }

  # Deferred warnings, reported after the top-level expression as R's
  # console does; warnings() then lists them.
  report_warnings <- function(ws) {
    n <- length(ws)
    if (n == 0L) return(invisible())
    # warnings() reads last.warning, which R keeps in base; R lets the
    # kernel replace it only once R itself created it.
    if (exists("last.warning", envir = baseenv(), inherits = FALSE)) {
      last <- lapply(ws, conditionCall)
      names(last) <- vapply(ws, conditionMessage, "")
      try({
        unlockBinding("last.warning", baseenv())
        assign("last.warning", last, envir = baseenv())
        lockBinding("last.warning", baseenv())
      }, silent = TRUE)
    }
    if (n == 1L) {
      err("Warning message:\n", format_warning(ws[[1]]), "\n")
    } else if (n <= 10L) {
      err("Warning messages:\n", paste(vapply(seq_len(n), function(i) format_warning(ws[[i]], i), ""), collapse = "\n"), "\n")
    } else {
      err("There were ", n, " warnings (use warnings() to see them)\n")
    }
  }

  # ── plots ──────────────────────────────────────────────
  # The default device (options(device)) writes one PNG per page. A page
  # is announced once finished — when the next one starts (plot.new and
  # grid.newpage hooks) or when the cell ends — so a plot built over
  # several lines (plot, then abline) is one image, and text printed
  # between two plots stays between them. The device creates a page's
  # file when the page starts: a device's newest file is unfinished while
  # the device is open.

  plot_dir <- file.path(Sys.getenv("XDG_CACHE_HOME", file.path(path.expand("~"), ".cache")), "rat", "plots")
  dir.create(plot_dir, recursive = TRUE, showWarnings = FALSE)
  plots <- new.env()
  plots$prefix <- ""
  plots$devices <- list()
  plots$shown <- character()

  rat_device <- function(...) {
    if (!nzchar(plots$prefix)) start_plots()
    width <- getOption("repr.plot.width", 8)
    height <- getOption("repr.plot.height", 16 / 3)
    res <- getOption("repr.plot.res", 150)
    type <- if (isTRUE(capabilities("cairo"))) "cairo" else getOption("bitmapType")
    prefix <- paste0(plots$prefix, length(plots$devices) + 1L, "-")
    grDevices::png(paste0(prefix, "%03d.png"), width = width, height = height,
                   units = "in", res = res, type = type, bg = "white")
    plots$devices[[length(plots$devices) + 1L]] <- list(number = grDevices::dev.cur(), prefix = prefix)
  }
  options(device = rat_device)

  pages_of <- function(prefix) {
    pattern <- paste0("^", basename(prefix), "[0-9]+\\.png$")
    sort(list.files(plot_dir, pattern = pattern, full.names = TRUE))
  }

  show_finished_pages <- function(..., final = FALSE) {
    open <- grDevices::dev.list()
    for (d in plots$devices) {
      files <- pages_of(d$prefix)
      if (!final && d$number %in% open) files <- utils::head(files, -1L)
      for (f in files[!files %in% plots$shown]) {
        plots$shown <- c(plots$shown, f)
        size <- file.info(f)$size
        if (!is.na(size) && size > 0) out("__RAT_PLOT__:", f, "\n")
      }
    }
    invisible()
  }
  setHook("plot.new", function(...) show_finished_pages(), "append")
  setHook("grid.newpage", function(...) show_finished_pages(), "append")

  start_plots <- function() {
    plots$prefix <- file.path(plot_dir, sprintf("r-%d-%d-", Sys.getpid(), run_count))
    plots$devices <- list()
    plots$shown <- character()
  }

  end_plots <- function() {
    for (d in plots$devices) {
      if (d$number %in% grDevices::dev.list()) try(grDevices::dev.off(d$number), silent = TRUE)
    }
    show_finished_pages(final = TRUE)
    plots$devices <- list()
    plots$prefix <- ""
  }

  # ── rich displays: htmlwidgets ────────────────────────────
  # A widget (plotly, leaflet, DT, …) prints as a self-contained HTML
  # page — its JavaScript and CSS inlined — saved as a display bundle
  # (Jupyter's display_data: {data: {mime: content}, metadata}) and
  # announced as __RAT_DISPLAY__:<bundle.json>. Printing a widget no
  # longer tries to open a browser.

  inline_dependency <- function(dep) {
    dir <- dep$src$file
    if (!is.null(dir) && !is.null(dep$package)) dir <- system.file(dir, package = dep$package)
    read_all <- function(f) paste(readLines(file.path(dir, f), warn = FALSE, encoding = "UTF-8"), collapse = "\n")
    out <- character()
    for (css in dep$stylesheet) {
      out <- c(out, if (is.null(dir)) sprintf('<link rel="stylesheet" href="%s/%s">', dep$src$href, css)
                    else paste0("<style>", read_all(css), "</style>"))
    }
    for (js in dep$script) {
      f <- if (is.list(js)) js$src else js
      out <- c(out, if (is.null(dir)) sprintf('<script src="%s/%s"></script>', dep$src$href, f)
                    else paste0("<script>", gsub("</script", "<\\/script", read_all(f), fixed = TRUE), "</script>"))
    }
    if (!is.null(dep$head)) out <- c(out, dep$head)
    paste(out, collapse = "\n")
  }

  widget_page <- function(widget) {
    rendered <- htmltools::renderTags(htmltools::as.tags(widget, standalone = TRUE))
    deps <- htmltools::resolveDependencies(rendered$dependencies)
    paste0("<!DOCTYPE html>\n<html><head><meta charset=\"utf-8\">\n",
           paste(vapply(deps, inline_dependency, ""), collapse = "\n"), "\n", rendered$head,
           "\n</head><body style=\"margin:0\">\n", rendered$html, "\n</body></html>\n")
  }

  displays <- new.env()
  displays$n <- 0L

  show_display <- function(data, metadata = list()) {
    displays$n <- displays$n + 1L
    path <- file.path(plot_dir, sprintf("r-%d-%d-display-%d.json", Sys.getpid(), run_count, displays$n))
    tmp <- paste0(path, ".tmp")
    writeLines(as.character(toJSON(list(data = data, metadata = metadata), auto_unbox = TRUE, digits = NA)), tmp, useBytes = TRUE)
    file.rename(tmp, path)
    out("__RAT_DISPLAY__:", path, "\n")
    invisible()
  }

  print_widget <- function(x, ...) {
    page <- tryCatch(widget_page(x), error = function(e) NULL)
    if (is.null(page)) {
      out("<", class(x)[1], " widget: could not be rendered>\n")
      return(invisible(x))
    }
    height <- suppressWarnings(as.numeric(x$height))
    if (!length(height) || is.na(height)) height <- 420
    show_display(list(`text/html` = page, `text/plain` = paste0("<", class(x)[1], ">")),
                 list(`text/html` = list(height = height + 20)))
    invisible(x)
  }
  register_widgets <- function(...) {
    registerS3method("print", "htmlwidget", print_widget, envir = asNamespace("htmlwidgets"))
  }
  if ("htmlwidgets" %in% loadedNamespaces()) register_widgets()
  setHook(packageEvent("htmlwidgets", "onLoad"), register_widgets)

  # ── input ──────────────────────────────────────────────────
  # readline() asks whoever runs the cell (Chattering, rat run, an agent)
  # through the protocol. The prompt and the answer stay in the output,
  # as in a console.

  ask <- function(prompt = "") {
    prompt <- as.character(prompt)
    out(prompt)
    send(list(op = "input_request", prompt = prompt))
    repeat {
      line <- readLines(con, n = 1L, encoding = "UTF-8", warn = FALSE)
      if (length(line) == 0L) stop("rat went away while waiting for input")
      req <- tryCatch(fromJSON(line, simplifyVector = TRUE), error = function(e) NULL)
      if (is.list(req) && identical(req$op, "input")) break
    }
    send(list(op = "input_delivered"))
    text <- if (is.null(req$text)) "" else as.character(req$text)
    text <- sub("\r?\n$", "", gsub("\r\n", "\n", text))
    out(text, "\n")
    text
  }
  unlockBinding("readline", baseenv())
  assign("readline", ask, envir = baseenv())
  lockBinding("readline", baseenv())

  # ── run ────────────────────────────────────────────────────

  # Autoprint as R's top level does: print() looked up from the global
  # environment, so the user's methods and overrides apply.
  autoprint <- function(value) {
    eval(quote(print(x)), list(x = value), user)
  }

  # .Last.value, as the console sets it. R locks the binding.
  set_last_value <- function(value) {
    unlockBinding(".Last.value", baseenv())
    assign(".Last.value", value, envir = baseenv())
    lockBinding(".Last.value", baseenv())
  }

  run_cell <- function(code) {
    exprs <- tryCatch(parse(text = code, keep.source = TRUE, srcfile = NULL),
                            error = function(e) e)
    if (inherits(exprs, "error")) {
      msg <- sub("^<text>:", "", conditionMessage(exprs))
      return(list(success = FALSE, output = "", error = paste0("Error: ", msg)))
    }
    run_count <<- run_count + 1L
    start_plots()
    on.exit(end_plots())
    for (i in seq_along(exprs)) {
      expr <- exprs[[i]]
      warnings <- list()
      trace <- NULL
      result <- tryCatch(
        withCallingHandlers({
          value <- withVisible(eval(expr, user))
          if (value$visible) autoprint(value$value)
          set_last_value(value$value)
          NULL
        }, warning = function(w) {
          level <- getOption("warn", 0)
          if (level >= 2) return() # R turns it into an error
          if (level == 1) {
            err("Warning", if (is.null(call_text(conditionCall(w)))) ": " else " in ",
                sub("^In ", "", format_warning(w)), "\n")
          } else if (level == 0) {
            warnings[[length(warnings) + 1L]] <<- w
          }
          invokeRestart("muffleWarning")
        }, error = function(e) {
          # For traceback(): the calls from the cell down to the error.
          trace <<- sys.calls()
        }),
        error = function(e) e,
        interrupt = function(e) e)
      report_warnings(warnings)
      if (inherits(result, "interrupt")) {
        return(list(success = FALSE, output = "", error = "Interrupted", vars = length(visible_vars())))
      }
      if (inherits(result, "error")) {
        return(list(success = FALSE, output = "", error = paste0(format_error(result), format_traceback(trace)),
                    vars = length(visible_vars())))
      }
    }
    list(success = TRUE, output = "", error = "", vars = length(visible_vars()))
  }

  # The calls from the cell down to the error, as Jupyter's R kernel
  # lists them (a notebook has no console to call traceback() in). Empty
  # when the error is in the cell's own line.
  format_traceback <- function(calls) {
    if (is.null(calls)) return("")
    texts <- vapply(calls, function(cl) paste(deparse(cl, nlines = 1L), collapse = ""), "")
    # The cell's code starts after the kernel's eval frames (eval makes
    # two) and ends where R's condition plumbing begins: the handler (a
    # function called directly) or .handleSimpleError for C-level errors.
    evals <- which(startsWith(texts, "eval(expr, user)"))
    if (length(evals) == 0L) return("")
    from <- evals[1]
    while ((from + 1L) %in% evals) from <- from + 1L
    plumbing <- which(vapply(seq_along(calls), function(i) {
      f <- calls[[i]][[1]]
      i > from && (is.function(f) || identical(f, as.name(".handleSimpleError")))
    }, TRUE))
    to <- if (length(plumbing)) plumbing[1] - 1L else length(calls)
    if (to <= from) return("")
    texts <- texts[seq.int(from + 1L, to)]
    if (length(texts) < 2L) return("")
    texts <- ifelse(nchar(texts) > 100, paste0(substr(texts, 1, 99), "\u2026"), texts)
    n <- length(texts)
    lines <- paste0(seq_len(n), ". ", texts)
    if (n > 15) lines <- c(lines[1:5], sprintf("\u2026 %d more", n - 14), lines[(n - 8):n])
    paste0("\nTraceback:\n", paste(lines, collapse = "\n"))
  }

  # ── look ───────────────────────────────────────────────────

  short_class <- function(val) {
    cl <- class(val)[1]
    gsub("[[:space:]]+", "_", cl)
  }

  preview <- function(val) {
    txt <- tryCatch({
      if (is.data.frame(val)) {
        sprintf("%d rows \u00d7 %d cols: %s", nrow(val), ncol(val),
                      paste(utils::head(names(val), 6), collapse = ", "))
      } else if (is.function(val)) {
        paste(deparse(args(val))[1], collapse = "")
      } else if (is.environment(val)) {
        format(val)
      } else {
        paste(utils::capture.output(utils::str(val, max.level = 0, give.attr = FALSE, vec.len = 3))[1], collapse = "")
      }
    }, error = function(e) "")
    txt <- trimws(gsub("[[:space:]]+", " ", txt))
    if (nchar(txt) > 100) txt <- paste0(substr(txt, 1, 99), "\u2026")
    txt
  }

  look_overview <- function() {
    vars <- visible_vars()
    head <- sprintf("R idle | %d vars", length(vars))
    if (length(vars) == 0L) return(head)
    rows <- vapply(vars, function(nm) {
      val <- get(nm, envir = user)
      sprintf("%-20s  %-12s  %s", nm, short_class(val), preview(val))
    }, "")
    paste(c(head, "", rows), collapse = "\n")
  }

  # A name, or a path into one: x, df$col, obj@slot, pkg::fun, x[["a"]].
  # Nothing is called, so looking never runs the user's code.
  safe_path <- function(expr) {
    if (is.name(expr) || is.atomic(expr)) return(TRUE)
    if (!is.call(expr)) return(FALSE)
    f <- as.character(expr[[1]])
    if (!f %in% c("$", "@", "::", ":::", "[[")) return(FALSE)
    all(vapply(as.list(expr)[-1], safe_path, TRUE))
  }

  look_at <- function(symbol, full = FALSE) {
    expr <- tryCatch(str2lang(symbol), error = function(e) NULL)
    if (is.null(expr) || !safe_path(expr)) return(paste0(symbol, ": not found"))
    val <- tryCatch(eval(expr, user), error = function(e) e)
    if (inherits(val, "error")) return(paste0(symbol, ": not found"))
    lines <- utils::capture.output(utils::str(val, give.attr = FALSE, list.len = if (full) 1e4 else 50))
    if (is.function(val)) lines <- utils::capture.output(print(val))
    if (!full && length(lines) > 60) lines <- c(lines[1:60], sprintf("\u2026 %d more lines", length(lines) - 60))
    paste0(symbol, ": ", paste(class(val), collapse = ", "), "\n", paste(lines, collapse = "\n"))
  }

  # ── complete ───────────────────────────────────────────────
  # R's own completion engine (the console's TAB). Each match replaces
  # the token it found — data.fr → data.frame, df$co → df$col1 — and the
  # reply says where that token starts.

  utils::rc.settings(ipck = TRUE)

  # What a match is, by looking its name up — never by evaluating it:
  # a match like `names<-.POSIXlt` parses as an assignment.
  completion_kind <- function(m) {
    if (endsWith(m, "=")) return("argument")
    if (endsWith(m, "::")) return("module")
    if (grepl("[$@]", m)) return("variable")
    name <- sub("\\($", "", m)
    parts <- strsplit(name, ":::?")[[1]]
    val <- if (length(parts) == 2L) {
      if (parts[1] %in% loadedNamespaces()) get0(parts[2], envir = asNamespace(parts[1]), inherits = FALSE)
    } else {
      get0(name, envir = user)
    }
    if (is.function(val)) return("function")
    if (!is.null(val)) return("variable")
    if (length(parts) == 1L && nzchar(system.file(package = name))) return("module")
    "value"
  }

  complete <- function(code, cursor) {
    if (is.null(cursor) || cursor < 0) cursor <- nchar(code)
    before <- substr(code, 1L, cursor)
    breaks <- gregexpr("\n", before, fixed = TRUE)[[1]]
    line_start <- if (breaks[1] == -1L) 0L else max(breaks)
    line <- substr(before, line_start + 1L, nchar(before))
    utils:::.assignLinebuffer(line)
    utils:::.assignEnd(nchar(line))
    utils:::.guessTokenFromLine()
    utils:::.completeToken()
    found <- utils::head(unique(utils:::.retrieveCompletions()), 100)
    start <- line_start + utils:::.CompletionEnv[["start"]]
    matches <- lapply(found, function(m) list(label = m, kind = completion_kind(m)))
    text <- if (length(found) == 0L) "No completions." else
      paste(vapply(matches, function(x) sprintf("%-20s %s", gsub(" ", "", x$label), x$kind), ""), collapse = "\n")
    list(text = text, start = start, matches = if (length(matches)) matches else list())
  }

  # ── main loop ──────────────────────────────────────────────

  version <- paste0("R ", R.version$major, ".", R.version$minor)

  handle <- function(req) {
    op <- req$op
    use_project_library()
    switch(op,
      ping = list(ok = TRUE),
      run = run_cell(if (is.null(req$code)) "" else req$code),
      look_overview = list(text = look_overview()),
      look_at = list(text = look_at(req$at, isTRUE(req$full))),
      complete = complete(if (is.null(req$code)) "" else req$code, req$cursor),
      status = list(text = paste0("idle\nruntime_version: ", version)),
      list(error = paste0("unknown op: ", op)))
  }

  repeat {
    state <- new.env()
    state$replied <- FALSE
    state$op <- NULL
    state$id <- NULL
    outcome <- tryCatch({
      line <- read_line()
      if (is.null(line)) "quit" else {
        req <- tryCatch(fromJSON(line, simplifyVector = TRUE), error = function(e) NULL)
        if (!is.list(req) || is.null(req$op)) {
          "skip"
        } else if (req$op == "shutdown") {
          "quit"
        } else if (req$op == "input") {
          "skip" # an answer that came after its prompt ended
        } else {
          state$op <- req$op
          state$id <- req$id
          reply <- tryCatch(handle(req), error = function(e) list(success = FALSE, error = conditionMessage(e)))
          reply$id <- req$id
          suspendInterrupts(send(reply))
          state$replied <- TRUE
          "ok"
        }
      }
    }, interrupt = function(e) "interrupted")
    if (identical(outcome, "quit")) break
    if (!is.null(state$op) && !state$replied) {
      # A cancel reached the kernel's own code: the request still gets
      # its one reply.
      reply <- if (state$op == "run") list(success = FALSE, output = "", error = "Interrupted") else list(error = "Interrupted")
      reply$id <- state$id
      suspendInterrupts(send(reply))
    }
  }
  close(con)
}, envir = new.env(parent = baseenv()))
