package send

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexStartupPendingVerdict(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	baseline := read("codex-resuming-empty.txt")
	pending := read("codex-startup-pending-later.txt")
	const message = "Do not run any command or edit any file. Reply with the single word pong6."
	for _, tc := range []struct {
		name  string
		frame string
		busy  bool
		want  Outcome
	}{
		{"startup still pending", pending, false, OutcomeDeliveredUnconfirmed},
		{"startup pending after a running turn", pending, true, OutcomeDeliveredUnconfirmed},
		{"retained body without startup acknowledgement", strings.Replace(pending, "  Waiting for startup  \u00b7 esc cancel\n", "", 1), false, OutcomeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := NewObserver("codex", message, Captured(baseline))
			o.CodexLike, o.BusyBeforeSend = true, tc.busy
			o.Observe(Captured(tc.frame))
			v := o.Verdict(10, "2s")
			if v.Outcome != tc.want {
				t.Fatalf("outcome = %q (%s), want %q", v.Outcome, v.Message, tc.want)
			}
		})
	}
}
