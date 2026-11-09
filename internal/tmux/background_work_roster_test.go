package tmux

import "testing"

// rosterFrame is an idle Claude frame (empty prompt) whose only sign of work
// is the agent roster drawn under the footer.
func rosterFrame(footer, roster string) string {
	return "✻ Cooked for 2m 18s\n\n" +
		"──────────────────────────────── work ─\n❯ \n────────────────────────────────\n" +
		"  Opus 5.5 high | ctx: 527.8k/1M\n" + footer + "\n\n" + roster
}

const rosterFooter = "  ⏵⏵ bypass permissions on (shift+tab to cycle)"

func TestClaudeRosterAgentRows(t *testing.T) {
	cases := []struct {
		name   string
		footer string
		roster string
		want   int
	}{
		{"running agent with tokens", rosterFooter, "  ● main\n  ◯ code-worker  Checking tidy results                     20m 15s · ↓ 143.4k tokens", 1},
		{"running agent without tokens", rosterFooter, "  ⏺ main\n  ◯ Explore  Map the module graph          15s", 1},
		{"two agents, one row without elapsed", rosterFooter, "  ◯ general-purpose  Task 4\n  ◯ Explore  Map hooks     12s · ↑ 3.0k tokens\n  ◯ Explore  Map more     1h 2m", 2},
		{"beside a monitor counter", "  ⏵⏵ bypass permissions on · 1 monitor · ← for agents", "  ◯ Explore  Map hooks     12s · ↓ 3.0k tokens", 1},
		{"idle roster, no elapsed", rosterFooter, "  ⏺ main\n  ◯ general-purpose  Task 1: review slice 1", 0},
		{"main row only", rosterFooter, "  ● main", 0},
		{"workflow row is not an agent", rosterFooter, "  ◯ probe-two-agents  ▰▰▰▰▰▰▰▰▰▰  2/2 · 1m44s · ↓ 74.4k tokens", 0},
		{"no footer", "  some status text", "  ◯ Explore  Map hooks     12s · ↓ 3.0k tokens", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClaudeRosterAgentRows(rosterFrame(c.footer, c.roster)); got != c.want {
				t.Fatalf("ClaudeRosterAgentRows = %d, want %d", got, c.want)
			}
		})
	}
}

func TestClaudeRosterRowElapsed(t *testing.T) {
	cases := []struct{ row, want string }{
		{"◯ code-worker  Checking tidy results     20m 15s · ↓ 143.4k tokens", "20m15s"},
		{"◯ Explore  Map hooks     12s", "12s"},
		{"◯ Explore  Map hooks 5s later     12s · ↑ 3.0k tokens", "12s"},
		{"◯ Explore  Task 4", ""},
	}
	for _, c := range cases {
		got, ok := claudeRosterRowElapsed(c.row)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("claudeRosterRowElapsed(%q) = %q, %v; want %q", c.row, got, ok, c.want)
		}
	}
}

// Roster-shaped text above the input box is scrollback, not the live roster.
func TestClaudeRosterAgentRows_ScrollbackIgnored(t *testing.T) {
	frame := "  ◯ Explore  Map hooks     12s · ↓ 3.0k tokens\n\n" +
		"────────────────\n❯ \n────────────────\n" + rosterFooter + "\n"
	if got := ClaudeRosterAgentRows(frame); got != 0 {
		t.Fatalf("scrollback row counted: %d", got)
	}
}

// The roster never makes the pane item in flight: a finished agent's row
// lingers, so only the session layer (with the transcript) may act on it.
func TestClaudePaneBackgroundWork_RosterIsNotInFlight(t *testing.T) {
	frame := rosterFrame(rosterFooter, "  ● main\n  ◯ code-worker  Checking tidy results     20m 15s · ↓ 143.4k tokens")
	got := claudePaneBackgroundWork(frame)
	if got.RosterAgents != 1 || got.InFlight() {
		t.Fatalf("pane work = %+v, want RosterAgents 1 and nothing in flight", got)
	}
	if sub, _, _ := frameSubstate(frame); sub != SubstateIdleAtEmptyPrompt {
		t.Fatalf("substate = %q, want idle-at-empty-prompt (unchanged)", sub)
	}
}
