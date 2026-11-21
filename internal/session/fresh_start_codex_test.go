package session

import (
	"strings"
	"testing"
	"time"
)

func TestFreshStartCodexBoot(t *testing.T) {
	for _, c := range []struct {
		name   string
		before string
		hook   string
		run    bool
		age    time.Duration
		want   string
		left   bool
	}{
		{name: "empty prompt without hooks", want: freshStartLine, left: true},
		{name: "unchanged old completion", before: `{"status":"waiting","event":"agent-turn-complete","ts":1,"codex_started_sequence":3,"codex_completed_sequence":3,"codex_completed_generation":"old"}`, want: freshStartLine, left: true},
		{name: "start sequence changed", hook: `{"status":"running","codex_started_sequence":4}`, want: "urgent: "},
		{name: "completion sequence changed", hook: `{"status":"waiting","codex_completed_sequence":4}`, want: "urgent: "},
		{name: "sequence reset", before: `{"status":"waiting","codex_started_sequence":3}`, hook: `{"status":"waiting","codex_started_sequence":0}`, want: "urgent: "},
		{name: "start generation changed", hook: `{"status":"running","codex_started_generation":"next"}`, want: "urgent: "},
		{name: "legacy completion generation changed", hook: `{"status":"waiting","event":"agent-turn-complete","codex_completed_generation":"next"}`, want: "urgent: "},
		{name: "generic hook noise", hook: `{"status":"waiting","sequence":99}`, want: freshStartLine, left: true},
		{name: "kickoff notify dropped", run: true, want: "urgent: "},
		{name: "boot timeout", age: 3 * time.Minute, want: "urgent: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			st.substate = string(SubstateIdleAtEmptyPrompt)
			f.child.Tool = "codex"
			f.child.ClaudeSessionID = ""
			at := time.Now().Add(-c.age)
			if c.before != "" {
				writeHookFileForTest(t, f.child.ID, c.before)
			}
			f.child.buildCodexCommand("codex --dangerously-bypass-approvals-and-sandbox")
			noteFreshStart(f.child, at)
			if c.hook != "" {
				writeHookFileForTest(t, f.child.ID, c.hook)
			}
			if c.run {
				f.d.observeCodexRuns("default", f.byID, f.status("running"), time.Now())
				f.d.observeCodexRuns("default", f.byID, f.status("waiting"), time.Now())
			}
			f.observeAllPaths(f.status("waiting"))
			f.observeAllPaths(f.status("waiting"))
			if got := f.pending(t); strings.Join(got, "\n") != c.want {
				t.Fatalf("records = %q, want %q", got, c.want)
			}
			if got := f.freshTokenLeft(); got != c.left {
				t.Fatalf("token left = %v, want %v", got, c.left)
			}
			if c.left && (*f.sends != 0 || InboxHasUrgentPending(f.parent.ID)) {
				t.Fatalf("boot woke parent: sends=%d", *f.sends)
			}
		})
	}
}

func TestFreshStartCodexCommandGate(t *testing.T) {
	for _, c := range []struct {
		name    string
		command string
		prompt  string
		setup   func(*Instance)
		want    bool
	}{
		{name: "bare", command: "codex", want: true},
		{name: "custom passthrough", command: "codex --dangerously-bypass-approvals-and-sandbox", want: true},
		{name: "path", command: "/opt/tools/codex --yolo", want: true},
		{name: "embedded prompt", command: "codex", prompt: "reply pong"},
		{name: "custom positional prompt", command: "codex 'reply pong'"},
		{name: "custom resume", command: "codex resume --last"},
		{name: "custom fork", command: "codex fork --last"},
		{name: "wrapper", command: "codex", setup: func(i *Instance) { i.Wrapper = "env {command}" }},
		{name: "SSH", command: "codex", setup: func(i *Instance) { i.SSHHost = "build-box" }},
		{name: "sandbox", command: "codex", setup: func(i *Instance) { i.Sandbox = &SandboxConfig{Enabled: true} }},
		{name: "bound conversation", command: "codex --yolo", setup: func(i *Instance) { i.CodexSessionID = otherConversation }},
		{name: "fork", command: "codex", setup: func(i *Instance) { i.IsForkAwaitingStart = true }},
		{name: "alias", command: "cdw"},
		{name: "inline env", command: "CODEX_HOME=/elsewhere codex"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := isolatedHomeDir(t)
			inst := NewInstanceWithTool("fresh-codex", home, "codex")
			if c.setup != nil {
				c.setup(inst)
			}
			inst.buildCodexCommandWithPrompt(c.command, c.prompt)
			noteFreshStart(inst, time.Now())
			if got := hasFreshStartToken(inst.ID); got != c.want {
				t.Fatalf("token = %v, want %v", got, c.want)
			}
			noteFreshStart(inst, time.Now())
			if hasFreshStartToken(inst.ID) {
				t.Fatal("fresh flag was not consumed")
			}
		})
	}
}
