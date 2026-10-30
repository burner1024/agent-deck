package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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
