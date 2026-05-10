package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximerivest/rat/internal/generic"
)

func TestCanInstallMissingRuntime(t *testing.T) {
	if !canInstallMissingRuntime(generic.InstallStep{Manager: "npm", Deps: []string{"@earendil-works/pi-coding-agent"}}) {
		t.Fatal("npm runtime deps should be allowed to provision a missing runtime binary")
	}
	if canInstallMissingRuntime(generic.InstallStep{Manager: "pip", Deps: []string{"prompt-toolkit"}}) {
		t.Fatal("pip deps should not be treated as provisioning the runtime binary")
	}
}

func TestInstallNPMPackagesUsesGlobalInstall(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "npm.log")
	npmPath := filepath.Join(dir, "npm")
	script := "#!/bin/sh\nprintf '%s\n' \"$*\" > \"$RAT_NPM_LOG\"\n"
	if err := os.WriteFile(npmPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RAT_NPM_LOG", logPath)

	npm, err := detectNPM()
	if err != nil {
		t.Fatalf("detectNPM: %v", err)
	}
	if npm != npmPath {
		t.Fatalf("detectNPM() = %q, want %q", npm, npmPath)
	}

	if err := installNPMPackages(npm, []string{"@earendil-works/pi-coding-agent"}); err != nil {
		t.Fatalf("installNPMPackages: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := strings.TrimSpace(string(data))
	want := "install -g @earendil-works/pi-coding-agent"
	if got != want {
		t.Fatalf("npm args = %q, want %q", got, want)
	}
}
