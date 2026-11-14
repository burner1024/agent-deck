package main

import (
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A Claude (or other harness) process started from a Codex pane's shell
// inherits that pane's AGENTDECK_INSTANCE_ID and AGENTDECK_TOOL. Its hooks
// reach hook-handler and must not be credited to the Codex session: they would
// overwrite its hook status and session id with a foreign conversation's.

func TestHookHandler_ForeignHarnessInCodexPane(t *testing.T) {
	claudeStop := `{"hook_event_name":"Stop","session_id":"` + bgWorkerTestSession + `","cwd":"/tmp"}`
	codexStop := `{"hook_event_name":"Stop","session_id":"019a0000-0000-7000-8000-000000000002","turn_id":"turn-1","cwd":"/tmp"}`
	tests := []struct {
		name      string
		tool      string
		payload   string
		wantWrite bool
	}{
		{name: "claude hook in a codex pane is dropped", tool: "codex", payload: claudeStop, wantWrite: false},
		{name: "codex hook in a codex pane is credited", tool: "codex", payload: codexStop, wantWrite: true},
		{name: "claude hook in a claude pane is credited", tool: "claude", payload: claudeStop, wantWrite: true},
		{name: "claude hook without AGENTDECK_TOOL is credited", tool: "", payload: claudeStop, wantWrite: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupBgWorkerHookEnv(t)
			inherited := "codex"
			if tt.tool == "codex" {
				inherited = "claude"
			}
			t.Setenv("AGENTDECK_TOOL", inherited)
			t.Setenv("AGENTDECK_TOOL", tt.tool)
			runHookHandlerWith(t, tt.payload)
			if got := hookStatusWritten(t); got != tt.wantWrite {
				t.Fatalf("hook status written = %v, want %v", got, tt.wantWrite)
			}
			data, _ := os.ReadFile(session.GetSessionIDLifecycleLogPath())
			logged := strings.Contains(string(data), `"reason":"hook_from_foreign_harness"`)
			if logged == tt.wantWrite {
				t.Fatalf("foreign-harness reject logged = %v, want %v", logged, !tt.wantWrite)
			}
		})
	}
}
