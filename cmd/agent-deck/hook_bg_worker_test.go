package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Issue #2683: Claude Code's agent-view supervisor and its bg-spare workers
// keep the environment of the pane that first started them, including
// AGENTDECK_INSTANCE_ID. Their hooks belong to whichever conversation was
// backgrounded, not to that pane, so they must not be credited to it.

const bgWorkerTestSession = "99999999-9999-4999-8999-999999999999"

func setupBgWorkerHookEnv(t *testing.T) (claudeDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir = filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CLAUDE_JOB_DIR", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "inst-pane-a")
	return claudeDir
}

func runBgWorkerHook(t *testing.T) {
	t.Helper()
	runHookHandlerWith(t, `{"hook_event_name":"PostToolUse","session_id":"`+bgWorkerTestSession+`"}`)
}

func hookStatusWritten(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(getHooksDir(), "inst-pane-a.json"))
	return err == nil
}

func lifecycleLogHasBgReject(t *testing.T) bool {
	t.Helper()
	data, _ := os.ReadFile(session.GetSessionIDLifecycleLogPath())
	return strings.Contains(string(data), `"reason":"hook_from_background_worker"`) &&
		strings.Contains(string(data), bgWorkerTestSession)
}

func TestHookHandler_ForegroundHookIsCredited(t *testing.T) {
	setupBgWorkerHookEnv(t)
	runBgWorkerHook(t)
	if !hookStatusWritten(t) {
		t.Fatal("a hook from the pane's own Claude must still write the status file")
	}
}

func TestHookHandler_DropsHookWithClaudeJobDir(t *testing.T) {
	setupBgWorkerHookEnv(t)
	t.Setenv("CLAUDE_JOB_DIR", t.TempDir())
	runBgWorkerHook(t)
	runBgWorkerHook(t)
	if hookStatusWritten(t) {
		t.Fatal("a background worker hook (CLAUDE_JOB_DIR set) was credited to the pane instance")
	}
	if !lifecycleLogHasBgReject(t) {
		t.Fatal("the dropped hook must leave a lifecycle reject line")
	}
	data, _ := os.ReadFile(session.GetSessionIDLifecycleLogPath())
	if n := strings.Count(string(data), "hook_from_background_worker"); n != 1 {
		t.Fatalf("want one lifecycle line per worker session, got %d", n)
	}
}

func TestHookHandler_DropsHookFromBgKindPidFile(t *testing.T) {
	claudeDir := setupBgWorkerHookEnv(t)
	sessionsDir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := os.Getppid()
	rec := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"kind":"bg"}`, pid, bgWorkerTestSession)
	if err := os.WriteFile(filepath.Join(sessionsDir, fmt.Sprintf("%d.json", pid)), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	runBgWorkerHook(t)
	if hookStatusWritten(t) {
		t.Fatal("a hook from a kind=bg Claude process was credited to the pane instance")
	}
	if !lifecycleLogHasBgReject(t) {
		t.Fatal("the dropped hook must leave a lifecycle reject line")
	}
}

// Review follow-up on #2683: CLAUDE_JOB_DIR can leak into a pane through its
// tmux server's environment. When the nearest Claude ancestor's sessions
// record says it is a foreground (non-bg) process, that record wins and the
// hook is credited.
func TestHookHandler_ForegroundKindBeatsLeakedClaudeJobDir(t *testing.T) {
	claudeDir := setupBgWorkerHookEnv(t)
	t.Setenv("CLAUDE_JOB_DIR", t.TempDir())
	sessionsDir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := os.Getppid()
	rec := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"kind":"interactive"}`, pid, bgWorkerTestSession)
	if err := os.WriteFile(filepath.Join(sessionsDir, fmt.Sprintf("%d.json", pid)), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	runBgWorkerHook(t)
	if !hookStatusWritten(t) {
		t.Fatal("a hook whose nearest Claude ancestor is a foreground process was dropped because CLAUDE_JOB_DIR leaked into the pane")
	}
	if lifecycleLogHasBgReject(t) {
		t.Fatal("a credited foreground hook must not log a background-worker reject")
	}
}

// The process-level scrub runs before any tmux exec for every command except
// the hook handler, which reads CLAUDE_JOB_DIR as a bg-worker signal.
func TestScrubClaudeJobDirForCommand(t *testing.T) {
	t.Setenv("CLAUDE_JOB_DIR", "/tmp/jobs/abc")
	scrubClaudeJobDirForCommand([]string{"hook-handler"})
	if _, ok := os.LookupEnv("CLAUDE_JOB_DIR"); !ok {
		t.Fatal("hook-handler must keep CLAUDE_JOB_DIR")
	}
	scrubClaudeJobDirForCommand([]string{"trace", "hook"})
	if _, ok := os.LookupEnv("CLAUDE_JOB_DIR"); !ok {
		t.Fatal("trace hook must keep CLAUDE_JOB_DIR")
	}
	scrubClaudeJobDirForCommand([]string{"add", "."})
	if _, ok := os.LookupEnv("CLAUDE_JOB_DIR"); ok {
		t.Fatal("CLAUDE_JOB_DIR must be scrubbed before agent-deck can start a tmux server")
	}
	scrubClaudeJobDirForCommand(nil) // bare TUI launch: no panic, still scrubbed
}

// A parked job's hooks belong to the instance that owns the parking pane,
// even when the worker inherited a different instance ID or profile.
func TestHookHandler_BgWorkerOfJobParkedByThisPane(t *testing.T) {
	for _, tc := range []struct {
		name       string
		parkedTmux string
		jobDirOnly bool
		owner      string
		profile    string
	}{
		{name: "parked by this pane", parkedTmux: "agentdeck_pane-a_1234abcd:@8.%8", owner: "inst-pane-a"},
		{name: "parked by this pane, job dir only", parkedTmux: "agentdeck_pane-a_1234abcd:@8.%8", jobDirOnly: true, owner: "inst-pane-a"},
		{name: "parked by another pane", parkedTmux: "agentdeck_pane-b_5678abcd:@2.%3", owner: "inst-pane-b"},
		{name: "parked by another profile", parkedTmux: "agentdeck_pane-b_5678abcd:@2.%3", owner: "inst-pane-b", profile: "parking"},
		{name: "parked by an unowned pane", parkedTmux: "unowned:@2.%3"},
		{name: "not parked anywhere"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claudeDir := setupBgWorkerHookEnv(t)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("AGENTDECK_PROFILE", "starter")
			seedParkedHookInstance(t, "starter", "inst-pane-a", "agentdeck_pane-a_1234abcd")
			profile := tc.profile
			if profile == "" {
				profile = "starter"
			}
			seedParkedHookInstance(t, profile, "inst-pane-b", "agentdeck_pane-b_5678abcd")
			sessionsDir := filepath.Join(claudeDir, "sessions")
			if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			const job = "0a586035"
			if tc.jobDirOnly {
				t.Setenv("CLAUDE_JOB_DIR", filepath.Join(claudeDir, "jobs", job))
			} else {
				pid := os.Getppid()
				rec := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"kind":"bg","jobId":%q}`, pid, bgWorkerTestSession, job)
				if err := os.WriteFile(filepath.Join(sessionsDir, fmt.Sprintf("%d.json", pid)), []byte(rec), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.parkedTmux != "" {
				rec := fmt.Sprintf(`{"pid":1,"kind":"interactive","parkedJobId":%q,"tmux":%q}`, job, tc.parkedTmux)
				if err := os.WriteFile(filepath.Join(sessionsDir, "1.json"), []byte(rec), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runBgWorkerHook(t)
			for _, id := range []string{"inst-pane-a", "inst-pane-b"} {
				data, err := os.ReadFile(filepath.Join(getHooksDir(), id+".json"))
				if got, want := err == nil, id == tc.owner; got != want {
					t.Errorf("%s status written = %v, want %v", id, got, want)
				}
				if err == nil && !strings.Contains(string(data), bgWorkerTestSession) {
					t.Errorf("%s status does not bind the worker conversation: %s", id, data)
				}
			}
			if got, want := lifecycleLogHasBgReject(t), tc.owner == ""; got != want {
				t.Fatalf("bg-worker reject logged = %v, want %v", got, want)
			}
			// The rest of the hook (bindings, inbox, ledger) resolves the
			// owner's profile, not the one the worker inherited.
			if tc.profile != "" {
				if got := os.Getenv("AGENTDECK_PROFILE"); got != tc.profile {
					t.Fatalf("AGENTDECK_PROFILE after routing = %q, want %q", got, tc.profile)
				}
			}
		})
	}
}

func seedParkedHookInstance(t *testing.T, profile, id, tmuxName string) {
	t.Helper()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	if err := storage.GetDB().InsertInstanceRow(&statedb.InstanceRow{
		ID: id, Title: id, Tool: "claude", TmuxSession: tmuxName,
	}); err != nil {
		t.Fatal(err)
	}
}
