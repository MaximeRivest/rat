package notebook

// Julia packages a notebook declares (rat.julia.dependencies).
//
// They go into <project>/.rat/julia, a Julia environment of rat's: a
// project that is itself a Julia package keeps its Project.toml as its
// authors wrote it. The Julia kernel activates the project's own
// environment when it has one (so `using TheProject` works) and stacks
// .rat/julia on LOAD_PATH — a standard Julia stacked environment — so the
// notebook's packages load too, and so do those of the default
// environment (~/.julia/environments/v1.x). A declared package found in
// any of them counts as installed; that is read from their manifests,
// without starting Julia.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	resolver "github.com/maximerivest/rat/internal/resolve"
	"github.com/maximerivest/rat/internal/state"
)

// JuliaSpec declares the Julia packages a notebook needs.
type JuliaSpec struct {
	Dependencies []string `yaml:"dependencies"`
}

// JuliaState describes the Julia environment the notebook resolves to.
type JuliaState struct {
	Kernel        string   `json:"kernel"`
	KernelRunning bool     `json:"kernel_running"`
	Julia         string   `json:"julia,omitempty"`
	Version       string   `json:"version,omitempty"`
	Environment   string   `json:"environment"`                   // where rat installs: <project>/.rat/julia
	Project       string   `json:"project_environment,omitempty"` // the project's own Project.toml, when it has one
	Requirements  []string `json:"requirements"`
	Missing       []string `json:"missing,omitempty"`

	cwd      string
	upgrades bool
	refs     []JuliaRef
}

// JuliaRef is one entry of rat.julia.dependencies.
type JuliaRef struct {
	Ref     string // as written
	Package string
	Version string // "", "1.6" (any 1.6.x), "1.6.1"
	URL     string // a Git repository (optionally #rev)
	Path    string // a local package, developed in place
}

var (
	jlNameRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	jlVersionRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)
	jlNamedRe   = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.+)$`)
)

// ParseJuliaRef reads one entry: a registered package (DataFrames,
// Plots@1.40), a Git URL (https://github.com/org/Foo.jl#main, optionally
// Foo=<url> to name it), or a local path (./MyPkg, developed in place).
func ParseJuliaRef(ref string) (JuliaRef, error) {
	ref = strings.TrimSpace(ref)
	out := JuliaRef{Ref: ref}
	if ref == "" {
		return out, fmt.Errorf("empty package reference")
	}
	if strings.ContainsAny(ref, " \t\n\r\x00;|&$`\"'") {
		return out, fmt.Errorf("%q: a package reference has no spaces or shell characters", ref)
	}
	body := ref
	if m := jlNamedRe.FindStringSubmatch(body); m != nil {
		out.Package, body = m[1], m[2]
	}
	switch {
	case body == "." || strings.HasPrefix(body, "./") || strings.HasPrefix(body, "../") || strings.HasPrefix(body, "/"):
		out.Path = body
	case strings.Contains(body, "://") || strings.HasPrefix(body, "git@"):
		out.URL = body
		if out.Package == "" {
			repo, _, _ := strings.Cut(body, "#")
			repo = strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(repo, "/"), ".git"), ".jl")
			out.Package = repo[strings.LastIndexAny(repo, "/:")+1:]
			if !jlNameRe.MatchString(out.Package) {
				return out, fmt.Errorf("%q: name the package it installs: <Package>=%s", ref, ref)
			}
		}
	default:
		name, version, _ := strings.Cut(body, "@")
		if !jlNameRe.MatchString(name) {
			return out, fmt.Errorf("%q: not a Julia package name", ref)
		}
		if version != "" && !jlVersionRe.MatchString(version) {
			return out, fmt.Errorf("%q: version must be like @1.6 or @1.6.1", ref)
		}
		if out.Package == "" {
			out.Package = name
		}
		out.Version = version
	}
	return out, nil
}

func mergeJulia(a, b *JuliaSpec) *JuliaSpec {
	if a == nil && b == nil {
		return nil
	}
	out := &JuliaSpec{}
	for _, spec := range []*JuliaSpec{a, b} {
		if spec == nil {
			continue
		}
		for _, d := range spec.Dependencies {
			if d = strings.TrimSpace(d); !containsString(out.Dependencies, d) {
				out.Dependencies = append(out.Dependencies, d)
			}
		}
	}
	return out
}

// juliaProjectFile returns the Project.toml of a directory, if any.
func juliaProjectFile(dir string) string {
	for _, f := range []string{"JuliaProject.toml", "Project.toml"} {
		if p := filepath.Join(dir, f); fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// juliaManifest reads the packages (name → version) of the manifest next
// to a project file. Manifest format 2 (Julia ≥ 1.7) and the older one.
func juliaManifest(projectFile string) map[string]string {
	out := map[string]string{}
	if projectFile == "" {
		return out
	}
	dir := filepath.Dir(projectFile)
	var data []byte
	for _, f := range []string{"JuliaManifest.toml", "Manifest.toml"} {
		if d, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
			data = d
			break
		}
	}
	if data == nil {
		return out
	}
	type entry struct {
		Version string `toml:"version"`
	}
	var v2 struct {
		Deps map[string][]entry `toml:"deps"`
	}
	if _, err := toml.Decode(string(data), &v2); err == nil && v2.Deps != nil {
		for name, es := range v2.Deps {
			if len(es) > 0 {
				out[name] = es[0].Version
			}
		}
		return out
	}
	var v1 map[string][]entry
	if _, err := toml.Decode(string(data), &v1); err == nil {
		for name, es := range v1 {
			if len(es) > 0 {
				out[name] = es[0].Version
			}
		}
	}
	return out
}

// juliaProjectName reads `name` from a directory's Project.toml.
func juliaProjectName(dir string) string {
	var p struct {
		Name string `toml:"name"`
	}
	if f := juliaProjectFile(dir); f != "" {
		_, _ = toml.DecodeFile(f, &p)
	}
	return p.Name
}

// juliaVersionOK: "1.6" accepts any 1.6.x, "1.6.1" only itself.
func juliaVersionOK(have, want string) bool {
	if want == "" {
		return true
	}
	return have == want || strings.HasPrefix(have, want+".")
}

func juliaDepot(opts *Options) string {
	if d := opts.getenv("JULIA_DEPOT_PATH"); d != "" {
		if first := strings.Split(d, string(os.PathListSeparator))[0]; first != "" {
			return first
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".julia")
}

func doctorJulia(store *state.Store, nb *Notebook, r *Report, opts *Options) error {
	js := &JuliaState{Requirements: []string{}}
	r.Julia = js
	julia, err := opts.lookPath("julia")
	if err != nil {
		return nil // the tool check already says Julia is missing
	}
	js.Julia = julia
	res, err := resolver.ResolveWith(store, "jl", resolver.Options{Cwd: nb.Dir, ProjectRoot: r.Project})
	if err != nil {
		return err
	}
	js.Kernel = res.Name
	js.cwd = res.Cwd
	if js.cwd == "" {
		js.cwd = r.Project
	}
	if k, _ := store.GetRunning(res.Name); k != nil {
		js.KernelRunning = true
	}
	js.Environment = filepath.Join(js.cwd, ".rat", "julia")
	js.Project = juliaProjectFile(js.cwd)
	if nb.Julia == nil || len(nb.Julia.Dependencies) == 0 {
		return nil // the kernel itself needs no package
	}

	out, err := runCommand([]string{julia, "--startup-file=no", "-e", "print(VERSION.major, '.', VERSION.minor, '.', VERSION.patch)"}, js.cwd, time.Minute)
	if err != nil {
		r.Checks = append(r.Checks, Check{ID: "julia-packages", Label: "Julia packages",
			Detail: "could not run julia: " + lastLines(out+"\n"+err.Error(), 3)})
		r.Blocked = true
		return nil
	}
	js.Version = strings.TrimSpace(out)
	parts := strings.SplitN(js.Version, ".", 3)
	defaultEnv := ""
	if len(parts) >= 2 {
		defaultEnv = filepath.Join(juliaDepot(opts), "environments", "v"+parts[0]+"."+parts[1], "Project.toml")
	}

	// What loads: the notebook's environment, the project's, the default one.
	visible := map[string]string{}
	for _, pf := range []string{defaultEnv, js.Project, filepath.Join(js.Environment, "Project.toml")} {
		for name, v := range juliaManifest(pf) {
			visible[name] = v
		}
	}
	rec := readJuliaReceipt(js.Environment, js.Version)
	for _, d := range nb.Julia.Dependencies {
		ref, err := ParseJuliaRef(d)
		if err != nil {
			return err // validated at load
		}
		if ref.Path != "" {
			dir := ref.Path
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(r.Project, dir)
			}
			if ref.Package == "" {
				ref.Package = juliaProjectName(dir)
			}
			if ref.Package == "" {
				r.Checks = append(r.Checks, Check{ID: "julia-packages", Label: "Julia packages",
					Detail: fmt.Sprintf("%s: no Julia package (Project.toml with a name) in %s", ref.Ref, dir),
					Hint:   "fix the path in the notebook's front matter"})
				r.Blocked = true
				return nil
			}
			ref.Path = dir
		}
		js.refs = append(js.refs, ref)
		js.Requirements = append(js.Requirements, ref.Ref)
		have, found := visible[ref.Package]
		ok := found && juliaVersionOK(have, ref.Version)
		if ok && (ref.URL != "" || ref.Path != "") {
			_, ok = rec.Satisfied[ref.Ref]
		}
		if opts.Force {
			ok = false
		}
		if !ok {
			js.Missing = append(js.Missing, ref.Ref)
			js.upgrades = js.upgrades || found
		}
	}
	if len(js.Missing) == 0 {
		r.Checks = append(r.Checks, Check{ID: "julia-packages", Label: "Julia packages", OK: true,
			Detail: fmt.Sprintf("%d satisfied · Julia %s · %s", len(js.Requirements), js.Version, js.Environment)})
		return nil
	}
	r.Checks = append(r.Checks, Check{ID: "julia-packages", Label: "Julia packages",
		Detail: fmt.Sprintf("%d to install into %s: %s", len(js.Missing), js.Environment, strings.Join(js.Missing, ", "))})
	effect := ""
	if js.upgrades && js.KernelRunning {
		effect = "the Julia kernel restarts — a package it may have loaded changes version; variables reset"
	}
	r.Actions = append(r.Actions, Action{ID: "julia-install", Label: "install Julia packages",
		Command: []string{"julia", "--project=" + js.Environment, "-e", "using Pkg; Pkg.add(…)"}, Dir: js.cwd,
		Reason: "declared by the notebook", Effect: effect})
	return nil
}

// juliaInstallScript adds registered and Git packages and develops local
// ones, into the active project (--project). Arguments: kind\tname\tspec.
const juliaInstallScript = `using Pkg
adds = Pkg.PackageSpec[]; devs = Pkg.PackageSpec[]
for a in ARGS
    kind, name, spec = split(a, '\t')
    if kind == "path"
        push!(devs, Pkg.PackageSpec(path = String(spec)))
    elseif kind == "url"
        url, rev = occursin('#', spec) ? split(spec, '#', limit = 2) : (spec, "")
        push!(adds, isempty(rev) ? Pkg.PackageSpec(url = String(url)) : Pkg.PackageSpec(url = String(url), rev = String(rev)))
    else
        push!(adds, isempty(spec) ? Pkg.PackageSpec(name = String(name)) : Pkg.PackageSpec(name = String(name), version = String(spec)))
    end
end
isempty(devs) || Pkg.develop(devs)
isempty(adds) || Pkg.add(adds)
Pkg.precompile()
`

func ensureJulia(js *JuliaState) (string, bool, error) {
	if err := os.MkdirAll(js.Environment, 0o755); err != nil {
		return "", false, err
	}
	ignore := filepath.Join(filepath.Dir(js.Environment), ".gitignore")
	if _, err := os.Stat(ignore); os.IsNotExist(err) {
		_ = os.WriteFile(ignore, []byte("# Created by rat: packages and state for this project's notebooks.\n*\n"), 0o644)
	}
	args := []string{js.Julia, "--startup-file=no", "--project=" + js.Environment, "-e", juliaInstallScript}
	for _, ref := range js.refs {
		if !containsString(js.Missing, ref.Ref) {
			continue
		}
		switch {
		case ref.Path != "":
			args = append(args, "path\t"+ref.Package+"\t"+ref.Path)
		case ref.URL != "":
			args = append(args, "url\t"+ref.Package+"\t"+ref.URL)
		default:
			args = append(args, "name\t"+ref.Package+"\t"+ref.Version)
		}
	}
	out, err := runCommand(args, js.cwd, 60*time.Minute)
	if err != nil {
		return out, false, err
	}
	if err := recordJuliaReceipt(js.Environment, js.Version, js.Missing); err != nil {
		out += "\n(could not write receipt: " + err.Error() + ")"
	}
	return out, js.upgrades && js.KernelRunning, nil
}

// ── receipt: Git and path packages rat installed ──

type juliaReceipt struct {
	Julia     string            `json:"julia"`
	Satisfied map[string]string `json:"satisfied"`
}

func readJuliaReceipt(env, version string) juliaReceipt {
	r := juliaReceipt{Julia: version, Satisfied: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(env, rReceiptFile))
	if err != nil {
		return r
	}
	var on juliaReceipt
	if json.Unmarshal(data, &on) != nil || on.Julia != version || on.Satisfied == nil {
		return r
	}
	r.Satisfied = on.Satisfied
	return r
}

func recordJuliaReceipt(env, version string, refs []string) error {
	r := readJuliaReceipt(env, version)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, ref := range refs {
		r.Satisfied[strings.TrimSpace(ref)] = now
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(env, rReceiptFile), append(data, '\n'), 0o644)
}
