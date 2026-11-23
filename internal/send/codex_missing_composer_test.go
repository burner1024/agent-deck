package send

import "testing"

func TestCodexSubmissionWithoutComposer(t *testing.T) {
	const msg = "Please summarize the current changes and their validation."
	baseline := codexFrame(lines("• Ready."), codexIdleComposer)
	for _, tc := range []struct {
		name string
		tail []string
		want Outcome
	}{
		{"reply started", lines("", "• Understood. I will summarize the changes."), OutcomeConfirmed},
		{"turn started", codexWorking("1s"), OutcomeConfirmed},
		{"text still in composer", lines("", codexFooter), OutcomeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := NewObserver("codex", msg, Captured(baseline))
			o.CodexLike = true
			frame := codexFrame(renderCodexCell(msg), tc.tail)
			v := replay(t, o, Captured(frame))
			if v.Outcome != tc.want {
				t.Fatalf("outcome = %q (%s), want %q", v.Outcome, v.Message, tc.want)
			}
		})
	}
}
