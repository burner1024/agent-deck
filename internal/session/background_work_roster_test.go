package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// An idle prompt whose only live sign of a background agent is the roster row
// drawn under the footer.
const rosterIdleFrame = `✻ Cooked for 2m 18s

──────────────────────────────── work ─
❯
────────────────────────────────────────
  Opus 5.5 high | ctx: 527.8k/1M
  ⏵⏵ bypass permissions on (shift+tab to cycle)

  ● main
  ◯ probe sleeper agent  Checking tidy results                     20m 15s · ↓ 143.4k tokens
`

func TestMergeBackgroundWork_RosterRow(t *testing.T) {
	now := time.Now()
	lines := loadProbeTranscript(t)
	// The agent launched long ago (past the transcript hold) and is still pending.
	pending := scanTranscriptBackground(restamp(lines[:28], now.Add(-time.Hour)))
	// Its terminal task-notification is in the transcript: the row only lingers.
	finished := scanTranscriptBackground(lines)
	row := tmux.BackgroundWork{RosterAgents: 1}

	cases := []struct {
		name     string
		pane     tmux.BackgroundWork
		sc       transcriptBackgroundScan
		scOK     bool
		wantKind string
		wantPane bool
	}{
		{"row, agent pending", row, pending, true, tmux.BackgroundKindAgent, true},
		{"row, agent finished", row, finished, true, "", false},
		{"row, no transcript", row, transcriptBackgroundScan{}, false, "", false},
		{"no row, agent pending past the hold", tmux.BackgroundWork{}, pending, true, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, paneShown := mergeBackgroundWork(c.pane, c.sc, c.scOK, time.Time{}, now)
			if got.Kind != c.wantKind || paneShown != c.wantPane {
				t.Fatalf("merge = %+v paneShown=%v, want kind %q paneShown=%v", got, paneShown, c.wantKind, c.wantPane)
			}
		})
	}
}

// Through the status pipeline: the foreground turn ended (Stop hook, empty
// prompt) and a background agent launched long ago still runs.
func TestBackgroundWork_RosterRowWithPendingAgentIsRunning(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
	inst, cleanup := startHookLagInstance(t, "bg-roster", rosterIdleFrame)
	defer cleanup()
	writeHookLagStopFile(t, inst.ID)
	inst.ClaudeSessionID = "sess-610"
	lines := loadProbeTranscript(t)

	// A pass with no transcript would cache the unresolved path for
	// backgroundTranscriptRetry; the no-transcript case is in the merge table.
	writeInstanceTranscript(t, inst, restamp(lines[:28], time.Now().Add(-time.Hour)))
	writeHookLagStopFile(t, inst.ID)
	status, sub := cliPass(t, inst)
	if status != StatusRunning || sub != SubstateBackgroundWork {
		t.Fatalf("roster row + pending agent = %q/%q, want running/background-work", status, sub)
	}
	if work := inst.BackgroundWork(); work.Kind != tmux.BackgroundKindAgent || work.Task != "probe sleeper agent" {
		t.Fatalf("background work = %+v, want the transcript's agent", work)
	}

	// The agent reported back: the lingering row no longer holds the session.
	writeInstanceTranscript(t, inst, restamp(lines, time.Now().Add(-5*time.Second)))
	writeHookLagStopFile(t, inst.ID)
	if status, _ := cliPass(t, inst); status != StatusWaiting {
		t.Fatalf("roster row after the agent finished = %q, want waiting", status)
	}
}
