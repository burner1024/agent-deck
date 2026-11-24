package session

import (
	"testing"
	"time"
)

func TestFreshStartConfiguredOpenCodeExecutable(t *testing.T) {
	for _, c := range []struct {
		name, configured string
		want             bool
	}{
		{"configured executable", "/opt/bin/opencode-capped", true},
		{"configured arguments", "/opt/bin/opencode-capped --continue", false},
		{"configured shell expression", "/opt/bin/opencode-capped;", false},
		{"configured environment", "OTHER_HOME=/elsewhere /opt/bin/opencode-capped", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := isolatedHomeDir(t)
			writeConfigTOML(t, home, "[opencode]\ncommand = \""+c.configured+"\"\n")
			inst := newGuardInstance(t, home)
			inst.ID = "configured-opencode"
			inst.Tool, inst.Command = "opencode", "opencode"
			inst.buildOpenCodeCommand("opencode")
			noteFreshStart(inst, time.Now())
			if got := hasFreshStartToken(inst.ID); got != c.want {
				t.Fatalf("configured executable token = %v, want %v", got, c.want)
			}
		})
	}
}
