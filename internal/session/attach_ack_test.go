package session

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// writeStopFileAt writes a Stop ("waiting") hook event with the given unix
// timestamp, for the hook session startHookLagInstance's hook file bound.
func writeStopFileAt(t *testing.T, instanceID string, ts int64) {
	t.Helper()
	body := fmt.Sprintf(`{"status":"waiting","session_id":"sess-610","event":"Stop","ts":%d}`, ts)
	if err := os.WriteFile(filepath.Join(GetHooksDir(), instanceID+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}
}

// TestAcknowledgeIfWaiting_SeenStopIsIdleForLaterProcesses pins acknowledgement
// across processes. A remote TUI opens a session through `session attach` on
// the remote, and every later poll is a fresh one-pass listing there; an
// acknowledgement held only in the attaching process left a seen session
// waiting for good. The record covers the Stop event that was seen and no
// later one.
func TestAcknowledgeIfWaiting_SeenStopIsIdleForLaterProcesses(t *testing.T) {
	inst, cleanup := startHookLagInstance(t, "ack", auditConductorIdlePane)
	defer cleanup()
	storage, err := NewStorageWithProfile("_test-attach-ack")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	// As stored once a turn started: a row stored idle loads already
	// acknowledged, whatever the hook says.
	inst.Status = StatusRunning
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	load := func() *Instance {
		t.Helper()
		instances, _, err := storage.LoadWithGroups()
		if err != nil || len(instances) != 1 {
			t.Fatalf("load: %v (%d instances)", err, len(instances))
		}
		return instances[0]
	}

	// A running session is not acknowledged by an attach.
	if err := AcknowledgeIfWaiting(storage.GetDB(), load()); err != nil {
		t.Fatalf("acknowledge running: %v", err)
	}
	stop := time.Now().Add(-2 * time.Second).Unix()
	writeStopFileAt(t, inst.ID, stop)
	if status, _ := cliPass(t, load()); status != StatusWaiting {
		t.Fatalf("after Stop, before any attach = %q, want waiting", status)
	}

	if err := AcknowledgeIfWaiting(storage.GetDB(), load()); err != nil {
		t.Fatalf("acknowledge waiting: %v", err)
	}
	for i := range 2 {
		if status, _ := cliPass(t, load()); status != StatusIdle {
			t.Fatalf("listing %d after the attach = %q, want idle", i+1, status)
		}
	}

	// The next turn's Stop is unseen.
	writeStopFileAt(t, inst.ID, stop+1)
	if status, _ := cliPass(t, load()); status != StatusWaiting {
		t.Fatalf("after a later Stop = %q, want waiting", status)
	}
}

// TestAcknowledgeIfWaiting_OpenCodeSeenTurnIsIdle covers a tool with no
// hooks: the turn seen is the latest session update the pane's own OpenCode
// server (the --port it was started with) reports for the project. The bound
// session ID is not trusted for it: only processes running the full status
// update rebind it, so after an in-pane /new it names a conversation that
// never changes again, and a seen turn there would hide every later one.
func TestAcknowledgeIfWaiting_OpenCodeSeenTurnIsIdle(t *testing.T) {
	for _, status := range []Status{StatusRunning, StatusWaiting, StatusIdle} {
		for _, local := range []bool{false, true} {
			t.Run(fmt.Sprintf("saved_%s/local_%t", status, local), func(t *testing.T) {
				testOpenCodeAcknowledgement(t, status, local)
			})
		}
	}
}

func testOpenCodeAcknowledgement(t *testing.T, savedStatus Status, local bool) {
	t.Helper()
	skipIfNoTmuxBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	var mu sync.Mutex
	paneUpdated := 200
	unavailable := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if unavailable {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprintf(w, `[{"id":"ses_old","directory":%q,"time":{"created":50,"updated":100}},`+
			`{"id":"ses_pane","directory":%q,"time":{"created":150,"updated":%d}}]`, tmpHome, tmpHome, paneUpdated)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	panePath := filepath.Join(tmpHome, "pane.txt")
	if err := os.WriteFile(panePath, []byte("  Ask anything...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithTool("opencode-ack", tmpHome, "opencode")
	// Bound before an in-pane /new moved the conversation to ses_pane.
	inst.OpenCodeSessionID = "ses_old"
	// As Instance.Start launches every tool but shell: the command is the
	// pane's initial process, so tmux keeps it as the pane's start command.
	inst.tmuxSession.RunCommandAsInitialProcess = true
	if err := inst.tmuxSession.Start(fmt.Sprintf("sh -c 'cat %q; exec sleep 3600' opencode --port %d", panePath, port)); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	defer func() { _ = inst.tmuxSession.Kill() }()
	inst.Command = "opencode"
	inst.tmuxSession.Command = "opencode"
	// Past UpdateStatus's 1.5s tmux grace window.
	time.Sleep(2 * time.Second)

	storage, err := NewStorageWithProfile("_test-attach-ack-opencode")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	inst.Status = savedStatus
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	load := func() *Instance {
		t.Helper()
		instances, _, err := storage.LoadWithGroups()
		if err != nil || len(instances) != 1 {
			t.Fatalf("load: %v (%d instances)", err, len(instances))
		}
		return instances[0]
	}

	attaching := load()
	var ackErr error
	if local {
		ackErr = attaching.AcknowledgeSeen(storage.GetDB())
	} else {
		// An idle row needs an unseen turn before attach will acknowledge it.
		attaching.tmuxSession.ResetAcknowledged()
		ackErr = AcknowledgeIfWaiting(storage.GetDB(), attaching)
	}
	if ackErr != nil {
		t.Fatalf("acknowledge: %v", ackErr)
	}
	if got := load().ackedOpenCodeUpdated; got != 200 {
		t.Fatalf("stored turn = %d, want 200", got)
	}
	for i := range 2 {
		if status, _ := cliPass(t, load()); status != StatusIdle {
			t.Fatalf("listing %d after the attach = %q, want idle", i+1, status)
		}
	}

	mu.Lock()
	unavailable = true
	mu.Unlock()
	if status, _ := cliPass(t, load()); status != StatusWaiting {
		t.Errorf("unavailable server = %q, want waiting", status)
	}
	if err := load().AcknowledgeSeen(storage.GetDB()); err != nil {
		t.Errorf("acknowledging an unknown turn: %v", err)
	}
	mu.Lock()
	unavailable = false
	mu.Unlock()
	if err := AcknowledgeIfWaiting(storage.GetDB(), load()); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	for i := range 2 {
		if status, _ := cliPass(t, load()); status != StatusIdle {
			t.Fatalf("listing %d after the attach = %q, want idle", i+1, status)
		}
	}

	// The next turn, in the pane's conversation: unseen.
	mu.Lock()
	paneUpdated = 300
	mu.Unlock()
	if status, _ := cliPass(t, load()); status != StatusWaiting {
		t.Fatalf("after a later turn = %q, want waiting", status)
	}
}

func TestAcknowledgeSeen_OpenCodeUnverifiedTurnIsLocal(t *testing.T) {
	for _, withPort := range []bool{false, true} {
		t.Run(fmt.Sprintf("with_port_%t", withPort), func(t *testing.T) {
			skipIfNoTmuxBinary(t)
			project := t.TempDir()
			t.Setenv("HOME", project)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}))
			t.Cleanup(srv.Close)
			panePath := filepath.Join(project, "pane.txt")
			if err := os.WriteFile(panePath, []byte("  Ask anything...\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			inst := NewInstanceWithTool("opencode-local-ack", project, "opencode")
			command := fmt.Sprintf("sh -c 'cat %q; exec sleep 300' opencode", panePath)
			if withPort {
				command += fmt.Sprintf(" --port %d", srv.Listener.Addr().(*net.TCPAddr).Port)
			}
			inst.tmuxSession.RunCommandAsInitialProcess = true
			if err := inst.tmuxSession.Start(command); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = inst.tmuxSession.Kill() })
			inst.Command = "opencode"
			inst.tmuxSession.Command = "opencode"
			time.Sleep(2 * time.Second)
			storage, err := NewStorageWithProfile("_test-opencode-local-ack")
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close()
			inst.Status = StatusWaiting
			if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
				t.Fatal(err)
			}
			if err := inst.AcknowledgeSeen(storage.GetDB()); err != nil {
				t.Errorf("acknowledge unverified turn: %v", err)
			}
			if inst.AcknowledgedAt().IsZero() {
				t.Error("local visit was not recorded")
			}
			if status, _ := cliPass(t, inst); status != StatusIdle {
				t.Errorf("opened instance = %q, want idle", status)
			}
			statuses, err := storage.GetDB().ReadAllStatuses()
			if err != nil {
				t.Fatal(err)
			}
			if statuses[inst.ID].Acknowledged || !statuses[inst.ID].AcknowledgedAt.IsZero() {
				t.Error("unverified acknowledgement was written to storage")
			}
			loaded, _, err := storage.LoadWithGroups()
			if err != nil || len(loaded) != 1 {
				t.Fatalf("load: %v (%d instances)", err, len(loaded))
			}
			if loaded[0].ackedOpenCodeUpdated != 0 {
				t.Error("unverified turn token was written to storage")
			}
			if status, _ := cliPass(t, loaded[0]); status != StatusWaiting {
				t.Errorf("fresh instance = %q, want waiting", status)
			}
		})
	}
}

// TestAcknowledgeIfWaiting_SeenStaleStopIsIdle covers the common case: a
// session left waiting past hookFastPathWindow, where the pane, not the hook,
// decides the status.
func TestAcknowledgeIfWaiting_SeenStaleStopIsIdle(t *testing.T) {
	inst, cleanup := startHookLagInstance(t, "ack-stale", auditConductorIdlePane)
	defer cleanup()
	storage, err := NewStorageWithProfile("_test-attach-ack-stale")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	inst.Status = StatusRunning
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	load := func() *Instance {
		t.Helper()
		instances, _, err := storage.LoadWithGroups()
		if err != nil || len(instances) != 1 {
			t.Fatalf("load: %v (%d instances)", err, len(instances))
		}
		return instances[0]
	}

	writeStopFileAt(t, inst.ID, time.Now().Add(-hookFastPathWindow-time.Minute).Unix())
	if status, _ := cliPass(t, load()); status != StatusWaiting {
		t.Fatalf("stale Stop, before any attach = %q, want waiting", status)
	}
	if err := AcknowledgeIfWaiting(storage.GetDB(), load()); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if status, _ := cliPass(t, load()); status != StatusIdle {
		t.Fatalf("stale Stop, after the attach = %q, want idle", status)
	}
}

func TestAcknowledgeSeen_HookIdentityWithinSecond(t *testing.T) {
	for _, event := range []string{"Stop", "PermissionRequest", "legacy_seconds"} {
		t.Run(event, func(t *testing.T) {
			inst, cleanup := startHookLagInstance(t, "ack-precision", auditConductorIdlePane)
			defer cleanup()
			storage, err := NewStorageWithProfile("_test-ack-precision")
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close()
			inst.Status = StatusRunning
			if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
				t.Fatal(err)
			}
			load := func() *Instance {
				t.Helper()
				instances, _, err := storage.LoadWithGroups()
				if err != nil || len(instances) != 1 {
					t.Fatalf("load: %v (%d instances)", err, len(instances))
				}
				return instances[0]
			}
			// Both events share a second; receive order puts the second after
			// the visit regardless of how long the status pass takes.
			first := time.Unix(time.Now().Add(time.Second).Unix(), 100_000_000)
			writeHook := func(event string, received time.Time) {
				t.Helper()
				body := fmt.Sprintf(`{"status":"waiting","session_id":"sess-610","event":%q,"ts":%d,"received_at_ns":%d}`,
					event, first.Unix(), received.UnixNano())
				if err := os.WriteFile(filepath.Join(GetHooksDir(), inst.ID+".json"), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writeHook("Stop", first)
			attached := load()
			if err := AcknowledgeIfWaiting(storage.GetDB(), attached); err != nil {
				t.Fatal(err)
			}
			if status, _ := cliPass(t, load()); status != StatusIdle {
				t.Fatalf("seen event = %q, want idle", status)
			}
			if event == "legacy_seconds" {
				if err := storage.GetDB().SetAcknowledgedTurn(inst.ID, statedb.ToolDataAcknowledgedHookKey, first.Unix()); err != nil {
					t.Fatal(err)
				}
				event = "Stop"
			}
			writeHook(event, first.Add(100*time.Millisecond))
			if status, _ := cliPass(t, load()); status != StatusWaiting {
				t.Errorf("new event in the same second = %q, want waiting", status)
			}
			if status, _ := cliPass(t, attached); status != StatusWaiting {
				t.Errorf("live event in the same second = %q, want waiting", status)
			}
		})
	}
}
