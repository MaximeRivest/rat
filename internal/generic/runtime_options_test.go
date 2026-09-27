package generic

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeOptionsRejectsUnknownOption(t *testing.T) {
	cfg := &RuntimeConfig{
		Name: "pi",
		Options: map[string]RuntimeOption{
			"model": {Arg: "--model"},
		},
	}
	if _, err := cfg.NormalizeOptions(map[string]string{"provider": "anthropic"}); err == nil {
		t.Fatal("expected unknown option error")
	}
}

func TestOptionArgsAndEnv(t *testing.T) {
	cfg := &RuntimeConfig{
		Name: "pi",
		Options: map[string]RuntimeOption{
			"model":    {Arg: "--model"},
			"thinking": {Arg: "--thinking", Enum: []string{"off", "high"}},
			"profile":  {Env: "AWS_PROFILE"},
		},
	}

	args, err := cfg.OptionArgs(map[string]string{
		"thinking": "high",
		"model":    "claude-sonnet-4-5",
		"profile":  "prod",
	})
	if err != nil {
		t.Fatalf("OptionArgs: %v", err)
	}
	wantArgs := []string{"--model", "claude-sonnet-4-5", "--thinking", "high"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("OptionArgs() = %#v, want %#v", args, wantArgs)
	}

	env, err := cfg.OptionEnv(map[string]string{"profile": "prod"})
	if err != nil {
		t.Fatalf("OptionEnv: %v", err)
	}
	if got := env["AWS_PROFILE"]; got != "prod" {
		t.Fatalf("AWS_PROFILE = %q, want %q", got, "prod")
	}
}

func TestTmuxOptionStringQuotesValues(t *testing.T) {
	cfg := &RuntimeConfig{
		Name: "pi",
		Options: map[string]RuntimeOption{
			"model": {Arg: "--model"},
		},
	}
	got, err := cfg.TmuxOptionString(map[string]string{"model": "openai/gpt-4o mini"})
	if err != nil {
		t.Fatalf("TmuxOptionString: %v", err)
	}
	want := "'--model' 'openai/gpt-4o mini'"
	if got != want {
		t.Fatalf("TmuxOptionString() = %q, want %q", got, want)
	}
}

func TestKnownPathFindsWhereAnInstallerPutIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	bin := filepath.Join(home, ".juliaup", "bin", "julia")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	for _, v := range []string{"1.9", "1.12"} {
		p := filepath.Join(home, "R", "R-"+v, "bin", "Rscript")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(""), 0o755)
	}
	cfg := &RuntimeConfig{Display: "Julia"}
	cfg.Detect.Commands = []string{"julia"}
	cfg.Detect.Paths = []string{"~/.juliaup/bin/julia"}
	if got, err := cfg.DetectBinary(); err != nil || got != bin {
		t.Fatalf("DetectBinary = %q, %v", got, err)
	}
	r := &RuntimeConfig{}
	r.Detect.Paths = []string{"$HOME/R/R-*/bin/Rscript"}
	if got := r.KnownPath(); !strings.HasSuffix(got, filepath.Join("R-1.12", "bin", "Rscript")) {
		t.Fatalf("the newest is R-1.12, not R-1.9 — got %q", got)
	}
}
