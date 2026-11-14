package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fxToolResultText(uuid, text string) string {
	rec := map[string]any{
		"type": "user", "uuid": uuid, "isSidechain": false,
		"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": text}}},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// A tagged send that started a long tool loop: the prompt that opened the
// turn lies outside the default tail window, by record count or by bytes.
// Classifying it unknown drops a root session's reply (no target).
func TestTurnFacts_SendTurnLongerThanTheTailWindow(t *testing.T) {
	cases := []struct {
		name   string
		result string
		pairs  int
	}{
		{"more records than the line window", "ok", turnScanTailLines},
		{"more bytes than the byte window", strings.Repeat("x", 40<<10), 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := []string{
				fxHuman("u0", "earlier question"),
				fxAssistantText("a0", "earlier answer"),
				fxUser("u1", "[agent-deck from:abc-123] four questions", nil),
			}
			for i := 0; i < c.pairs; i++ {
				lines = append(lines, fxAssistantToolUse("tu"), fxToolResultText("tr", c.result))
			}
			lines = append(lines, fxAssistantText("a9", "Answers to all four."))
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			cache := &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}
			facts, err := cache.Facts(path)
			if err != nil {
				t.Fatalf("Facts: %v", err)
			}
			if facts.Trigger != TurnTriggerSend || facts.FromID != "abc-123" {
				t.Fatalf("trigger = %q from %q, want %q from %q", facts.Trigger, facts.FromID, TurnTriggerSend, "abc-123")
			}
			if facts.Text != "Answers to all four." {
				t.Fatalf("text = %q, want the final reply", facts.Text)
			}
		})
	}
}
