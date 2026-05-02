package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximerivest/rat/internal/state"
)

func tempStore(t *testing.T) *state.Store {
	t.Helper()
	return state.NewStore(filepath.Join(t.TempDir(), "state.yaml"))
}

func TestStopStoppedKernelReturnsNotRunning(t *testing.T) {
	s := tempStore(t)
	if err := s.Put(state.Kernel{
		Name:    "py@proj",
		Lang:    "py",
		Status:  state.StatusStopped,
		PID:     0,
		Port:    0,
		Cwd:     "/tmp/proj",
		Started: time.Now(),
		Stopped: time.Now(),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err := Stop(s, "py@proj")
	if err == nil {
		t.Fatal("expected error for stopped kernel")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStopAllIgnoresStoppedKernels(t *testing.T) {
	s := tempStore(t)
	if err := s.Put(state.Kernel{
		Name:    "py@proj",
		Lang:    "py",
		Status:  state.StatusStopped,
		PID:     0,
		Port:    0,
		Cwd:     "/tmp/proj",
		Started: time.Now(),
		Stopped: time.Now(),
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	n, err := StopAll(s)
	if err != nil {
		t.Fatalf("StopAll: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 stopped kernels to be signaled, got %d", n)
	}
}

func TestOpenKernelLogPermissions(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")

	f, err := openKernelLog(logDir, "py@proj")
	if err != nil {
		t.Fatalf("openKernelLog: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dirInfo, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("Stat(logDir): %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("log dir mode = %#o, want 0o700", got)
	}

	fileInfo, err := os.Stat(filepath.Join(logDir, "py@proj.log"))
	if err != nil {
		t.Fatalf("Stat(logFile): %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("log file mode = %#o, want 0o600", got)
	}
}

func TestOpenKernelLogRejectsInvalidName(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	if _, err := openKernelLog(logDir, "../evil"); err == nil {
		t.Fatal("expected invalid runtime name error")
	}
}

func TestBuildServeArgsDoNotIncludeEnvOrOptions(t *testing.T) {
	opts := StartOpts{
		Name:    "slack@proj",
		Lang:    "slack",
		Cwd:     "/tmp/proj",
		Env:     map[string]string{"SLACK_BOT_TOKEN": "xoxb-secret"},
		Options: map[string]string{"token": "secret-token", "model": "claude-sonnet"},
	}

	args := buildServeArgs(opts, 8717)
	joined := strings.Join(args, "\x00")
	for _, forbidden := range []string{"xoxb-secret", "secret-token", "SLACK_BOT_TOKEN", "--env", "--opt"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("serve args leak %q: %#v", forbidden, args)
		}
	}
	if !strings.Contains(joined, "serve\x00slack@proj") || !strings.Contains(joined, "--port\x008717") {
		t.Fatalf("serve args missing expected fields: %#v", args)
	}
}

func TestBuildServeEnvCarriesEnvAndOptions(t *testing.T) {
	opts := StartOpts{
		Env:     map[string]string{"SLACK_BOT_TOKEN": "xoxb-new"},
		Options: map[string]string{"token": "secret-token", "model": "claude-sonnet"},
	}
	env := buildServeEnv([]string{"PATH=/bin", "SLACK_BOT_TOKEN=old", ServeOptionsEnv + "=stale"}, opts)

	if got := envValue(env, "SLACK_BOT_TOKEN"); got != "xoxb-new" {
		t.Fatalf("SLACK_BOT_TOKEN = %q, want xoxb-new", got)
	}
	rawOptions := envValue(env, ServeOptionsEnv)
	if rawOptions == "" || rawOptions == "stale" {
		t.Fatalf("%s = %q, want fresh JSON", ServeOptionsEnv, rawOptions)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(rawOptions), &decoded); err != nil {
		t.Fatalf("decode %s: %v", ServeOptionsEnv, err)
	}
	if decoded["token"] != "secret-token" || decoded["model"] != "claude-sonnet" {
		t.Fatalf("decoded options = %#v", decoded)
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}
