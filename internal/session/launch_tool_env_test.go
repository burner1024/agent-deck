package session

import (
	"os/exec"
	"strings"
	"testing"
)

func TestLaunchToolEnvOverridesInherited(t *testing.T) {
	probe := `sh -c 'printf "%s" "$AGENTDECK_TOOL"'`
	cases := []struct {
		name, tool, inherited string
		build                 func(*Instance) string
	}{
		{"claude", "claude", "codex", func(i *Instance) string { return i.buildClaudeCommand(probe) }},
		{"gemini", "gemini", "codex", func(i *Instance) string { return i.buildGeminiCommand(probe) }},
		{"codex", "codex", "claude", func(i *Instance) string { return i.buildCodexCommand(probe) }},
		{"fork_prefix", "claude", "codex", func(i *Instance) string { return i.buildBashExportPrefix(false) + probe }},
		{"custom", "custom tool's", "codex", func(i *Instance) string { return i.buildGenericCommand(probe) }},
		{"raw", "custom", "codex", func(i *Instance) string {
			cmd, _, err := i.prepareCommand(probe)
			if err != nil {
				t.Fatal(err)
			}
			return cmd
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project := sessionIDInjectionEnv(t)
			t.Setenv("AGENTDECK_TOOL", tc.inherited)
			i := NewInstanceWithTool("tool env", project, tc.tool)
			cmd := exec.Command("bash", "-c", tc.build(i))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("launch: %v: %s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.tool {
				t.Fatalf("tool = %q, want %q", got, tc.tool)
			}
		})
	}
}

func TestLaunchToolEnvShellPassthrough(t *testing.T) {
	i := NewInstanceWithTool("subcommand", sessionIDInjectionEnv(t), "shell")
	i.Command = "codex mcp list"
	i.SubcommandPassthrough = true
	if cmd := i.buildShellPassthroughCommand(i.Command); !strings.Contains(cmd, " AGENTDECK_TOOL=codex ") {
		t.Fatalf("subcommand lacks matched tool: %s", cmd)
	}
}

func TestPrepareCommand_PlainShellPreservesCommand(t *testing.T) {
	i := NewInstanceWithTool("plain shell", sessionIDInjectionEnv(t), "shell")
	for _, command := range []string{"echo hi", "set -g value native", "echo first; echo second"} {
		t.Run(command, func(t *testing.T) {
			i.Command = command
			got, _, err := i.prepareCommand(command)
			if err != nil {
				t.Fatal(err)
			}
			if got != command {
				t.Fatalf("plain shell command = %q, want %q", got, command)
			}
		})
	}
}

func TestLaunchToolEnvAllPanes(t *testing.T) {
	project := sessionIDInjectionEnv(t)
	for _, tool := range []string{"claude", "gemini", "codex", "shell"} {
		t.Run(tool, func(t *testing.T) {
			i := NewInstanceWithTool("pane tool", project, tool)
			i.setInteractiveShellLaunchEnv()
			if !i.shellEnvAtLaunch() {
				t.Skip("tmux launch environment unavailable")
			}
			found := false
			for _, pair := range i.tmuxSession.LaunchEnv {
				found = found || pair == "AGENTDECK_TOOL="+tool
			}
			if !found {
				t.Fatalf("launch environment = %v, missing tool %q", i.tmuxSession.LaunchEnv, tool)
			}
		})
	}
}
