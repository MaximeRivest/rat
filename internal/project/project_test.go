package project

import (
	"os"
	"path/filepath"
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
