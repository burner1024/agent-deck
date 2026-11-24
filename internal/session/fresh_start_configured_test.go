package session

import (
	"testing"
	"time"
)

func TestFreshStartConfiguredExecutable(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "bound"}[resume], func(t *testing.T) {
			home := isolatedHomeDir(t)
			installClaudeHooks(t, home)
			writeConfigTOML(t, home, "[claude]\ncommand = \"/opt/bin/claude-capped\"\n")
			inst := newGuardInstance(t, home)
			inst.ID = "configured-fresh"
			if resume {
				inst.ClaudeSessionID = otherConversation
				inst.buildClaudeResumeCommand()
			} else {
				inst.buildClaudeCommandWithMessage("claude", "Reply with the single word ok.")
			}
			noteFreshStart(inst, time.Now())
			if !hasFreshStartToken(inst.ID) {
				t.Fatal("configured executable has no fresh-start token")
			}
		})
	}
}

func TestFreshStartCompletionAfterUnsignalledBoot(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	st.substate = string(SubstateIdleAtEmptyPrompt)
	at := time.Now()
	f.d.emitTurn("default", f.child, f.byID, "running", "idle", at, true)
	if got := len(f.inboxRecords(t)); got != 1 {
		t.Fatalf("boot records = %d, want 1", got)
	}
	// The first real completion follows SessionStart with turn_seq 2.
	// Its terminal pane can be idle, just like the earlier boot.
	st.hook = &HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: at.Add(6 * time.Second), TurnSeq: 2}
	f.turnSeq(t, 2)
	f.appendTurn(t, fxHuman("u1", "Reply with the single word ok."), fxAssistantText("a1", "ok"))
	f.bootSeen()
	f.d.emitTurn("default", f.child, f.byID, "running", "idle", at.Add(6*time.Second), true)
	if got := f.pending(t); len(got) != 2 || got[1] != "info: ok" {
		t.Fatalf("completion records = %q, want boot and one reply", got)
	}
	f.observeAllPaths(f.idle())
	if got := len(f.inboxRecords(t)); got != 2 {
		t.Fatalf("repeated completion records = %d, want 2", got)
	}
	f.appendTurn(t, fxHuman("u2", "Reply with the single word ok."), fxAssistantText("a2", "ok"))
	f.turnSeq(t, 4)
	st.hook.TurnSeq = 4
	f.bootSeen()
	f.d.emitTurn("default", f.child, f.byID, "running", "idle", at.Add(12*time.Second), true)
	f.observeAllPaths(f.idle())
	if got := f.pending(t); len(got) != 3 || got[2] != "info: ok" {
		t.Fatalf("second completion records = %q, want one record per reply", got)
	}
}
