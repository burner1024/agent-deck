package session

import (
	"strings"
	"testing"
	"time"
)

func TestFreshStartCodexEmbeddedKickoffToken(t *testing.T) {
	home := isolatedHomeDir(t)
	inst := NewInstanceWithTool("fresh-codex", home, "codex")
	inst.Command = "codex"
	inst.Wrapper = "{command} --dangerously-bypass-approvals-and-sandbox"
	command, embedded := inst.buildCodexCommandWithPrompt(inst.Command, "reply pong")
	if !embedded || !strings.HasSuffix(command, " 'reply pong'") {
		t.Fatalf("kickoff was not embedded: %q, %v", command, embedded)
	}
	noteFreshStart(inst, time.Now())
	if tok, ok := readFreshStartToken(inst.ID); !ok || tok.Codex == nil {
		t.Fatalf("embedded kickoff has no Codex token: %+v, %v", tok, ok)
	}
}

func TestFreshStartCodexEmbeddedKickoffBoot(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	st.substate = string(SubstateIdleAtEmptyPrompt)
	f.child.Tool = "codex"
	f.child.Command = "codex"
	f.child.Wrapper = "{command} --dangerously-bypass-approvals-and-sandbox"
	f.child.ClaudeSessionID = ""
	_, embedded := f.child.buildCodexCommandWithPrompt(f.child.Command, "reply pong")
	if !embedded {
		t.Fatal("kickoff was not embedded")
	}
	noteFreshStart(f.child, time.Now())
	f.observeAllPaths(f.status("waiting"))
	f.observeAllPaths(f.status("idle"))
	if got := f.pending(t); strings.Join(got, "\n") != freshStartLine {
		t.Fatalf("boot records = %q, want %q", got, freshStartLine)
	}
	if *f.sends != 0 || InboxHasUrgentPending(f.parent.ID) {
		t.Fatalf("boot woke parent: sends=%d", *f.sends)
	}
	if !f.freshTokenLeft() {
		t.Fatal("boot spent the kickoff token")
	}
	writeHookFileForTest(t, f.child.ID, `{"status":"waiting","event":"agent-turn-complete","codex_completed_sequence":1,"codex_completed_generation":"kickoff"}`)
	f.observeAllPaths(f.status("running"))
	f.observeAllPaths(f.status("waiting"))
	if f.freshTokenLeft() {
		t.Fatal("kickoff completion did not spend token")
	}
	if !InboxHasUrgentPending(f.parent.ID) {
		t.Fatalf("kickoff completion did not reach parent: %q", f.pending(t))
	}
}

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
		{name: "embedded prompt", command: "codex", prompt: "reply pong", want: true},
		{name: "custom positional prompt", command: "codex 'reply pong'"},
		{name: "custom resume", command: "codex resume --last"},
		{name: "custom fork", command: "codex fork --last"},
		{name: "wrapper", command: "codex", setup: func(i *Instance) { i.Wrapper = "env {command}" }},
		// `launch -cmd 'codex <switches>'` keeps the switches as a wrapper after the command.
		{name: "switch wrapper", command: "codex", setup: func(i *Instance) {
			i.Wrapper = "{command} --dangerously-bypass-approvals-and-sandbox"
		}, want: true},
		{name: "two switch wrapper", command: "codex", setup: func(i *Instance) { i.Wrapper = "{command} --yolo --full-auto" }, want: true},
		{name: "prompt wrapper", command: "codex", setup: func(i *Instance) { i.Wrapper = "{command} 'reply pong'" }},
		{name: "resume wrapper", command: "codex", setup: func(i *Instance) { i.Wrapper = "{command} resume --last" }},
		{name: "glued wrapper", command: "codex", setup: func(i *Instance) { i.Wrapper = "{command}--yolo" }},
		{name: "switch wrapper over SSH", command: "codex", setup: func(i *Instance) {
			i.Wrapper = "{command} --yolo"
			i.SSHHost = "build-box"
		}},
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
