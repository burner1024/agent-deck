package tmux

import "testing"

// These pulse frames come from the 1.19.5 footer during a shell tool call.
// Its dots and squares differ from the older shaded-block pulse.
func TestOpenCodeCapturedPulseFooter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame string
		busy  bool
	}{
		{"dots", "   \u2b1d\u2b1d\u2b1d\u2b1d\u2b1d\u2b1d\u2b1d\u2b1d  esc interrupt   tab agents  ctrl+p commands    \u2022 OpenCode 1.19.5\n", true},
		{"mixed", "   \u25a0\u25a0\u25a0\u25a0\u2b1d\u2b1d\u2b1d\u2b1d  esc interrupt   tab agents  ctrl+p commands    \u2022 OpenCode 1.19.5\n", true},
		{"squares", "   \u25a0\u25a0\u25a0\u25a0\u25a0\u25a0\u25a0\u25a0  esc interrupt   tab agents  ctrl+p commands    \u2022 OpenCode 1.19.5\n", true},
		{"idle", "tab agents  ctrl+p commands    \u2022 OpenCode 1.19.5\n", false},
		{"quoted", "Example: \u25a0\u25a0\u25a0\u25a0\u2b1d\u2b1d\u2b1d\u2b1d esc interrupt was displayed\n", false},
		{"static progress", "\u25a0\u25a0\u25a0\u25a0\u2b1d\u2b1d\u2b1d\u2b1d  enter submit  esc dismiss\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{Command: "opencode"}
			if got := s.hasBusyIndicator(tc.frame); got != tc.busy {
				t.Fatalf("busy=%v, want %v for %q", got, tc.busy, tc.frame)
			}
		})
	}
}
