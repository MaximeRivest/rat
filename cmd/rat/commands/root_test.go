package commands

import "testing"

func TestReplTargetInputInstanceDoesNotRequireBaseStart(t *testing.T) {
	target, instance, err := replTargetInput("py", []string{"2"})
	if err != nil {
		t.Fatalf("replTargetInput: %v", err)
	}
	if target != "py.2" {
		t.Fatalf("target = %q, want py.2", target)
	}
	if instance != 2 {
		t.Fatalf("instance = %d, want 2", instance)
	}
}

func TestReplTargetInputInstanceOneUsesBase(t *testing.T) {
	target, instance, err := replTargetInput("py", []string{"1"})
	if err != nil {
		t.Fatalf("replTargetInput: %v", err)
	}
	if target != "py" {
		t.Fatalf("target = %q, want py", target)
	}
	if instance != 1 {
		t.Fatalf("instance = %d, want 1", instance)
	}
}

func TestReplTargetInputRejectsUnexpectedArgs(t *testing.T) {
	if _, _, err := replTargetInput("py", []string{"two"}); err == nil {
		t.Fatal("expected error for non-numeric instance")
	}
	if _, _, err := replTargetInput("py", []string{"2", "extra"}); err == nil {
		t.Fatal("expected error for extra args")
	}
}

func TestSplitInstanceSuffix(t *testing.T) {
	base, instance, ok := splitInstanceSuffix("py@project.3")
	if !ok {
		t.Fatal("expected instance suffix")
	}
	if base != "py@project" || instance != 3 {
		t.Fatalf("got (%q, %d), want (py@project, 3)", base, instance)
	}

	if _, _, ok := splitInstanceSuffix("py@project"); ok {
		t.Fatal("did not expect suffix")
	}
	if _, _, ok := splitInstanceSuffix("py@project.1"); ok {
		t.Fatal(".1 is not an instance suffix")
	}
}
