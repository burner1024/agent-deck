package session

import (
	"testing"
	"time"
)

func TestCodexWaitingBackgroundDoesNotRepeatTurn(t *testing.T) {
	f := newCodexTurnFixture(t, "codex")
	now := time.Now()
	f.pass(t, "running", now)
	f.writeHook(t, "turn-a", now)
	f.pass(t, "waiting", now.Add(time.Second))
	if got := f.records(t); got != 1 {
		t.Fatalf("completed turn: %d records, want 1", got)
	}
	content := "\u2022 Waiting for background terminal (42m 23s \u2022 esc to interrupt)\n\n\u203a Ask Codex to do anything\n"
	f.d.codexComposerIdle = func(inst *Instance) bool { return codexContentAtComposer(inst.Tool, content) }
	for i := 1; i <= 3; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		f.pass(t, "running", at)
		f.pass(t, "waiting", at.Add(time.Second))
		f.pass(t, "waiting", at.Add(20*time.Second))
		if got := f.records(t); got != 1 {
			t.Errorf("background pane flip %d: %d records, want 1", i, got)
		}
	}
	// A real completion whose notify was dropped must still wake the parent.
	content = "\u2022 Done.\n\n\u203a Ask Codex to do anything\n"
	f.pass(t, "waiting", now.Add(4*time.Minute))
	if got := f.records(t); got != 2 {
		t.Fatalf("completed background work with dropped notify: %d records, want 2", got)
	}
	f.pass(t, "waiting", now.Add(5*time.Minute))
	if got := f.records(t); got != 2 {
		t.Fatalf("completed background work replayed: %d records, want 2", got)
	}
}

func TestCodexWaitingWithoutCompletionStaysPending(t *testing.T) {
	f := newCodexTurnFixture(t, "codex")
	f.idle = false
	now := time.Now()
	f.pass(t, "running", now)
	f.pass(t, "waiting", now.Add(time.Second))
	if got := f.records(t); got != 0 {
		t.Fatalf("running turn without completion evidence: %d records, want 0", got)
	}
	f.writeHook(t, "turn-a", now.Add(2*time.Second))
	f.pass(t, "waiting", now.Add(3*time.Second))
	if got := f.records(t); got != 1 {
		t.Fatalf("new completion before pane settles: %d records, want 1", got)
	}
}

func TestCodexWaitingInteractiveInputStillDelivered(t *testing.T) {
	f := newCodexTurnFixture(t, "codex")
	f.idle = false
	old := turnCachedSubstate
	turnCachedSubstate = func(*Instance) Substate { return SubstateInteractiveMenu }
	t.Cleanup(func() { turnCachedSubstate = old })
	now := time.Now()
	f.pass(t, "running", now)
	f.pass(t, "waiting", now.Add(time.Second))
	if got := f.records(t); got != 1 {
		t.Fatalf("open interactive input: %d records, want 1", got)
	}
	if !InboxHasUrgentPending(f.parent.ID) {
		t.Fatal("open interactive input did not produce urgent news")
	}
}
