package generic

import (
	"reflect"
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
