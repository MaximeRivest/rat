# rat's package manager for R notebooks: rat.r.dependencies.
#
#   Rscript packages.R check|install|lock <project> [--force] [--update] -- <ref>...
#
# The contract with rat (internal/notebook/runtimeenv.go): check prints
# one fact per line, tab-separated; install and lock print their log.
# Base R only — this must run where jsonlite is not installed yet.
#
# References are pak's (https://pak.r-lib.org/reference/pak_package_sources.html):
# dplyr, ggplot2@3.5.1, jsonlite@>=1.8, owner/repo@ref, bioc::DESeq2,
# local::., name=url::https://….
#
# Where packages go: a project that uses renv keeps its renv library
# (renv::install). Any other project gets its own library,
# .rat/r-library/<platform>/R-<x.y>, first on the kernel's .libPaths();
# packages installed elsewhere (site library, Nix, user library) stay
# visible, and a declared package found there will do.
#
# The lock, .rat/r.lock (DCF, committed with the project), records the
# version of every declared package and of everything it depends on, and
# how to get exactly that version again. With a lock, check wants those
# versions and install reproduces them; --update resolves again.

args <- commandArgs(TRUE)
verb <- args[1]
project <- normalizePath(args[2], mustWork = FALSE)
sep <- match("--", args)
flags <- if (!is.na(sep) && sep > 3) args[3:(sep - 1)] else character()
declared <- if (!is.na(sep) && sep < length(args)) args[(sep + 1):length(args)] else character()
force <- "--force" %in% flags
update <- "--update" %in% flags

emit <- function(...) cat(paste(..., sep = "\t"), "\n", sep = "")

root <- getwd()
v <- R.version
xy <- paste0(v$major, ".", sub("[.].*", "", v$minor))
renv_mode <- nzchar(Sys.getenv("RENV_PROJECT"))
project_library <- file.path(root, ".rat", "r-library", v$platform, paste0("R-", xy))
library_dir <- if (renv_mode) .libPaths()[1] else project_library
if (!renv_mode && dir.exists(project_library)) .libPaths(c(project_library, .libPaths()))
lock_path <- file.path(root, ".rat", "r.lock")

# The kernel's own need.
if (!any(grepl("^(jsonlite)([@=]|$)", declared))) declared <- c(declared, "jsonlite")
declared <- unique(trimws(declared))

# ── references ─────────────────────────────────────────────

parse_ref <- function(ref) {
  r <- list(ref = ref, package = NA_character_, version = "", remote = FALSE, local = NA_character_, error = NA_character_)
  fail <- function(msg) { r$error <- msg; r }
  if (grepl("[[:space:];|&$`\"']", ref)) return(fail("a package reference has no spaces or shell characters"))
  body <- ref
  m <- regmatches(body, regexec("^([A-Za-z][A-Za-z0-9.]*)=(.+)$", body))[[1]]
  if (length(m) && !grepl("/", m[2])) { r$package <- m[2]; body <- m[3] }
  source <- ""
  m <- regmatches(body, regexec("^([a-z]+)::(.*)$", body))[[1]]
  if (length(m)) { source <- m[2]; body <- m[3] } else if (grepl("/", body)) source <- "github"
  if (source %in% c("", "cran", "bioc", "any", "standard")) {
    parts <- strsplit(body, "@", fixed = TRUE)[[1]]
    name <- parts[1]
    if (!grepl("^[A-Za-z][A-Za-z0-9.]*$", name)) return(fail("not an R package name"))
    ver <- if (length(parts) > 1) parts[2] else ""
    if (nzchar(ver) && !(ver %in% c("current", "last"))) {
      if (!grepl("^(>=)?[0-9]+([.-][0-9]+)*$", ver)) return(fail("version must be like @1.2.3 or @>=1.2"))
      r$version <- ver
    }
    if (is.na(r$package)) r$package <- name
  } else if (source %in% c("github", "gitlab")) {
    repo <- sub("[@#].*$", "", body)
    parts <- strsplit(gsub("^/+|/+$", "", repo), "/")[[1]]
    if (length(parts) < 2 || !all(nzchar(parts[1:2]))) return(fail("write a Git host reference as owner/repo[@ref]"))
    r$remote <- TRUE
    if (is.na(r$package)) r$package <- parts[length(parts)]
  } else if (source == "local") {
    if (!nzchar(body)) return(fail("local:: needs a path"))
    dir <- if (grepl("^(/|~)", body)) path.expand(body) else file.path(project, body)
    desc <- file.path(dir, "DESCRIPTION")
    if (!file.exists(desc)) return(fail(paste0("no R package (DESCRIPTION) in ", dir)))
    d <- read.dcf(desc, fields = c("Package", "Version"))
    r$remote <- TRUE
    r$local <- dir
    r$package <- d[1, "Package"]
    r$version <- d[1, "Version"]
  } else if (source %in% c("git", "url")) {
    r$remote <- TRUE
    if (is.na(r$package)) return(fail(paste0("name the package it installs: <package>=", ref)))
  } else {
    return(fail(paste0("unknown package source ", source, "::")))
  }
  r
}

installed_version <- function(pkg) tryCatch(as.character(utils::packageVersion(pkg)), error = function(e) "")

version_ok <- function(have, want) {
  if (!nzchar(want)) return(TRUE)
  if (startsWith(want, ">=")) return(utils::compareVersion(have, substring(want, 3)) >= 0)
  utils::compareVersion(have, want) == 0
}

read_lock <- function() {
  if (renv_mode || update || !file.exists(lock_path)) return(NULL)
  lock <- tryCatch(read.dcf(lock_path, fields = c("Package", "Version", "Ref", "Declared")), error = function(e) NULL)
  if (is.null(lock) || !nrow(lock)) return(NULL)
  lock[is.na(lock)] <- ""
  lock
}

# ── the state: what is declared, locked, installed, missing ──

inspect <- function() {
  refs <- lapply(declared, parse_ref)
  problems <- Filter(function(r) !is.na(r$error), refs)
  refs <- Filter(function(r) is.na(r$error), refs)
  lock <- read_lock()
  missing <- character()   # refs to install
  upgrades <- FALSE
  want_missing <- function(line, pkg) {
    missing <<- c(missing, line)
    if (nzchar(installed_version(pkg))) upgrades <<- TRUE
  }
  locked_row <- function(pkg) if (is.null(lock)) NULL else { i <- which(lock[, "Package"] == pkg); if (length(i)) lock[i[1], ] else NULL }
  for (r in refs) {
    have <- installed_version(r$package)
    row <- locked_row(r$package)
    if (!is.null(row) && row[["Declared"]] == r$ref) {
      ok <- have == row[["Version"]]
      if (ok && r$remote && is.na(r$local)) {
        sha <- sub("^.*@", "", row[["Ref"]])
        got <- tryCatch(utils::packageDescription(r$package)$RemoteSha, error = function(e) NULL)
        ok <- is.null(got) || identical(got, sha) || !grepl("@", row[["Ref"]])
      }
      if (force || !ok) want_missing(row[["Ref"]], r$package)
      next
    }
    ok <- nzchar(have) && version_ok(have, r$version)
    if (ok && r$remote && is.na(r$local)) {
      desc <- tryCatch(utils::packageDescription(r$package), error = function(e) NULL)
      ok <- !is.null(desc) && !is.null(desc$RemoteType)
    }
    if (ok && !is.na(r$local)) ok <- have == r$version
    if (force || !ok) want_missing(r$ref, r$package)
  }
  # What the declared packages depend on, as locked.
  if (!is.null(lock)) {
    declared_pkgs <- vapply(refs, function(r) r$package, "")
    for (i in seq_len(nrow(lock))) {
      pkg <- lock[i, "Package"]
      if (pkg %in% declared_pkgs) next
      if (force || installed_version(pkg) != lock[i, "Version"]) want_missing(lock[i, "Ref"], pkg)
    }
  }
  locked_decl <- if (is.null(lock)) character() else lock[nzchar(lock[, "Declared"]), "Declared"]
  relock <- !renv_mode && (is.null(lock) || !setequal(locked_decl, vapply(refs, function(r) r$ref, "")))
  list(refs = refs, problems = problems, lock = lock, missing = unique(missing), upgrades = upgrades, relock = relock)
}

# ── the lock ───────────────────────────────────────────────

base_packages <- rownames(utils::installed.packages(priority = "base"))

ref_for <- function(pkg, declared_ref) {
  d <- tryCatch(utils::packageDescription(pkg), error = function(e) NULL)
  if (is.null(d)) return("")
  type <- if (is.null(d$RemoteType)) "" else d$RemoteType
  if (type %in% c("github", "gitlab") && !is.null(d$RemoteSha)) {
    sub <- if (!is.null(d$RemoteSubdir) && nzchar(d$RemoteSubdir)) paste0("/", d$RemoteSubdir) else ""
    return(paste0(if (type == "gitlab") "gitlab::" else "", d$RemoteUsername, "/", d$RemoteRepo, sub, "@", d$RemoteSha))
  }
  if (nzchar(declared_ref) && (type %in% c("local", "url", "git") || grepl("^(local|url|git)::|=", declared_ref))) return(declared_ref)
  if (!is.null(d$biocViews) && nzchar(d$biocViews) && (is.null(d$Repository) || !identical(d$Repository, "CRAN"))) return(paste0("bioc::", pkg))
  paste0(pkg, "@", d$Version)
}

write_lock <- function(refs) {
  if (renv_mode) return(invisible())
  ip <- utils::installed.packages()
  ip <- ip[!duplicated(ip[, "Package"]), , drop = FALSE]
  top <- vapply(refs, function(r) r$package, "")
  deps <- tools::package_dependencies(top, db = ip, which = c("Depends", "Imports", "LinkingTo"), recursive = TRUE)
  pkgs <- unique(c(top, unlist(deps, use.names = FALSE)))
  pkgs <- sort(setdiff(pkgs[pkgs %in% rownames(ip)], c("R", base_packages)))
  declared_of <- function(p) { i <- match(p, top); if (is.na(i)) "" else refs[[i]]$ref }
  rows <- lapply(pkgs, function(p) c(Package = p, Version = ip[p, "Version"], Ref = ref_for(p, declared_of(p)), Declared = declared_of(p)))
  dir.create(dirname(lock_path), recursive = TRUE, showWarnings = FALSE)
  tmp <- paste0(lock_path, ".tmp")
  write.dcf(do.call(rbind, rows), tmp, keep.white = "Declared")
  file.rename(tmp, lock_path)
  cat("wrote ", lock_path, " (", length(pkgs), " packages, R ", xy, ")\n", sep = "")
}

# ── commands ───────────────────────────────────────────────

state <- inspect()

if (verb == "check") {
  emit("version", as.character(getRversion()))
  emit("environment", library_dir)
  if (!renv_mode) emit("lock", lock_path)
  emit("info", "library", library_dir)
  emit("info", "renv", if (renv_mode) "true" else "false")
  for (r in declared) emit("requirement", r)
  for (p in state$problems) emit("problem", p$ref, p$error)
  for (m in state$missing) emit("missing", m)
  if (state$upgrades) emit("restart", "true")
  if (state$relock && !length(state$missing)) emit("relock", "true")
  where <- if (renv_mode) paste0(library_dir, " (renv)") else library_dir
  locked <- if (!is.null(state$lock)) paste0(" · locked (", basename(lock_path), ")") else ""
  if (length(state$missing)) {
    emit("detail", paste0(length(state$missing), " to install into ", where, ": ", paste(state$missing, collapse = ", ")))
    q <- paste0('"', state$missing, '"', collapse = ", ")
    emit("summary", if (renv_mode) paste0("Rscript -e 'renv::install(c(", q, "))'")
                    else paste0("Rscript -e 'pak::pkg_install(c(", q, "), lib = \"", library_dir, "\")'"))
  } else {
    emit("detail", paste0(length(declared), " satisfied · R ", as.character(getRversion()), " · ", where, locked))
  }
  if (!length(state$missing) && renv_mode) emit("hint", "renv records versions: renv::snapshot()")
  quit(status = 0)
}

if (length(state$problems)) {
  for (p in state$problems) message(p$ref, ": ", p$error)
  quit(status = 2)
}

if (verb == "install" && length(state$missing)) {
  targets <- state$missing
  if (renv_mode) {
    renv::install(targets, prompt = FALSE)
  } else {
    tools_dir <- file.path(Sys.getenv("XDG_CACHE_HOME", file.path(path.expand("~"), ".cache")), "rat", "r-tools", v$platform, paste0("R-", xy))
    dir.create(library_dir, recursive = TRUE, showWarnings = FALSE)
    dir.create(tools_dir, recursive = TRUE, showWarnings = FALSE)
    if (!requireNamespace("pak", lib.loc = tools_dir, quietly = TRUE)) {
      install.packages("pak", lib = tools_dir, repos = sprintf("https://r-lib.github.io/p/pak/stable/%s/%s/%s",
        .Platform$pkgType, R.Version()$os, R.Version()$arch))
    }
    # pak works in a subprocess that inherits the library path: it finds
    # pak in rat's tools library, after the project's.
    .libPaths(c(library_dir, tools_dir, .libPaths()))
    pak::pkg_install(targets, lib = library_dir, upgrade = FALSE, ask = FALSE)
  }
  state <- inspect()
  if (length(state$missing)) {
    message("still missing after the install: ", paste(state$missing, collapse = ", "))
    quit(status = 1)
  }
}

if (verb %in% c("install", "lock")) write_lock(state$refs)
