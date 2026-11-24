package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexStartupSendVerification(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "send", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	const message = "Do not run any command or edit any file. Reply with the single word pong4."
	empty := read("codex-resuming-empty.txt")
	pending := read("codex-startup-pending.txt")
	submitted := read("codex-startup-submitted.txt")
	for _, tc := range []struct {
		name            string
		retries         int
		frames          []string
		turnAfterBudget bool
		want            string
	}{
		{"startup outlasts the arrival cap", 30, append(repeatFrame(pending, arrivalVerifyChecks+1), submitted), false, deliverySubmitted},
		{"last capture sees submission", arrivalVerifyChecks, append(repeatFrame(pending, arrivalVerifyChecks), submitted), false, deliverySubmitted},
		{"startup still pending at the full budget", 30, []string{pending}, false, deliveryDelivered},
		{"last transcript read sees submission", arrivalVerifyChecks, []string{strings.Replace(pending, "  Waiting for startup  \u00b7 esc cancel\n", "", 1)}, true, deliverySubmitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := &mockSendRetryTarget{statuses: []string{"waiting"}, panes: append([]string{empty}, tc.frames...)}
			baseline := captureArrivalBaseline(target, message)
			opts := noWaitSendOptionsNoDelay()
			opts.tool, opts.maxRetries = "codex", tc.retries
			turnReads := 0
			if tc.turnAfterBudget {
				opts.turnAdvanced = func() bool {
					turnReads++
					return turnReads > arrivalVerifyChecks
				}
			}
			delivery, err := verifyContentArrival(target, message, opts, baseline, false)
			if err != nil || delivery != tc.want {
				t.Fatalf("delivery = %q, err = %v, want %q without error", delivery, err, tc.want)
			}
		})
	}
}

func repeatFrame(frame string, count int) []string {
	frames := make([]string, count)
	for i := range frames {
		frames[i] = frame
	}
	return frames
}

func TestSendWithRetryTarget_FinalTurnAdvance(t *testing.T) {
	const message = "Report the current validation result."
	target := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"\u276f \n", "\u276f " + message + "\n"},
	}
	reads := 0
	opts := sendRetryOptions{tool: "claude", maxRetries: 1, verifyDelivery: true}
	opts.turnAdvanced = func() bool { reads++; return reads > 1 }
	delivery, err := sendWithRetryTarget(target, message, false, opts)
	if err != nil || delivery != deliverySubmitted {
		t.Fatalf("delivery = %q, err = %v, want submitted without error", delivery, err)
	}
}

func TestCodexStartupDoesNotRetryAcceptedEnter(t *testing.T) {
	t.Setenv("AGENTDECK_CODEX_SUBMIT_RETRY", "1")
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "send", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	const message = "Do not run any command or edit any file. Reply with the single word pong4."
	pending := read("codex-startup-pending.txt")
	target := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{read("codex-resuming-empty.txt"), pending, pending, read("codex-startup-submitted.txt")},
	}
	baseline := captureArrivalBaseline(target, message)
	opts := noWaitSendOptionsNoDelay()
	opts.tool, opts.maxRetries = "codex", 3
	delivery, err := verifyContentArrival(target, message, opts, baseline, false)
	if err != nil || delivery != deliverySubmitted {
		t.Fatalf("delivery = %q, err = %v, want submitted without error", delivery, err)
	}
	if target.sendEnterCalls != 0 {
		t.Fatalf("startup already acknowledged Enter; got %d retries", target.sendEnterCalls)
	}
}
