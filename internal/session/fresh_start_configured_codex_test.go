package session

import (
	"testing"
	"time"
)

func TestFreshStartConfiguredCodexExecutable(t *testing.T) {
	for _, c := range []struct {
		name, configured, command string
		want                      bool
	}{
		{"configured executable", "/opt/bin/codex-capped", "codex", true},
		{"configured executable with a known switch", "/opt/bin/codex-capped", "/opt/bin/codex-capped --yolo", true},
		{"other executable", "/opt/bin/codex-capped", "/opt/bin/other", false},
		{"resume", "/opt/bin/codex-capped", "/opt/bin/codex-capped resume --last", false},
		{"configured shell expression", "/opt/bin/codex-capped;", "codex", false},
		{"configured environment", "CODEX_HOME=/elsewhere /opt/bin/codex-capped", "codex", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := isolatedHomeDir(t)
			writeConfigTOML(t, home, "[codex]\ncommand = \""+c.configured+"\"\n")
			inst := newGuardInstance(t, home)
			inst.ID = "configured-codex"
			inst.Tool, inst.Command = "codex", c.command
			inst.buildCodexCommandWithPrompt(c.command, "Reply with the single word ok.")
			noteFreshStart(inst, time.Now())
			if got := hasFreshStartToken(inst.ID); got != c.want {
				t.Fatalf("configured executable token = %v, want %v", got, c.want)
			}
		})
	}
}
