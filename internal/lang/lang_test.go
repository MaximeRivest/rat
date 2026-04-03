package lang

import "testing"

func TestResolve(t *testing.T) {
	tests := []struct {
		input string
		want  string
		err   bool
	}{
		{"py", "py", false},
		{"python", "py", false},
		{"r", "r", false},
		{"jl", "jl", false},
		{"ju", "jl", false},
		{"julia", "jl", false},
		{"sh", "sh", false},
		{"bash", "sh", false},
		{"js", "js", false},
		{"node", "js", false},
		{"javascript", "js", false},
		{"xyz", "", true},
		{"python3", "", true},
		{"zsh", "", true},
	}
	for _, tt := range tests {
		got, err := Resolve(tt.input)
		if tt.err && err == nil {
			t.Errorf("Resolve(%q): expected error", tt.input)
		}
		if !tt.err && err != nil {
			t.Errorf("Resolve(%q): unexpected error: %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("Resolve(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsAlias(t *testing.T) {
	if !IsAlias("py") {
		t.Error("py should be an alias")
	}
	if IsAlias("python3") {
		t.Error("python3 should not be an alias")
	}
}

func TestInferFromName(t *testing.T) {
	tests := []struct {
		name string
		want string
		ok   bool
	}{
		{"py-ml", "py", true},
		{"r-stats", "r", true},
		{"sh-dev", "sh", true},
		{"js_test", "js", true},
		{"my-kernel", "", false},
		{"py", "py", true},
	}
	for _, tt := range tests {
		got, ok := InferFromName(tt.name)
		if ok != tt.ok {
			t.Errorf("InferFromName(%q): ok=%v, want %v", tt.name, ok, tt.ok)
		}
		if got != tt.want {
			t.Errorf("InferFromName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
