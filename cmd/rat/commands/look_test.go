package commands

import (
	"strings"
	"testing"
)

func TestLookValidatesFlagConflictBeforeResolvingRuntime(t *testing.T) {
	oldAt := lookAt
	oldCode := lookCode
	oldCursor := lookCursor
	t.Cleanup(func() {
		lookAt = oldAt
		lookCode = oldCode
		lookCursor = oldCursor
	})

	lookAt = "df"
	lookCode = "df."
	lookCursor = -1

	err := lookCmd.RunE(lookCmd, []string{"definitely-no-such-runtime"})
	if err == nil {
		t.Fatal("expected --at/--code conflict error")
	}
	if !strings.Contains(err.Error(), "use either --at or --code") {
		t.Fatalf("error = %q, want flag validation error", err.Error())
	}
}
