package session

import (
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// The captured 1.19.5 stream ends with a user-summary update after idle.
// Message identifiers and timestamps are normalized; event order is intact.
func TestOpenCodeCapturedCompletionOrder(t *testing.T) {
	f := newTurnTestFixture(t)
	f.child.Tool = "opencode"
	f.child.ClaudeSessionID = ""
	data, err := os.ReadFile("testdata/opencode-1.19.5-turn.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan string, 32)
	_, port := sseTestServer(t, `{}`, events)
	w := NewOpenCodeSSEWatcher(nil)
	w.Sync([]SSETarget{{InstanceID: f.child.ID, Port: port}})
	t.Cleanup(w.Stop)
	waitOpenCodeDaemon(t, func() bool { return w.writes.Load() > 0 })
	send := func(event string) { events <- event }
	for _, event := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		sendAndSettle(t, w, send, event)
	}
	obs := readOpenCodeTurn(f.child.ID)
	if !obs.Ready {
		t.Error("observed real turn did not establish readiness")
	}
	if obs.Running {
		t.Error("late user summary restarted a completed turn")
	}
	if obs.TurnID != "msg_reply" || obs.CompletedID != "msg_reply" {
		t.Errorf("completion identity regressed: turn=%q completed=%q", obs.TurnID, obs.CompletedID)
	}
	f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
	if got := f.pending(t); len(got) != 1 {
		t.Fatalf("completion records = %q, want one", got)
	}
}

func TestOpenCodeInitialObservationGrace(t *testing.T) {
	t.Run("stale running is bounded", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newTurnTestFixture(t)
			f.child.Tool = "opencode"
			f.child.ClaudeSessionID = ""
			obs := openCodeTurnObservation{Running: true, Ran: true, TurnID: "msg_reply", CompletedID: "msg_reply", ObservedAt: time.Now()}
			writeTokenFile(openCodeTurnDir, f.child.ID, ".json", obs)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
			if got := f.pending(t); len(got) != 0 {
				t.Fatalf("first contradictory observation emitted: %q", got)
			}
			time.Sleep(5 * time.Second)
			// Heartbeats must not extend the terminal hold.
			obs.ObservedAt = time.Now()
			writeTokenFile(openCodeTurnDir, f.child.ID, ".json", obs)
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
			if got := f.pending(t); len(got) != 1 {
				t.Fatalf("stale running swallowed completion: %q", got)
			}
		})
	})
	t.Run("before subscription", func(t *testing.T) {
		reviewTestHome(t, "default")
		i := &Instance{ID: "starting", Tool: "opencode"}
		now := time.Now()
		tok := freshStartToken{At: now, OpenCode: &freshStartOpenCodeTurn{}}
		writeTokenFile(freshStartDir, i.ID, ".json", tok)
		if _, got := judgeFreshStart(i, "idle", now, string(SubstateIdleAtEmptyPrompt)); got != freshStartNotYet {
			t.Fatalf("before first observation: verdict=%v, want pending", got)
		}
	})
}

func TestOpenCodeDisabledSSETurns(t *testing.T) {
	f := newTurnTestFixture(t)
	f.child.Tool = "opencode"
	f.child.ClaudeSessionID = ""
	// With no stream, each observed running interval is distinct evidence.
	// The first idle may be boot, but it must not consume the real turn end.
	f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
	for turn := 1; turn <= 2; turn++ {
		f.d.recordTerminalTurns("default", f.byID, map[string]string{f.child.ID: "running"}, nil)
		f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
		if got := f.pending(t); len(got) != turn+1 {
			t.Fatalf("turn %d: records=%q, want boot plus each completed turn", turn, got)
		}
	}
}
