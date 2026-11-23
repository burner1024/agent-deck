package send

import (
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestObserverBusyComposerRetainsBody(t *testing.T) {
	raw, err := os.ReadFile("testdata/busy-tool-running-2.1.296.ansi")
	if err != nil {
		t.Fatal(err)
	}
	baseline := string(raw)
	const prompt = "\u276f\u00a0\x1b[39m"
	if strings.Count(baseline, prompt) != 1 {
		t.Fatal("fixture must have exactly one empty composer prompt")
	}
	frame := func(body string) string {
		return strings.Replace(baseline, prompt, prompt+body, 1)
	}
	for _, tc := range []struct {
		name, message, body string
	}{
		{
			name:    "wrapped text with a prompt glyph in the body",
			message: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ\n\n\u276f inspect the result",
			body:    "abcdefghijklmnopqrstuvwxyz\n  ABCDEFGHIJKLMNOPQRSTUVWXYZ\n\n  \u276f inspect the result",
		},
		{
			name:    "paste marker after a blank row",
			message: "OK\n\nplease inspect the result and report the failure",
			body:    "OK\n\n  [Pasted text #1 +3 lines]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			final := frame(tc.body)
			body, ok := ClaudeComposerFullBody(final, tmux.StripANSI)
			if !ok || FullBodyEmpty(body) {
				t.Fatal("the pre-send full-body check must find an occupied composer")
			}
			o := NewObserver("claude", tc.message, Captured(baseline))
			o.ClaudeLike, o.BusyBeforeSend = true, true
			// The typed body can be observed before the paste collapses.
			o.Observe(Captured(frame(tc.message)))
			o.Observe(Captured(final))
			v := o.Verdict(2, "6s")
			if v.Outcome != OutcomeFailed || v.Failure != FailureComposerHolds || v.Confirmation != ConfirmationFailed {
				t.Fatalf("verdict = %+v, want failed/composer_holds_body", v)
			}
		})
	}
}
