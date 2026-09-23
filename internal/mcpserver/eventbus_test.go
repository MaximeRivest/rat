package mcpserver

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

func TestEventsViewTracksRunsInProgress(t *testing.T) {
	b := NewEventBus(server.NewMCPServer("t", "0"))
	b.Publish(map[string]any{"kind": "run_started", "run_id": "a", "caller": "Lilly's agent", "code": "train()"})
	b.Publish(map[string]any{"kind": "run_output", "run_id": "a", "text": "epoch 1\n"})
	b.Publish(map[string]any{"kind": "run_started", "run_id": "b", "code": "x = 1"})
	b.Publish(map[string]any{"kind": "run_ended", "run_id": "b", "ok": true})
	b.Publish(map[string]any{"kind": "run_waiting", "run_id": "a", "prompt": "Continue? ", "secret": false})

	v := b.View(0, true)
	if v.Seq != 5 || len(v.Events) != 5 || v.Boot == "" {
		t.Fatalf("view = %+v", v)
	}
	if len(v.Active) != 1 || v.Active[0]["run_id"] != "a" || v.Active[0]["output"] != "epoch 1\n" || v.Active[0]["caller"] != "Lilly's agent" {
		t.Fatalf("active = %+v, want only run a with its output", v.Active)
	}
	if w, _ := v.Active[0]["waiting"].(map[string]any); w["prompt"] != "Continue? " {
		t.Fatalf("waiting = %+v", v.Active[0]["waiting"])
	}
	b.Publish(map[string]any{"kind": "run_input_done", "run_id": "a"})
	if _, still := b.View(0, true).Active[0]["waiting"]; still {
		t.Fatal("input answered, the run still says it waits")
	}
	if got := b.View(4, false); len(got.Events) != 2 || got.Gap || got.Active != nil {
		t.Fatalf("since 4 = %+v", got)
	}
	if other := NewEventBus(server.NewMCPServer("t", "0")); other.View(0, false).Boot == v.Boot {
		t.Fatal("two server processes share a boot id")
	}
}

func TestEventsViewReportsAGapAndCapsOutput(t *testing.T) {
	b := NewEventBus(server.NewMCPServer("t", "0"))
	b.Publish(map[string]any{"kind": "run_started", "run_id": "a"})
	chunk := strings.Repeat("x", 64*1024)
	for i := 0; i < 300; i++ { // more than the buffer holds
		b.Publish(map[string]any{"kind": "run_output", "run_id": "a", "text": chunk})
	}
	if v := b.View(1, false); !v.Gap {
		t.Fatal("events after seq 1 fell out of the buffer, but no gap was reported")
	}
	if v := b.View(b.View(0, false).Seq, false); v.Gap || len(v.Events) != 0 {
		t.Fatalf("an up-to-date reader got %+v", v)
	}
	run := b.View(0, true).Active[0]
	if out, _ := run["output"].(string); len(out) != activeOutputCap || run["output_cut"] != true {
		t.Fatalf("output kept = %d bytes, cut = %v", len(out), run["output_cut"])
	}
}
