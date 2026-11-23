package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSessionSendBusyComposerExitStatus(t *testing.T) {
	skipIfNoTmuxBinaryCLI(t)
	home, inst := seedIssue2099Session(t, "stuck-paste")
	inst.Tool = "claude"
	inst.Status = session.StatusRunning
	storage, err := session.NewStorageWithProfile(session.DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Save([]*session.Instance{inst}); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	baseline, err := os.ReadFile("../../internal/send/testdata/busy-tool-running-2.1.296.ansi")
	if err != nil {
		t.Fatal(err)
	}
	const message = "OK\n\nplease inspect the result and report the failure"
	const prompt = "\u276f\u00a0\x1b[39m"
	if strings.Count(string(baseline), prompt) != 1 {
		t.Fatal("fixture must have exactly one empty composer prompt")
	}
	final := strings.Replace(string(baseline), prompt, prompt+"OK\n\n  [Pasted text #1 +3 lines]", 1)
	dir := t.TempDir()
	for name, data := range map[string]string{
		"before":  string(baseline),
		"after":   final,
		"pane.sh": "stty -echo\ncat \"$1\"\nIFS= read -r line\nprintf '\\033[H\\033[2J'\ncat \"$2\"\ncat >/dev/null\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pane := inst.GetTmuxSession()
	if err := pane.Start(fmt.Sprintf("sh %q %q %q", filepath.Join(dir, "pane.sh"), filepath.Join(dir, "before"), filepath.Join(dir, "after"))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pane.KillAndWait(); err != nil {
			t.Errorf("stop fixture pane: %v", err)
		}
	})
	waitForPane(t, pane, func(raw string) bool { return strings.Contains(raw, "Skedaddling") })
	hooks := session.GetHooksDir()
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := fmt.Sprintf(`{"status":"running","session_id":"fixture","event":"UserPromptSubmit","ts":%d}`, time.Now().Unix())
	if err := os.WriteFile(filepath.Join(hooks, inst.ID+".json"), []byte(hook), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home, "-p", session.DefaultProfile, "session", "send", "--no-wait", "--ignore-budget", "--no-tag", "--json", inst.ID, message)
	var result struct {
		Success      bool   `json:"success"`
		Delivery     string `json:"delivery"`
		Confirmation string `json:"confirmation"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid result: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if code != 1 || result.Success || result.Delivery != deliveryTypedNotSubmitted || result.Confirmation != send.ConfirmationFailed {
		t.Fatalf("exit=%d result=%+v\nstdout: %s\nstderr: %s", code, result, stdout, stderr)
	}
}
