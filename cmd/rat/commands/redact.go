package commands

import "strings"

const redactedValue = "<redacted>"

func displayEnvValue(_, _ string) string {
	return redactedValue
}

func displayOptionValue(key, value string) string {
	if isSensitiveKey(key) {
		return redactedValue
	}
	return value
}

func isSensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	for _, marker := range []string{"token", "secret", "password", "passwd", "api_key", "apikey", "credential", "auth"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}
