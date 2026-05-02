package runtimeid

import "testing"

func TestValidateName(t *testing.T) {
	valid := []string{
		"py",
		"py@project",
		"py@project.2",
		"py-ml",
		"r_stats",
		"pi.sonnet",
	}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q): %v", name, err)
		}
	}

	invalid := []string{
		"",
		"../evil",
		"py/evil",
		"py\\evil",
		" py",
		".hidden",
		"-dash",
		"name with spaces",
		"name:colon",
	}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q): expected error", name)
		}
	}
}

func TestSlugPart(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my project", "my-project"},
		{"...My Project!!!", "My-Project"},
		{"cafe-data", "cafe-data"},
		{" café ", "caf"},
		{"!!!", "project"},
	}
	for _, tt := range tests {
		if got := SlugPart(tt.input); got != tt.want {
			t.Errorf("SlugPart(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
