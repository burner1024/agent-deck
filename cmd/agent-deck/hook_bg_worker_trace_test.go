package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTraceHook_BgWorkerRouting(t *testing.T) {
	for _, tc := range []struct {
		name, pane, owner, profile string
	}{
		{name: "same pane", pane: "pane-a", owner: "inst-pane-a", profile: "starter"},
		{name: "another pane", pane: "pane-b", owner: "inst-pane-b", profile: "starter"},
		{name: "another profile", pane: "pane-b", owner: "inst-pane-b", profile: "parking"},
		{name: "unowned pane", pane: "unowned"},
		{name: "not parked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := setupBgWorkerTraceEnv(t)
			t.Setenv("AGENTDECK_PROFILE", "starter")
			seedParkedHookInstance(t, "starter", "inst-pane-a", "pane-a")
			profile := tc.profile
			if profile == "" {
				profile = "starter"
			}
			seedParkedHookInstance(t, profile, "inst-pane-b", "pane-b")
			claudeDir := filepath.Join(home, ".claude")
			if tc.pane != "" {
				writeClaudeSessionFile(t, claudeDir, 1, map[string]any{
					"kind": "interactive", "parkedJobId": "job", "tmux": tc.pane + ":@1.%1",
				})
			}
			env := []string{"AGENTDECK_PROFILE=starter", "AGENTDECK_INSTANCE_ID=inst-pane-a",
				"AGENTDECK_TOOL=claude", "AGENTDECK_TRACE=summaries",
				"CLAUDE_CONFIG_DIR=" + claudeDir, "CLAUDE_JOB_DIR=" + filepath.Join(claudeDir, "jobs", "job")}
			payload := `{"hook_event_name":"PostToolUse","session_id":"` + bgWorkerTestSession + `"}`
			_, stderr, code := runAgentDeckEnv(t, home, payload, env, "trace", "hook")
			if code != 0 || stderr != "" {
				t.Fatalf("trace hook exit %d: %s", code, stderr)
			}
			files := traceFiles(t, home)
			if tc.owner == "" {
				if len(files) != 0 {
					t.Fatalf("unowned hook wrote trace files: %v", files)
				}
				return
			}
			if len(files) != 1 {
				t.Fatalf("want one trace spool, got %v", files)
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range map[string]string{"instance": tc.owner, "profile": tc.profile} {
				if !strings.Contains(string(data), fmt.Sprintf(`%q:%q`, key, value)) {
					t.Errorf("trace missing %s=%s: %s", key, value, data)
				}
			}
			if !strings.Contains(files[0], string(filepath.Separator)+tc.profile+string(filepath.Separator)) {
				t.Errorf("trace spool stored outside owner profile: %s", files[0])
			}
		})
	}
}

// Tracing (ASK-129) meets the background-worker drop (#2683): a hook from a
// Claude background worker carries the pane's AGENTDECK_INSTANCE_ID but does
// not belong to it, so it must not be traced under that instance either. A
// foreground hook with tracing on still writes the trace spool.

func setupBgWorkerTraceEnv(t *testing.T) (home string) {
	t.Helper()
	setupBgWorkerHookEnv(t)
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTDECK_PROFILE", "default")
	t.Setenv("AGENTDECK_TOOL", "claude")
	t.Setenv("AGENTDECK_TRACE", "summaries")
	return home
}

func TestHookHandler_ForegroundHookIsTraced(t *testing.T) {
	home := setupBgWorkerTraceEnv(t)
	runBgWorkerHook(t)
	if files := traceFiles(t, home); len(files) != 1 {
		t.Fatalf("a foreground hook with tracing on must write the trace spool, got %v", files)
	}
	if !hookStatusWritten(t) {
		t.Fatal("a foreground hook must still write the status file")
	}
}

func TestHookHandler_BgWorkerHookIsNotTraced(t *testing.T) {
	home := setupBgWorkerTraceEnv(t)
	t.Setenv("CLAUDE_JOB_DIR", t.TempDir())
	runBgWorkerHook(t)
	if files := traceFiles(t, home); len(files) != 0 {
		t.Fatalf("a background worker hook was traced under the pane instance: %v", files)
	}
	if hookStatusWritten(t) {
		t.Fatal("a background worker hook was credited to the pane instance")
	}
	if !lifecycleLogHasBgReject(t) {
		t.Fatal("the dropped hook must leave a lifecycle reject line")
	}
}
