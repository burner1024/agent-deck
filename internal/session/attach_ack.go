package session

import (
	"encoding/json"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// A user acknowledges a waiting session by opening it, which turns it idle.
// The flag lives in the tmux layer's memory and in the row's acknowledged
// column, which only a running TUI reads. A one-pass listing (`list --json`,
// the remote-agent probe a remote TUI polls) rebuilds it from the stored
// status, which a seen session keeps at waiting unless a process persisting
// statuses runs, so a session opened through `session attach` stayed waiting.
// For hook tools the row therefore also records which hook event was seen
// (statedb.ToolDataAcknowledgedHookKey); a load applies it while the hook
// still names that event, and the next event is unseen again.

// ReadAcknowledgedHookFromToolData returns the recorded hook timestamp (unix
// seconds), 0 for none.
func ReadAcknowledgedHookFromToolData(td json.RawMessage) int64 {
	var blob map[string]json.RawMessage
	if len(td) == 0 || json.Unmarshal(td, &blob) != nil {
		return 0
	}
	var ts int64
	if raw, ok := blob[statedb.ToolDataAcknowledgedHookKey]; !ok || json.Unmarshal(raw, &ts) != nil {
		return 0
	}
	return ts
}

// AcknowledgeSeen acknowledges the session for this process and, through db,
// for every process reading the profile's state. db may be nil.
func (i *Instance) AcknowledgeSeen(db *statedb.StateDB) error {
	i.mu.Lock()
	var hookTS int64
	if HookStatusTool(i.Tool) && !i.hookLastUpdate.IsZero() {
		hookTS = i.hookLastUpdate.Unix()
		i.ackedHookTS, i.ackedHookApplied = hookTS, hookTS
	}
	if i.tmuxSession != nil {
		i.tmuxSession.Acknowledge()
	}
	i.mu.Unlock()
	i.noteVisit(time.Now())
	if db == nil {
		return nil
	}
	if hookTS != 0 {
		return db.SetAcknowledgedHook(i.ID, hookTS)
	}
	return db.SetAcknowledged(i.ID, true)
}

// AcknowledgeIfWaiting is the TUI's acknowledge-on-attach for a process that
// has not polled the session: one status pass as `list` runs it, then
// AcknowledgeSeen when the session is waiting.
func AcknowledgeIfWaiting(db *statedb.StateDB, inst *Instance) error {
	RefreshInstancesForCLIStatus([]*Instance{inst})
	var pass StatusUpdatePass
	if err := pass.UpdateStatusOnly(inst); err != nil {
		return err
	}
	if inst.GetStatusThreadSafe() != StatusWaiting {
		return nil
	}
	return inst.AcknowledgeSeen(db)
}

// applyAcknowledgedHookLocked acknowledges the session when the loaded record
// names the waiting hook event it holds. Applied once per record, so a reset
// since (new activity, the u key) stands. Caller holds i.mu.
func (i *Instance) applyAcknowledgedHookLocked() {
	if i.tmuxSession == nil || i.ackedHookTS == 0 || i.ackedHookTS == i.ackedHookApplied ||
		i.hookStatus != "waiting" || i.hookLastUpdate.Unix() != i.ackedHookTS {
		return
	}
	i.ackedHookApplied = i.ackedHookTS
	i.tmuxSession.Acknowledge()
}
