package send

import "testing"

func TestObserverBusyComposerShortTextKeepsWordBoundaries(t *testing.T) {
	const message = "a b"
	o := NewObserver("claude", message, Captured(claudePane("", "")))
	o.ClaudeLike, o.BusyBeforeSend = true, true
	o.Observe(Captured(claudePane("", message)))
	o.Observe(Captured(claudePane("", "cab")))
	v := o.Verdict(2, "6s")
	if v.Outcome != OutcomeDeliveredUnconfirmed || v.Confirmation != ConfirmationUnknown {
		t.Fatalf("different text in the final composer: verdict = %+v, want delivered-unconfirmed", v)
	}
}
