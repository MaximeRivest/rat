// Package resolve implements the unified name resolution algorithm for rat.
//
// Every command uses Resolve() to turn user input into a concrete kernel
// identity. The algorithm (from the CLI spec):
//
//  1. Exact match — running kernel or saved runtime with this exact name
//  2. Language alias — compute canonical name for cwd (lang@project),
//     check if it exists (running or saved) → use it, else return as new
//  3. Prefix match — collect all whose name starts with input
//  4. Error — no match
//
// Language aliases are resolved early (step 2), before prefix matching
// (step 3), so that "rat py" from ~/ always works even when py@api and
// py@web are running.
package resolve

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/maximerivest/rat/internal/lang"
	"github.com/maximerivest/rat/internal/project"
	"github.com/maximerivest/rat/internal/runtimeid"
	"github.com/maximerivest/rat/internal/state"
)

// Result is what Resolve returns.
type Result struct {
	Name        string            // resolved kernel name (e.g. "py@myproject", "py-ml")
	Lang        string            // canonical language (e.g. "py", "sh")
	Cwd         string            // working directory for the kernel
	Venv        string            // detected venv path (py only, may be "")
	RuntimePath string            // explicit binary path (from rat add --runtime)
	Options     map[string]string // structured runtime options (from rat add --opt)
	Env         map[string]string // extra env vars (from rat add --env)
	IsNew       bool              // true if no existing kernel/runtime matches
}

// Resolve maps user input + cwd to a concrete kernel identity.
//
// It checks running kernels, saved runtimes, language aliases (with
// project-aware expansion), and prefix matches — in that order.
func Resolve(s *state.Store, input string, cwd string) (*Result, error) {
	// Gather all known names: running/stopped kernels + saved runtimes.
	kernels, err := s.ListKnown()
	if err != nil {
		return nil, err
	}
	runtimes, err := s.ListRuntimes()
	if err != nil {
		return nil, err
	}

	// ── Step 1: Exact match ──────────────────────────────────────

	// Check kernels (running or stopped).
	// If a matching runtime config exists, merge in its extra fields
	// (Env, Options, RuntimePath) that the kernel entry doesn't carry.
	for _, k := range kernels {
		if k.Name == input {
			r := &Result{
				Name: k.Name,
				Lang: k.Lang,
				Cwd:  k.Cwd,
				Venv: k.Venv,
			}
			for _, rt := range runtimes {
				if rt.Name == k.Name {
					r.RuntimePath = rt.RuntimePath
					r.Options = rt.Options
					r.Env = rt.Env
					break
				}
			}
			return r, nil
		}
	}

	// Check saved runtimes (from `rat add`).
	for _, rt := range runtimes {
		if rt.Name == input {
			rtCwd := rt.Cwd
			if rtCwd == "" {
				rtCwd = cwd
			}
			return &Result{
				Name:        rt.Name,
				Lang:        rt.Lang,
				Cwd:         rtCwd,
				Venv:        rt.Venv,
				RuntimePath: rt.RuntimePath,
				Options:     rt.Options,
				Env:         rt.Env,
			}, nil
		}
	}

	// ── Step 1b: Instance shorthand ───────────────────────────────────────────
	// "py.2" → resolve "py", then rename to "<resolved>.2".
	// Runs after exact match so that existing instance kernels
	// (e.g. "py@rat.2" already in state) are found directly.

	if base, n, ok := parseInstance(input); ok {
		result, err := Resolve(s, base, cwd)
		if err != nil {
			return nil, err
		}
		instanceName := fmt.Sprintf("%s.%d", result.Name, n)
		// Check if the instance already exists in state.
		for _, k := range kernels {
			if k.Name == instanceName {
				return &Result{
					Name: k.Name,
					Lang: k.Lang,
					Cwd:  k.Cwd,
					Venv: k.Venv,
				}, nil
			}
		}
		result.Name = instanceName
		result.IsNew = true
		return result, nil
	}

	// ── Step 2: Language alias ────────────────────────────────────

	if lang.IsAlias(input) {
		canonical, _ := lang.Resolve(input)
		return resolveLanguage(canonical, cwd, kernels, runtimes)
	}

	// ── Step 3: Prefix match ─────────────────────────────────────

	var matches []Result

	for _, k := range kernels {
		if strings.HasPrefix(k.Name, input) {
			matches = append(matches, Result{
				Name: k.Name,
				Lang: k.Lang,
				Cwd:  k.Cwd,
				Venv: k.Venv,
			})
		}
	}
	for _, rt := range runtimes {
		if strings.HasPrefix(rt.Name, input) {
			// Don't duplicate if a kernel with this name already matched.
			dup := false
			for _, m := range matches {
				if m.Name == rt.Name {
					dup = true
					break
				}
			}
			if !dup {
				rtCwd := rt.Cwd
				if rtCwd == "" {
					rtCwd = cwd
				}
				matches = append(matches, Result{
					Name:        rt.Name,
					Lang:        rt.Lang,
					Cwd:         rtCwd,
					Venv:        rt.Venv,
					RuntimePath: rt.RuntimePath,
					Options:     rt.Options,
					Env:         rt.Env,
				})
			}
		}
	}

	switch len(matches) {
	case 1:
		return &matches[0], nil
	case 0:
		// Fall through to error
	default:
		// Ambiguous — list the matches.
		var lines []string
		for _, m := range matches {
			lines = append(lines, fmt.Sprintf("  %s", m.Name))
		}
		return nil, fmt.Errorf(
			"multiple runtimes match %q:\n%s\nUse the full name, e.g.: rat run %s '...'",
			input, strings.Join(lines, "\n"), matches[0].Name,
		)
	}

	// ── Step 4: Error ────────────────────────────────────────────

	return nil, fmt.Errorf(
		"no runtime matching %q. Use a language (py, sh, r, jl, js)\nor see 'rat status' for running kernels.",
		input,
	)
}

// resolveLanguage handles step 2: language alias → project-aware naming.
// Computes the canonical name (lang@project), checks if it exists, and
// returns it. If it doesn't exist, returns it as new (caller creates).
func resolveLanguage(
	canonical string,
	cwd string,
	kernels []state.Kernel,
	runtimes []state.Runtime,
) (*Result, error) {
	root, _ := project.FindRoot(cwd)
	projName := runtimeid.SlugPart(project.Name(root))

	// Build the canonical kernel name: lang@project
	canonicalName := computeCanonicalName(canonical, projName, root, kernels, runtimes)

	// Check if it exists already (kernel or saved runtime).
	for _, k := range kernels {
		if k.Name == canonicalName {
			return &Result{
				Name: k.Name,
				Lang: k.Lang,
				Cwd:  k.Cwd,
				Venv: k.Venv,
			}, nil
		}
	}
	for _, rt := range runtimes {
		if rt.Name == canonicalName {
			rtCwd := rt.Cwd
			if rtCwd == "" {
				rtCwd = root
			}
			return &Result{
				Name:        rt.Name,
				Lang:        rt.Lang,
				Cwd:         rtCwd,
				Venv:        rt.Venv,
				RuntimePath: rt.RuntimePath,
				Options:     rt.Options,
				Env:         rt.Env,
			}, nil
		}
	}

	// Doesn't exist — return as new. Caller decides whether to create.
	venv := ""
	if canonical == "py" {
		venv = project.FindVenv(cwd)
	}

	return &Result{
		Name:  canonicalName,
		Lang:  canonical,
		Cwd:   root,
		Venv:  venv,
		IsNew: true,
	}, nil
}

// computeCanonicalName determines the kernel name for a language
// in a project. Uses the project name cascade from the spec:
//
//  1. lang@project (default)
//  2. If that name exists for a different path → lang@parent-project
func computeCanonicalName(
	canonical string,
	projName string,
	root string,
	kernels []state.Kernel,
	runtimes []state.Runtime,
) string {
	name := canonical + "@" + projName

	// Check for collision: does this name exist for a different path?
	for _, k := range kernels {
		if k.Name == name && k.Cwd != root {
			// Collision — prepend parent folder.
			return canonical + "@" + parentQualifiedName(root)
		}
	}
	for _, rt := range runtimes {
		if rt.Name == name && rt.Cwd != "" && rt.Cwd != root {
			return canonical + "@" + parentQualifiedName(root)
		}
	}

	return name
}

// parseInstance checks if input ends with ".N" where N is an integer >= 2.
// Returns the base string, the instance number, and true if matched.
// "py.2" → ("py", 2, true), "py" → ("", 0, false), "py.ml" → ("", 0, false)
func parseInstance(input string) (string, int, bool) {
	i := strings.LastIndex(input, ".")
	if i < 1 || i == len(input)-1 {
		return "", 0, false
	}
	n, err := strconv.Atoi(input[i+1:])
	if err != nil || n < 2 {
		return "", 0, false
	}
	return input[:i], n, true
}

// parentQualifiedName returns "parent-basename" for collision tiebreaking.
// e.g. ~/Work/sidehustle/backend → "sidehustle-backend"
func parentQualifiedName(root string) string {
	base := runtimeid.SlugPart(project.Name(root))
	parent := runtimeid.SlugPart(project.Name(filepath.Dir(root)))
	if parent == base || parent == "." || parent == string(filepath.Separator) {
		return base
	}
	return parent + "-" + base
}
