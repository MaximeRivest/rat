package commands

import "testing"

func TestDisplayEnvValueRedacts(t *testing.T) {
	if got := displayEnvValue("SLACK_BOT_TOKEN", "xoxb-secret"); got != redactedValue {
		t.Fatalf("displayEnvValue() = %q, want %q", got, redactedValue)
	}
}

func TestDisplayOptionValueRedactsSensitiveKeys(t *testing.T) {
	for _, key := range []string{"token", "api_key", "password", "client-secret", "auth_header"} {
		if got := displayOptionValue(key, "secret"); got != redactedValue {
			t.Fatalf("displayOptionValue(%q) = %q, want %q", key, got, redactedValue)
		}
	}
}

func TestDisplayOptionValueKeepsOrdinaryKeys(t *testing.T) {
	if got := displayOptionValue("model", "claude-sonnet"); got != "claude-sonnet" {
		t.Fatalf("displayOptionValue(model) = %q, want claude-sonnet", got)
	}
}
