package commands

import (
	"testing"

	"github.com/maximerivest/rat/internal/daemon"
)

func TestServeOptionsMapMergesDaemonEnvAndFlags(t *testing.T) {
	t.Setenv(daemon.ServeOptionsEnv, `{"token":"secret","model":"from-env"}`)

	got, err := serveOptionsMap([]string{"model=from-flag", "thinking=high"})
	if err != nil {
		t.Fatalf("serveOptionsMap: %v", err)
	}
	if got["token"] != "secret" || got["model"] != "from-flag" || got["thinking"] != "high" {
		t.Fatalf("options = %#v", got)
	}
}

func TestServeOptionsMapRejectsBadDaemonJSON(t *testing.T) {
	t.Setenv(daemon.ServeOptionsEnv, `{not json}`)

	if _, err := serveOptionsMap(nil); err == nil {
		t.Fatal("expected bad JSON error")
	}
}
