package main

import (
	"strings"
	"testing"
)

func TestCodexSendWithoutReadableComposer(t *testing.T) {
	const msg = "Please summarize the current changes and their validation."
	baseline := issue2424CodexPane(nil, "")
	for _, tc := range []struct {
		name  string
		frame string
		want  string
	}{
		{
			name:  "submitted user turn and reply",
			frame: strings.Join(append(issue2424CodexCell(msg), "", "\u2022 Understood. I will summarize the changes."), "\n"),
			want:  deliverySubmitted,
		},
		{
			name:  "arrival without submission evidence",
			frame: "  " + msg + "\n",
			want:  deliveryDelivered,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockSendRetryTarget{
				statuses: []string{"waiting"},
				panes:    []string{baseline, baseline, tc.frame},
			}
			res, err := executeSend(mock, "codex", msg, true, sendExecTuning{retry: noWaitSendOptionsNoDelay()})
			if err != nil || res.delivery != tc.want {
				t.Fatalf("delivery = %q, err = %v, want %q without error", res.delivery, err, tc.want)
			}
		})
	}
}
