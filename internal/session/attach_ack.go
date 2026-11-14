package session

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// A user acknowledges a waiting session by opening it, which turns it idle.
// The flag lives in the tmux layer's memory and in the row's acknowledged
// column, which only a running TUI reads. A one-pass listing (`list --json`,
// the remote-agent probe a remote TUI polls) rebuilds it from the stored
// status, which a seen session keeps at waiting unless a process persisting
// statuses runs, so a session opened through `session attach` stayed waiting.
// The row therefore also records which turn was seen: the hook event's
// timestamp for hook tools, the project's latest session update for OpenCode,
// which emits no hooks (statedb.ToolDataAcknowledged*Key). A load applies the
// record while the session is still on that turn, and the next turn is unseen
// again.

// ReadAcknowledgedHookFromToolData returns the recorded hook receive time (unix
// nanoseconds), 0 for none. Legacy second-scale values are unverified.
func ReadAcknowledgedHookFromToolData(td json.RawMessage) int64 {
	var ts int64
	readToolDataKey(td, statedb.ToolDataAcknowledgedHookKey, &ts)
	return ts
}

// ReadAcknowledgedOpenCodeFromToolData returns the recorded OpenCode session
// update (unix milliseconds), 0 for none.
func ReadAcknowledgedOpenCodeFromToolData(td json.RawMessage) int64 {
	var updated int64
	readToolDataKey(td, statedb.ToolDataAcknowledgedOpenCodeKey, &updated)
	return updated
}

func readToolDataKey(td json.RawMessage, key string, v any) {
	var blob map[string]json.RawMessage
	if len(td) == 0 || json.Unmarshal(td, &blob) != nil {
		return
	}
	if raw, ok := blob[key]; ok {
		_ = json.Unmarshal(raw, v)
	}
}

// AcknowledgeSeen acknowledges the session for this process and, through db,
// for every process reading the profile's state. db may be nil.
func (i *Instance) AcknowledgeSeen(db *statedb.StateDB) error {
	if i.Tool == "opencode" {
		return i.acknowledgeOpenCodeSeen(db)
	}
	i.mu.Lock()
	var hookTS int64
	if HookStatusTool(i.Tool) && !i.hookLastUpdate.IsZero() {
		hookTS = hookIdentityTime(i.hookLastUpdate, i.hookReceivedAt).UnixNano()
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
		return db.SetAcknowledgedTurn(i.ID, statedb.ToolDataAcknowledgedHookKey, hookTS)
	}
	return db.SetAcknowledged(i.ID, true)
}

// AcknowledgeIfWaiting is the TUI's acknowledge-on-attach for a process that
// has not polled the session: one status pass as `list` runs it, then, when
// the session is waiting, AcknowledgeSeen.
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

func (i *Instance) acknowledgeOpenCodeSeen(db *statedb.StateDB) error {
	updated, err := i.openCodeLatestUpdate()
	if err != nil {
		// Without a named turn, an acknowledgement could hide unseen output.
		if sess := i.GetTmuxSession(); sess != nil {
			sess.ResetAcknowledged()
		}
		return fmt.Errorf("identifying seen opencode turn: %w", err)
	}
	i.mu.Lock()
	i.ackedOpenCodeUpdated, i.ackedOpenCodeApplied = updated, updated
	if i.tmuxSession != nil {
		i.tmuxSession.Acknowledge()
	}
	i.mu.Unlock()
	i.noteVisit(time.Now())
	if db == nil {
		return nil
	}
	return db.SetAcknowledgedTurn(i.ID, statedb.ToolDataAcknowledgedOpenCodeKey, updated)
}

// applyAcknowledgedHookLocked acknowledges the session when the loaded record
// names the waiting hook event it holds. Applied once per record, so a reset
// since (new activity, the u key) stands. Caller holds i.mu.
func (i *Instance) applyAcknowledgedHookLocked() {
	if i.tmuxSession == nil || i.ackedHookTS == 0 || i.hookStatus != "waiting" {
		return
	}
	// Second-scale records cannot identify an event within their second.
	if i.ackedHookTS < 1_000_000_000_000 ||
		hookIdentityTime(i.hookLastUpdate, i.hookReceivedAt).UnixNano() != i.ackedHookTS {
		i.tmuxSession.ResetAcknowledged()
		return
	}
	if i.ackedHookTS == i.ackedHookApplied {
		return
	}
	i.ackedHookApplied = i.ackedHookTS
	i.tmuxSession.Acknowledge()
}

func hookIdentityTime(updated, received time.Time) time.Time {
	if !received.IsZero() {
		return received
	}
	return updated
}

// applyAcknowledgedOpenCodeTurn acknowledges an OpenCode session whose
// project's latest session update is the one the loaded record names: nothing
// has happened in it since it was seen. A mismatch or unavailable server clears
// the restored flag: unverified output must stay unseen. Asks the server, so the
// caller must not hold i.mu.
func (i *Instance) applyAcknowledgedOpenCodeTurn() {
	i.mu.RLock()
	rec, sess := i.ackedOpenCodeUpdated, i.tmuxSession
	i.mu.RUnlock()
	if i.Tool != "opencode" || rec == 0 || sess == nil {
		return
	}
	updated, err := i.openCodeLatestUpdate()
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.ackedOpenCodeUpdated != rec || i.tmuxSession != sess {
		return
	}
	if err != nil || updated != rec {
		i.ackedOpenCodeApplied = 0
		sess.ResetAcknowledged()
		return
	}
	if i.ackedOpenCodeApplied == rec {
		return
	}
	i.ackedOpenCodeApplied = rec
	sess.Acknowledge()
}

// openCodePortFlag finds the --port OpenCode was launched with
// (buildOpenCodeSSEPortFlag).
var openCodePortFlag = regexp.MustCompile(`--port[= ]+['"]?(\d+)`)

// openCodeAckQueryTimeout bounds the one request to a session's own
// localhost server; a listing runs it for every acknowledged OpenCode row.
const openCodeAckQueryTimeout = time.Second

// openCodeLatestUpdate returns the latest update (unix milliseconds) among the
// project's sessions on the OpenCode server the pane was started with
// (--port): the port is not stored, and a one-pass process never saw the
// launch. Every project session counts, not only the bound one: only a full
// status update rebinds it, so after an in-pane /new it names a conversation
// that no longer changes. A sibling's activity then reads as unseen, never the
// pane's own as seen.
func (i *Instance) openCodeLatestUpdate() (int64, error) {
	i.mu.RLock()
	projectPath, sess := i.ProjectPath, i.tmuxSession
	i.mu.RUnlock()
	if sess == nil {
		return 0, fmt.Errorf("no tmux session")
	}
	cmd, err := sess.PaneStartCommand()
	if err != nil {
		return 0, err
	}
	m := openCodePortFlag.FindStringSubmatch(cmd)
	if m == nil {
		return 0, fmt.Errorf("opencode pane was started without --port")
	}
	port, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("parsing opencode port %q: %w", m[1], err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), openCodeAckQueryTimeout)
	defer cancel()
	sessions, err := i.queryOpenCodeSessionsHTTP(ctx, port, projectPath)
	if err != nil {
		return 0, fmt.Errorf("listing opencode sessions: %w", err)
	}
	project := normalizePath(projectPath)
	var latest int64
	for _, s := range sessions {
		if s.inProject(project) {
			latest = max(latest, s.Updated, s.Created)
		}
	}
	if latest == 0 {
		return 0, fmt.Errorf("opencode lists no session in %s", projectPath)
	}
	return latest, nil
}
