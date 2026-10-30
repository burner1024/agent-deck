package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
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
// timestamp for hook tools, the newest message ID for OpenCode, which emits no
// hooks (statedb.ToolDataAcknowledged*Key). A load applies the record while
// the session is still on that turn, and the next turn is unseen again.

// ReadAcknowledgedHookFromToolData returns the recorded hook timestamp (unix
// seconds), 0 for none.
func ReadAcknowledgedHookFromToolData(td json.RawMessage) int64 {
	var ts int64
	readToolDataKey(td, statedb.ToolDataAcknowledgedHookKey, &ts)
	return ts
}

// ReadAcknowledgedOpenCodeFromToolData returns the recorded OpenCode message
// ID, "" for none.
func ReadAcknowledgedOpenCodeFromToolData(td json.RawMessage) string {
	var id string
	readToolDataKey(td, statedb.ToolDataAcknowledgedOpenCodeKey, &id)
	return id
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
		return db.SetAcknowledgedTurn(i.ID, statedb.ToolDataAcknowledgedHookKey, hookTS)
	}
	return db.SetAcknowledged(i.ID, true)
}

// AcknowledgeIfWaiting is the TUI's acknowledge-on-attach for a process that
// has not polled the session: one status pass as `list` runs it, then, when
// the session is waiting, AcknowledgeSeen, or for OpenCode the record of its
// newest message.
func AcknowledgeIfWaiting(db *statedb.StateDB, inst *Instance) error {
	RefreshInstancesForCLIStatus([]*Instance{inst})
	var pass StatusUpdatePass
	if err := pass.UpdateStatusOnly(inst); err != nil {
		return err
	}
	if inst.GetStatusThreadSafe() != StatusWaiting {
		return nil
	}
	if inst.Tool != "opencode" {
		return inst.AcknowledgeSeen(db)
	}
	msg, err := inst.openCodeNewestMessageID()
	if err != nil {
		// The session cannot be named to other processes; acknowledge it for
		// this one and the TUIs that read the shared flag.
		sessionLog.Debug("opencode_ack_turn_unknown", slog.String("instance", inst.ID), slog.String("error", err.Error()))
		return inst.AcknowledgeSeen(db)
	}
	inst.mu.Lock()
	inst.ackedOpenCodeMessage, inst.ackedOpenCodeApplied = msg, msg
	if inst.tmuxSession != nil {
		inst.tmuxSession.Acknowledge()
	}
	inst.mu.Unlock()
	inst.noteVisit(time.Now())
	if db == nil {
		return nil
	}
	return db.SetAcknowledgedTurn(inst.ID, statedb.ToolDataAcknowledgedOpenCodeKey, msg)
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

// applyAcknowledgedOpenCodeTurn acknowledges an OpenCode session whose newest
// message is the one the loaded record names: nothing has happened in it since
// it was seen. Applied once per record, like applyAcknowledgedHookLocked.
// Asks the session's OpenCode server, so the caller must not hold i.mu.
func (i *Instance) applyAcknowledgedOpenCodeTurn() {
	i.mu.RLock()
	rec, applied, sess := i.ackedOpenCodeMessage, i.ackedOpenCodeApplied, i.tmuxSession
	i.mu.RUnlock()
	if i.Tool != "opencode" || rec == "" || rec == applied || sess == nil {
		return
	}
	msg, err := i.openCodeNewestMessageID()
	if err != nil || msg != rec {
		return
	}
	i.mu.Lock()
	i.ackedOpenCodeApplied = rec
	i.mu.Unlock()
	sess.Acknowledge()
}

// openCodePortFlag finds the --port OpenCode was launched with
// (buildOpenCodeSSEPortFlag).
var openCodePortFlag = regexp.MustCompile(`--port[= ]+['"]?(\d+)`)

// openCodeAckQueryTimeout bounds the one request to a session's own
// localhost server; a listing runs it for every acknowledged OpenCode row.
const openCodeAckQueryTimeout = time.Second

// openCodeNewestMessageID returns the newest message ID of the session's bound
// OpenCode session, from the server on the --port its pane was started with:
// the port is not stored, and a one-pass process never saw the launch. A port
// since reused by another program fails the request.
func (i *Instance) openCodeNewestMessageID() (string, error) {
	i.mu.RLock()
	sessionID, sess := i.OpenCodeSessionID, i.tmuxSession
	i.mu.RUnlock()
	if sessionID == "" || sess == nil {
		return "", fmt.Errorf("no bound opencode session")
	}
	cmd, err := sess.PaneStartCommand()
	if err != nil {
		return "", err
	}
	m := openCodePortFlag.FindStringSubmatch(cmd)
	if m == nil {
		return "", fmt.Errorf("opencode pane was started without --port")
	}
	port, err := strconv.Atoi(m[1])
	if err != nil {
		return "", fmt.Errorf("parsing opencode port %q: %w", m[1], err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), openCodeAckQueryTimeout)
	defer cancel()
	endpoint := url.URL{
		Scheme:   "http",
		Host:     net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Path:     "/session/" + sessionID + "/message",
		RawQuery: "limit=1",
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := openCodeSessionHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("opencode messages endpoint returned %s", resp.Status)
	}
	// OpenCode 1.0 ignores limit and lists every message, oldest first; later
	// releases page back from the newest. Message IDs sort by creation, so the
	// highest one is the newest either way.
	var messages []struct {
		Info struct {
			ID string `json:"id"`
		} `json:"info"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, openCodeSessionResponseLimit)).Decode(&messages); err != nil {
		return "", fmt.Errorf("decoding opencode messages: %w", err)
	}
	var newest string
	for _, m := range messages {
		if m.Info.ID > newest {
			newest = m.Info.ID
		}
	}
	if newest == "" {
		return "", fmt.Errorf("opencode session %s has no messages", sessionID)
	}
	return newest, nil
}
