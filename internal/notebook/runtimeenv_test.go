package notebook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The packages of R and Julia notebooks, through the runtimes' own
// scripts (runtimeenv.go). Skipped without the runtime.

func requireRuntime(t *testing.T, bin, probe string) {
	t.Helper()
	if _, err := exec.LookPath(bin); err != nil {
		t.Skip(bin + " not available")
	}
	if probe != "" && exec.Command(bin, "-e", probe).Run() != nil {
		t.Skip(bin + ": " + probe + " fails")
	}
}

func gitProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := exec.Command("git", "init", "-q", dir).Run(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRPackagesAreCheckedInstalledAndLocked(t *testing.T) {
	requireRuntime(t, "Rscript", "library(jsonlite)")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no user runtime overrides
	dir := gitProject(t)
	store := tempStore(t)

	bad := writeNotebook(t, dir, "bad.md", "---\nrat:\n  r:\n    dependencies: [\"nope::thing\"]\n---\n\n```r\n1\n```\n")
	r, err := Doctor(store, bad, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Blocked || !strings.Contains(checkDetail(r, "packages-r"), "unknown package source nope::") {
		t.Fatalf("a bad line: blocked=%v %+v", r.Blocked, r.Checks)
	}

	nb := writeNotebook(t, dir, "nb.md", "---\nrat:\n  r:\n    dependencies: [jsonlite]\n---\n\n```r\n1\n```\n")
	r, err = Doctor(store, nb, Options{})
	if err != nil {
		t.Fatal(err)
	}
	es := r.Envs["r"]
	if es == nil || len(es.Missing) != 0 || !es.Relock || !r.OK {
		t.Fatalf("doctor: ok=%v env=%+v checks=%+v", r.OK, es, r.Checks)
	}
	r, err = Ensure(store, nb, Options{})
	if err != nil || !r.OK {
		t.Fatalf("ensure: %v %+v %+v", err, r.Checks, r.Steps)
	}
	lock, err := os.ReadFile(filepath.Join(dir, ".rat", "r.lock"))
	if err != nil || !strings.Contains(string(lock), "Package: jsonlite") || !strings.Contains(string(lock), "Declared: jsonlite") {
		t.Fatalf("lock = %s (%v)", lock, err)
	}
	if ignore, _ := os.ReadFile(filepath.Join(dir, ".rat", ".gitignore")); !strings.Contains(string(ignore), "!r.lock") {
		t.Fatalf(".gitignore = %s", ignore)
	}
	if r, _ = Doctor(store, nb, Options{}); r.Envs["r"].Relock {
		t.Fatal("the lock was written again for nothing")
	}

	// The lock wants its version: another one installed is missing.
	lines := strings.Split(string(lock), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "Package: jsonlite") {
			for j := i; j < len(lines) && lines[j] != ""; j++ {
				if strings.HasPrefix(lines[j], "Version:") {
					lines[j] = "Version: 0.0.1"
				}
				if strings.HasPrefix(lines[j], "Ref:") {
					lines[j] = "Ref: jsonlite@0.0.1"
				}
			}
		}
	}
	os.WriteFile(filepath.Join(dir, ".rat", "r.lock"), []byte(strings.Join(lines, "\n")), 0o644)
	r, _ = Doctor(store, nb, Options{})
	if es := r.Envs["r"]; len(es.Missing) != 1 || es.Missing[0] != "jsonlite@0.0.1" || r.OK {
		t.Fatalf("a locked version not installed: %+v", es)
	}
	if r, _ = Doctor(store, nb, Options{Update: true}); len(r.Envs["r"].Missing) != 0 {
		t.Fatalf("--update ignores the lock: %+v", r.Envs["r"])
	}
}

func TestJuliaPackagesDevelopALocalPackageAndLock(t *testing.T) {
	requireRuntime(t, "julia", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("JULIA_DEPOT_PATH", t.TempDir()+string(os.PathListSeparator))
	t.Setenv("JULIA_PKG_OFFLINE", "true")
	dir := gitProject(t)
	pkg := filepath.Join(dir, "MyPkg")
	os.MkdirAll(filepath.Join(pkg, "src"), 0o755)
	os.WriteFile(filepath.Join(pkg, "Project.toml"), []byte("name = \"MyPkg\"\nuuid = \"4b7c1a2e-3f5d-4e6a-8b9c-0d1e2f3a4b5c\"\nversion = \"0.1.0\"\n"), 0o644)
	os.WriteFile(filepath.Join(pkg, "src", "MyPkg.jl"), []byte("module MyPkg\nhello() = \"hi\"\nend\n"), 0o644)
	store := tempStore(t)
	nb := writeNotebook(t, dir, "nb.md", "---\nrat:\n  julia:\n    dependencies: [./MyPkg]\n---\n\n```julia\n1\n```\n")
	r, err := Doctor(store, nb, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if es := r.Envs["julia"]; es == nil || len(es.Missing) != 1 || es.Missing[0] != "./MyPkg" {
		t.Fatalf("doctor: %+v %+v", es, r.Checks)
	}
	r, err = Ensure(store, nb, Options{})
	if err != nil || !r.OK {
		t.Fatalf("ensure: %v %+v %+v", err, r.Checks, r.Steps)
	}
	if m, err := os.ReadFile(filepath.Join(dir, ".rat", "julia", "Manifest.toml")); err != nil || !strings.Contains(string(m), "[[deps.MyPkg]]") {
		t.Fatalf("manifest = %s (%v)", m, err)
	}
}

func checkDetail(r *Report, id string) string {
	for _, c := range r.Checks {
		if c.ID == id {
			return c.Detail
		}
	}
	return ""
}
