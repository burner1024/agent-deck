package statedb

import (
	"encoding/json"
	"testing"
	"time"
)

// TestSetAcknowledgedHook_RecordsTheSeenEventAndUnreadClearsIt pins the
// shared record of which hook event a user has seen: the acknowledgement sets
// the flag and stores the event's timestamp beside the row's other extras, and
// clearing the flag (the u key) drops the record so no process re-applies it.
func TestSetAcknowledgedHook_RecordsTheSeenEventAndUnreadClearsIt(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveInstance(&InstanceRow{
		ID: "a1", Title: "a1", ProjectPath: "/tmp", GroupPath: "grp",
		Tool: "claude", Status: "waiting", CreatedAt: time.Now(),
		ToolData: json.RawMessage(`{"claude_session_id":"c1"}`),
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	if err := db.SetAcknowledgedHook("a1", 1700000000); err != nil {
		t.Fatalf("SetAcknowledgedHook: %v", err)
	}
	statuses, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses: %v", err)
	}
	if !statuses["a1"].Acknowledged {
		t.Error("acknowledged = false after SetAcknowledgedHook, want true")
	}
	td := toolDataOf(t, db, "a1")
	if got := string(td[ToolDataAcknowledgedHookKey]); got != "1700000000" {
		t.Errorf("tool_data.%s = %q, want 1700000000", ToolDataAcknowledgedHookKey, got)
	}
	if got := string(td["claude_session_id"]); got != `"c1"` {
		t.Errorf("tool_data.claude_session_id = %q after the acknowledgement, want it kept", got)
	}

	if err := db.SetAcknowledged("a1", false); err != nil {
		t.Fatalf("SetAcknowledged(false): %v", err)
	}
	td = toolDataOf(t, db, "a1")
	if got, ok := td[ToolDataAcknowledgedHookKey]; ok {
		t.Errorf("tool_data.%s = %s after marking unread, want it removed", ToolDataAcknowledgedHookKey, got)
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
