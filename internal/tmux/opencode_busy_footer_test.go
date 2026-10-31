package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenCodeBusyFooter covers OpenCode 1.19's working footer: the pulse
// (■ and ⬝ frames) and "esc interrupt" share the bottom row with the key hints
// and the version, so the bare-hint match misses it and a running session read
// as waiting. The frames are captures of a real OpenCode 1.19.5 TUI streaming a
// reply (paths and dates replaced); the ready and done frames carry no pulse.
func TestOpenCodeBusyFooter(t *testing.T) {
	for _, tc := range []struct {
		file string
		busy bool
	}{
		{"opencode-1.19-working.txt", true},
		{"opencode-1.19-working-dim.txt", true},
		{"opencode-1.19-ready.txt", false},
		{"opencode-1.19-done.txt", false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "pane_status", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			s := NewSession("opencode-footer", t.TempDir())
			s.Command = "opencode --port 4567"
			if got := s.hasBusyIndicator(string(data)); got != tc.busy {
				t.Fatalf("busy=%v, want %v", got, tc.busy)
			}
		})
	}
}
