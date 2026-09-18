package project

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindRootWithGit(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "src", "pkg")
	os.MkdirAll(sub, 0755)
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)

	root, found := FindRoot(sub)
	if !found {
		t.Fatal("expected to find project root")
	}
	if root != dir {
		t.Fatalf("expected root %s, got %s", dir, root)
	}
}

func TestFindRootWithPyproject(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]"), 0644)

	root, found := FindRoot(dir)
	if !found {
		t.Fatal("expected to find project root")
	}
	if root != dir {
		t.Fatalf("expected root %s, got %s", dir, root)
	}
}

func TestFindRootWithJulia(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Project.toml"), []byte("[deps]"), 0644)

	root, found := FindRoot(dir)
	if !found {
		t.Fatal("expected to find Julia project root")
	}
	if root != dir {
		t.Fatalf("expected root %s, got %s", dir, root)
	}
}

func TestFindRootWithRenv(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "renv.lock"), []byte("{}"), 0644)

	root, found := FindRoot(dir)
	if !found {
		t.Fatal("expected to find R renv project root")
	}
	if root != dir {
		t.Fatalf("expected root %s, got %s", dir, root)
	}
}

func TestFindRootWithSln(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "MyApp.sln"), []byte(""), 0644)

	root, found := FindRoot(dir)
	if !found {
		t.Fatal("expected to find .sln project root")
	}
	if root != dir {
		t.Fatalf("expected root %s, got %s", dir, root)
	}
}

func TestFindRootMonorepo(t *testing.T) {
	// Simulate: rat/ has .git + go.mod, rat/vscode-rat/ has package.json
	root := t.TempDir()
	sub := filepath.Join(root, "vscode-rat")
	deep := filepath.Join(sub, "src")
	os.MkdirAll(filepath.Join(root, ".git"), 0755)
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module m"), 0644)
	os.MkdirAll(deep, 0755)
	os.WriteFile(filepath.Join(sub, "package.json"), []byte("{}"), 0644)

	// From vscode-rat/src/ → should find package.json in vscode-rat/
	got, found := FindRoot(deep)
	if !found {
		t.Fatal("expected to find project root from sub/src")
	}
	if got != sub {
		t.Fatalf("from sub/src: expected root %s, got %s", sub, got)
	}

	// From rat/internal/ → should find .git in rat/
	internal := filepath.Join(root, "internal")
	os.MkdirAll(internal, 0755)
	got2, found2 := FindRoot(internal)
	if !found2 {
		t.Fatal("expected to find project root from internal/")
	}
	if got2 != root {
		t.Fatalf("from internal/: expected root %s, got %s", root, got2)
	}

	// From rat/ root → should find .git in rat/
	got3, found3 := FindRoot(root)
	if !found3 {
		t.Fatal("expected to find project root from root")
	}
	if got3 != root {
		t.Fatalf("from root: expected %s, got %s", root, got3)
	}
}

func TestFindRootNoMarker(t *testing.T) {
	dir := t.TempDir()

	root, found := FindRoot(dir)
	if found {
		t.Fatal("expected no project root found")
	}
	if root != dir {
		t.Fatalf("expected cwd %s as fallback, got %s", dir, root)
	}
}

func TestName(t *testing.T) {
	if got := Name("/home/user/Projects/myapp"); got != "myapp" {
		t.Fatalf("expected myapp, got %s", got)
	}
}

func TestNameHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}
	if got := Name(home); got != "home" {
		t.Fatalf("expected home for ~, got %s", got)
	}
}

// makeVenv creates the minimal shape rat recognises as a venv.
func makeVenv(t *testing.T, dir string) string {
	t.Helper()
	venv := filepath.Join(dir, ".venv")
	binDir := filepath.Join(venv, "bin")
	if runtime.GOOS == "windows" {
		binDir = filepath.Join(venv, "Scripts")
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(binDir, "python")
	if runtime.GOOS == "windows" {
		py = filepath.Join(binDir, "python.exe")
	}
	if err := os.WriteFile(py, []byte(""), 0755); err != nil {
		t.Fatal(err)
	}
	return venv
}

func TestFindRootWeakMarkerInsideRepo(t *testing.T) {
	// repo/.git + repo/pyproject.toml; repo/docs/requirements.txt + Pipfile
	// (a documentation build) must NOT make docs/ its own project.
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0755)
	os.WriteFile(filepath.Join(repo, "pyproject.toml"), []byte("[project]\nname='x'"), 0644)
	docs := filepath.Join(repo, "docs")
	deep := filepath.Join(docs, "docs", "tutorials", "voice")
	os.MkdirAll(deep, 0755)
	os.WriteFile(filepath.Join(docs, "requirements.txt"), []byte("mkdocs"), 0644)
	os.WriteFile(filepath.Join(docs, "Pipfile"), []byte(""), 0644)

	got, found := FindRoot(deep)
	if !found || got != repo {
		t.Fatalf("expected repo root %s, got %s (found=%v)", repo, got, found)
	}
	got, _ = FindRoot(docs)
	if got != repo {
		t.Fatalf("from docs/: expected %s, got %s", repo, got)
	}
}

func TestFindRootWeakMarkerAlone(t *testing.T) {
	// A bare folder with only requirements.txt and nothing stronger above
	// is still a project.
	base := t.TempDir()
	scratch := filepath.Join(base, "scratch")
	sub := filepath.Join(scratch, "sub")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(scratch, "requirements.txt"), []byte("numpy"), 0644)

	got, found := FindRoot(sub)
	if !found || got != scratch {
		t.Fatalf("expected %s, got %s (found=%v)", scratch, got, found)
	}
}

func TestFindRootNearestWeakMarkerWins(t *testing.T) {
	// Two weak markers, nothing strong: the nearest one is the project.
	base := t.TempDir()
	outer := filepath.Join(base, "outer")
	inner := filepath.Join(outer, "inner")
	os.MkdirAll(inner, 0755)
	os.WriteFile(filepath.Join(outer, "Makefile"), []byte(""), 0644)
	os.WriteFile(filepath.Join(inner, "requirements.txt"), []byte(""), 0644)

	got, _ := FindRoot(inner)
	if got != inner {
		t.Fatalf("expected nearest weak marker %s, got %s", inner, got)
	}
}

func TestFindRootVenvIsStrongest(t *testing.T) {
	// monorepo/.git; monorepo/services/api/.venv (+ only a weak marker):
	// the sub-folder with its own environment is its own project.
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0755)
	api := filepath.Join(repo, "services", "api")
	src := filepath.Join(api, "src")
	os.MkdirAll(src, 0755)
	os.WriteFile(filepath.Join(api, "requirements.txt"), []byte(""), 0644)
	venv := makeVenv(t, api)

	got, found := FindRoot(src)
	if !found || got != api {
		t.Fatalf("expected %s, got %s (found=%v)", api, got, found)
	}
	if v := FindVenv(src); v != venv {
		t.Fatalf("expected venv %s, got %q", venv, v)
	}
}

func TestFindVenvStopsAtProjectRoot(t *testing.T) {
	// home/.venv above home/proj/.git: the project must not inherit it.
	base := t.TempDir()
	makeVenv(t, base)
	proj := filepath.Join(base, "proj")
	sub := filepath.Join(proj, "nb")
	os.MkdirAll(sub, 0755)
	os.MkdirAll(filepath.Join(proj, ".git"), 0755)

	if v := FindVenv(sub); v != "" {
		t.Fatalf("expected no venv (must not walk past project root), got %q", v)
	}
}

func TestFindVenvFromNestedFolder(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0755)
	venv := makeVenv(t, repo)
	deep := filepath.Join(repo, "docs", "tutorials")
	os.MkdirAll(deep, 0755)
	os.WriteFile(filepath.Join(repo, "docs", "requirements.txt"), []byte(""), 0644)

	if v := FindVenv(deep); v != venv {
		t.Fatalf("expected %s, got %q", venv, v)
	}
}
