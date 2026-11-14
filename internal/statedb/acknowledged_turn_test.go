package statedb

import (
	"encoding/json"
	"testing"
	"time"
)

// TestSetAcknowledgedTurn_RecordsTheSeenTurnAndUnreadClearsIt pins the
// shared record of which turn a user has seen: the acknowledgement sets the
// flag and stores the turn beside the row's other extras, and clearing the
// flag (the u key) drops every record so no process re-applies one.
func TestSetAcknowledgedTurn_RecordsTheSeenTurnAndUnreadClearsIt(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveInstance(&InstanceRow{
		ID: "a1", Title: "a1", ProjectPath: "/tmp", GroupPath: "grp",
		Tool: "claude", Status: "waiting", CreatedAt: time.Now(),
		ToolData: json.RawMessage(`{"claude_session_id":"c1"}`),
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	if err := db.SetAcknowledgedTurn("a1", ToolDataAcknowledgedHookKey, 1700000000123456789); err != nil {
		t.Fatalf("SetAcknowledgedTurn hook: %v", err)
	}
	if err := db.SetAcknowledgedTurn("a1", ToolDataAcknowledgedOpenCodeKey, 1700000000123); err != nil {
		t.Fatalf("SetAcknowledgedTurn opencode: %v", err)
	}
	statuses, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses: %v", err)
	}
	if !statuses["a1"].Acknowledged {
		t.Error("acknowledged = false after SetAcknowledgedTurn, want true")
	}
	td := toolDataOf(t, db, "a1")
	for key, want := range map[string]string{
		ToolDataAcknowledgedHookKey:     "1700000000123456789",
		ToolDataAcknowledgedOpenCodeKey: "1700000000123",
		"claude_session_id":             `"c1"`,
	} {
		if got := string(td[key]); got != want {
			t.Errorf("tool_data.%s = %q, want %s", key, got, want)
		}
	}

	if err := db.SetAcknowledged("a1", false); err != nil {
		t.Fatalf("SetAcknowledged(false): %v", err)
	}
	td = toolDataOf(t, db, "a1")
	for _, key := range []string{ToolDataAcknowledgedHookKey, ToolDataAcknowledgedOpenCodeKey} {
		if got, ok := td[key]; ok {
			t.Errorf("tool_data.%s = %s after marking unread, want it removed", key, got)
		}
	}
	if got := string(td["claude_session_id"]); got != `"c1"` {
		t.Errorf("tool_data.claude_session_id = %q after marking unread, want it kept", got)
	}
}

func toolDataOf(t *testing.T, db *StateDB, id string) map[string]json.RawMessage {
	t.Helper()
	var raw string
	if err := db.db.QueryRow(`SELECT tool_data FROM instances WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("read tool_data of %s: %v", id, err)
	}
	var td map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &td); err != nil {
		t.Fatalf("tool_data of %s is not JSON: %v (%q)", id, err, raw)
	}
	return td
}
