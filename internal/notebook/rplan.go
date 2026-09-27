package notebook

// R packages a notebook declares (rat.r.dependencies).
//
// Where they go: a project that uses renv keeps its renv library, and rat
// installs with renv::install. Any other project gets a library of its
// own, <project>/.rat/r-library/<platform>/R-<major.minor>, which the R
// kernel puts first on .libPaths() — packages installed elsewhere (the
// site library, Nix, the user library) stay visible, and a declared
// package already found there counts as satisfied. The packages are
// installed with pak, which rat keeps in its own cache, not in the
// project.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/maximerivest/rat/internal/cachedir"
	resolver "github.com/maximerivest/rat/internal/resolve"
	"github.com/maximerivest/rat/internal/state"
)

// RKernelRequirements are what rat's R kernel itself needs.
var RKernelRequirements = []string{"jsonlite"}

// RState describes the R library the notebook resolves to.
type RState struct {
	Kernel        string   `json:"kernel"`
	KernelRunning bool     `json:"kernel_running"`
	Rscript       string   `json:"rscript,omitempty"`
	Version       string   `json:"version,omitempty"`
	Renv          bool     `json:"renv"`    // the project's renv library is used
	Library       string   `json:"library"` // where packages are installed
	Requirements  []string `json:"requirements"`
	Missing       []string `json:"missing,omitempty"`

	cwd      string
	upgrades bool // a missing package is installed at another version (a running kernel may have it loaded)
}

// RRef is one entry of rat.r.dependencies: a pak package reference.
type RRef struct {
	Ref     string // as written
	Package string // the package it installs ("" for local:: until its DESCRIPTION is read)
	// Version: "" (any), "1.2.3" (exactly), ">=1.2" (at least).
	Version string
	// Remote: installed from somewhere other than a package repository (a
	// Git host, a path, a URL). Only rat's receipt proves it was.
	Remote bool
	Local  string // local:: path as written
}

var (
	rNameRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9.]*$`)
	rVersionRe = regexp.MustCompile(`^(>=)?[0-9]+([.-][0-9]+)*$`)
	rSourceRe  = regexp.MustCompile(`^([a-z]+)::(.*)$`)
	rNamedRe   = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9.]*)=(.+)$`)
)

// ParseRRef reads one pak package reference. It knows the sources a
// notebook needs — CRAN and Bioconductor names, GitHub/GitLab, Git, a URL,
// a local path — and says what package each installs.
func ParseRRef(ref string) (RRef, error) {
	ref = strings.TrimSpace(ref)
	out := RRef{Ref: ref}
	if ref == "" {
		return out, fmt.Errorf("empty package reference")
	}
	if strings.ContainsAny(ref, " \t\n\r\x00;|&$`\"'") {
		return out, fmt.Errorf("%q: a package reference has no spaces or shell characters", ref)
	}
	body := ref
	if m := rNamedRe.FindStringSubmatch(body); m != nil && !strings.Contains(m[1], "/") {
		out.Package, body = m[1], m[2]
	}
	source := ""
	if m := rSourceRe.FindStringSubmatch(body); m != nil {
		source, body = m[1], m[2]
	} else if strings.Contains(body, "/") {
		source = "github"
	}
	switch source {
	case "", "cran", "bioc", "any", "standard":
		name, version, _ := strings.Cut(body, "@")
		if !rNameRe.MatchString(name) {
			return out, fmt.Errorf("%q: not an R package name", ref)
		}
		if version != "" && version != "current" && version != "last" {
			if !rVersionRe.MatchString(version) {
				return out, fmt.Errorf("%q: version must be like @1.2.3 or @>=1.2", ref)
			}
			out.Version = version
		}
		if out.Package == "" {
			out.Package = name
		}
	case "github", "gitlab":
		repo := body
		if i := strings.IndexAny(repo, "@#"); i >= 0 {
			repo = repo[:i]
		}
		parts := strings.Split(strings.Trim(repo, "/"), "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return out, fmt.Errorf("%q: write a Git host reference as owner/repo[@ref]", ref)
		}
		out.Remote = true
		if out.Package == "" {
			out.Package = parts[len(parts)-1] // the repo, or its subdirectory
		}
	case "local":
		if body == "" {
			return out, fmt.Errorf("%q: local:: needs a path", ref)
		}
		out.Remote = true
		out.Local = body
	case "git", "url":
		out.Remote = true
		if out.Package == "" {
			return out, fmt.Errorf("%q: name the package it installs: <package>=%s", ref, ref)
		}
	default:
		return out, fmt.Errorf("%q: unknown package source %s::", ref, source)
	}
	return out, nil
}

// rProjectLibrary is the library rat gives a project, per R platform and
// minor version (packages built for one R x.y do not load in another).
// kernel.R computes the same path.
func rProjectLibrary(cwd, platform, version string) string {
	parts := strings.SplitN(version, ".", 3)
	xy := version
	if len(parts) >= 2 {
		xy = parts[0] + "." + parts[1]
	}
	return filepath.Join(cwd, ".rat", "r-library", platform, "R-"+xy)
}

// rInspectScript runs where the kernel runs (the project, so renv
// activates through .Rprofile) and reports R and the declared packages.
const rInspectScript = `v <- R.version
cat("version\t", as.character(getRversion()), "\n", sep = "")
cat("platform\t", v$platform, "\n", sep = "")
renv <- nzchar(Sys.getenv("RENV_PROJECT"))
cat("renv\t", renv, "\n", sep = "")
lib <- file.path(getwd(), ".rat", "r-library", v$platform, paste0("R-", v$major, ".", sub("[.].*", "", v$minor)))
if (!renv && dir.exists(lib)) .libPaths(c(lib, .libPaths()))
cat("library\t", if (renv) .libPaths()[1] else lib, "\n", sep = "")
for (p in commandArgs(TRUE)) {
  ver <- tryCatch(as.character(utils::packageVersion(p)), error = function(e) "")
  cat("pkg\t", p, "\t", ver, "\n", sep = "")
}
`

// rInstallScript installs refs: with renv in an renv project, otherwise
// with pak into the project library (pak itself lives in rat's cache).
const rInstallScript = `a <- commandArgs(TRUE)
mode <- a[1]; lib <- a[2]; tools <- a[3]; refs <- a[-(1:3)]
if (mode == "renv") {
  renv::install(refs, prompt = FALSE)
} else {
  dir.create(lib, recursive = TRUE, showWarnings = FALSE)
  dir.create(tools, recursive = TRUE, showWarnings = FALSE)
  if (!requireNamespace("pak", lib.loc = tools, quietly = TRUE)) {
    install.packages("pak", lib = tools, repos = sprintf("https://r-lib.github.io/p/pak/stable/%s/%s/%s",
      .Platform$pkgType, R.Version()$os, R.Version()$arch))
  }
  # pak works in a subprocess that inherits the library path: it finds
  # pak in rat's tools library, after the project's.
  .libPaths(c(lib, tools, .libPaths()))
  pak::pkg_install(refs, lib = lib, upgrade = FALSE, ask = FALSE)
}
`

func doctorR(store *state.Store, nb *Notebook, r *Report, opts *Options) error {
	rs := &RState{Requirements: []string{}}
	r.R = rs
	rscript, err := opts.lookPath("Rscript")
	if err != nil {
		return nil // the tool check already says R is missing
	}
	rs.Rscript = rscript

	res, err := resolver.ResolveWith(store, "r", resolver.Options{Cwd: nb.Dir, ProjectRoot: r.Project})
	if err != nil {
		return err
	}
	rs.Kernel = res.Name
	rs.cwd = res.Cwd
	if rs.cwd == "" {
		rs.cwd = r.Project
	}
	if k, _ := store.GetRunning(res.Name); k != nil {
		rs.KernelRunning = true
	}

	var refs []RRef
	if nb.R != nil {
		for _, d := range nb.R.Dependencies {
			ref, err := ParseRRef(d)
			if err != nil {
				return err // validated at load
			}
			if ref.Local != "" {
				dir := ref.Local
				if !filepath.IsAbs(dir) {
					dir = filepath.Join(r.Project, dir)
				}
				ref.Package, _ = rDescription(dir)
				if ref.Package == "" {
					r.Checks = append(r.Checks, Check{ID: "r-packages", Label: "R packages",
						Detail: fmt.Sprintf("%s: no R package (DESCRIPTION) in %s", ref.Ref, dir),
						Hint:   "fix the local:: path in the notebook's front matter"})
					r.Blocked = true
					return nil
				}
			}
			refs = append(refs, ref)
		}
	}
	for _, need := range RKernelRequirements {
		declared := false
		for _, ref := range refs {
			declared = declared || ref.Package == need
		}
		if !declared {
			ref, _ := ParseRRef(need)
			refs = append(refs, ref)
		}
	}
	for _, ref := range refs {
		rs.Requirements = append(rs.Requirements, ref.Ref)
	}

	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Package)
	}
	out, err := runCommand(append([]string{rscript, "--no-save", "--no-restore", "-e", rInspectScript}, names...), rs.cwd, 2*time.Minute)
	if err != nil {
		r.Checks = append(r.Checks, Check{ID: "r-packages", Label: "R packages",
			Detail: "could not inspect R: " + lastLines(out+"\n"+err.Error(), 5),
			Hint:   "check that `Rscript` runs in " + rs.cwd})
		r.Blocked = true
		return nil
	}
	info := map[string]string{}
	installed := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		switch {
		case len(f) == 3 && f[0] == "pkg":
			installed[f[1]] = f[2]
		case len(f) == 2:
			info[f[0]] = f[1]
		}
	}
	rs.Version = info["version"]
	rs.Renv = info["renv"] == "TRUE"
	rs.Library = info["library"]
	if rs.Library == "" {
		rs.Library = rProjectLibrary(rs.cwd, info["platform"], rs.Version)
	}

	rec := readRReceipt(rs.Library, rs.Version)
	for _, ref := range refs {
		have := installed[ref.Package]
		ok := have != "" && rVersionOK(have, ref.Version)
		if ok && ref.Remote {
			_, ok = rec.Satisfied[ref.Ref]
		}
		if ok && ref.Local != "" {
			dir := ref.Local
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(r.Project, dir)
			}
			_, srcVersion := rDescription(dir)
			ok = srcVersion == have
		}
		if opts.Force {
			ok = false
		}
		if !ok {
			rs.Missing = append(rs.Missing, ref.Ref)
			if have != "" {
				rs.upgrades = true
			}
		}
	}

	where := rs.Library
	if rs.Renv {
		where += " (renv)"
	}
	if len(rs.Missing) == 0 {
		r.Checks = append(r.Checks, Check{ID: "r-packages", Label: "R packages", OK: true,
			Detail: fmt.Sprintf("%d satisfied · R %s · %s", len(rs.Requirements), rs.Version, where)})
		return nil
	}
	r.Checks = append(r.Checks, Check{ID: "r-packages", Label: "R packages",
		Detail: fmt.Sprintf("%d to install into %s: %s", len(rs.Missing), where, strings.Join(rs.Missing, ", "))})
	effect := ""
	if rs.upgrades && rs.KernelRunning {
		effect = "the R kernel restarts — a package it may have loaded changes version; variables reset"
	}
	r.Actions = append(r.Actions, Action{ID: "r-install", Label: "install R packages",
		Command: rInstallSummary(rs), Dir: rs.cwd,
		Reason: "declared by the notebook (and rat's R kernel needs)",
		Effect: effect})
	return nil
}

// rInstallSummary is the install as a person would type it (the action
// runs rInstallScript, which also fetches pak when needed).
func rInstallSummary(rs *RState) []string {
	quoted := make([]string, len(rs.Missing))
	for i, m := range rs.Missing {
		quoted[i] = strconv.Quote(m)
	}
	refs := "c(" + strings.Join(quoted, ", ") + ")"
	if rs.Renv {
		return []string{"Rscript", "-e", "renv::install(" + refs + ")"}
	}
	return []string{"Rscript", "-e", "pak::pkg_install(" + refs + ", lib = " + strconv.Quote(rs.Library) + ")"}
}

func rInstallCommand(rs *RState, tools string) []string {
	mode := "pak"
	if rs.Renv {
		mode = "renv"
	}
	return append([]string{rs.Rscript, "--no-save", "--no-restore", "-e", rInstallScript, mode, rs.Library, tools}, rs.Missing...)
}

// ensureR carries out the r-install action. It reports whether the R
// kernel must restart.
func ensureR(rs *RState) (string, bool, error) {
	tools := ""
	if !rs.Renv {
		dir, err := cachedir.Rat()
		if err != nil {
			return "", false, err
		}
		tools = filepath.Join(dir, "r-tools", filepath.Base(filepath.Dir(rs.Library)), filepath.Base(rs.Library))
		// The project library is rat's, not the project's source: keep
		// it out of Git, as uv does for .venv.
		ratDir := filepath.Join(rs.cwd, ".rat")
		if err := os.MkdirAll(ratDir, 0o755); err != nil {
			return "", false, err
		}
		ignore := filepath.Join(ratDir, ".gitignore")
		if _, err := os.Stat(ignore); os.IsNotExist(err) {
			_ = os.WriteFile(ignore, []byte("# Created by rat: packages and state for this project's notebooks.\n*\n"), 0o644)
		}
	}
	out, err := runCommand(rInstallCommand(rs, tools), rs.cwd, 60*time.Minute)
	if err != nil {
		return out, false, err
	}
	if err := recordRReceipt(rs.Library, rs.Version, rs.Missing); err != nil {
		out += "\n(could not write receipt: " + err.Error() + ")"
	}
	return out, rs.upgrades && rs.KernelRunning, nil
}

// rDescription reads Package and Version from dir/DESCRIPTION.
func rDescription(dir string) (pkg, version string) {
	data, err := os.ReadFile(filepath.Join(dir, "DESCRIPTION"))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "Package:"); ok {
			pkg = strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(line, "Version:"); ok {
			version = strings.TrimSpace(v)
		}
	}
	return pkg, version
}

// rVersionOK compares R package versions (numbers separated by . or -).
func rVersionOK(have, want string) bool {
	if want == "" {
		return true
	}
	atLeast := strings.HasPrefix(want, ">=")
	cmp := compareRVersions(have, strings.TrimPrefix(want, ">="))
	if atLeast {
		return cmp >= 0
	}
	return cmp == 0
}

func compareRVersions(a, b string) int {
	split := func(s string) []int {
		var out []int
		for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' }) {
			n, _ := strconv.Atoi(p)
			out = append(out, n)
		}
		return out
	}
	x, y := split(a), split(b)
	for i := 0; i < len(x) || i < len(y); i++ {
		var xi, yi int
		if i < len(x) {
			xi = x[i]
		}
		if i < len(y) {
			yi = y[i]
		}
		if xi != yi {
			if xi < yi {
				return -1
			}
			return 1
		}
	}
	return 0
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ── receipt: refs rat installed, per library and R version ──

const rReceiptFile = ".rat-receipt.json"

type rReceipt struct {
	R         string            `json:"r"`
	Satisfied map[string]string `json:"satisfied"` // ref → RFC 3339 time
}

func readRReceipt(library, version string) rReceipt {
	r := rReceipt{R: version, Satisfied: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(library, rReceiptFile))
	if err != nil {
		return r
	}
	var on rReceipt
	if json.Unmarshal(data, &on) != nil || on.R != version || on.Satisfied == nil {
		return r
	}
	r.Satisfied = on.Satisfied
	return r
}

func recordRReceipt(library, version string, refs []string) error {
	r := readRReceipt(library, version)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, ref := range refs {
		r.Satisfied[strings.TrimSpace(ref)] = now
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(library, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(library, rReceiptFile), append(data, '\n'), 0o644)
}
