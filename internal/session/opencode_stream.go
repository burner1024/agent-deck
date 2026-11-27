package session

// Which OpenCode session a child is in, read from the event server of its own TUI.
//
// An OpenCode 1.x child started with --port runs one HTTP server of its own. `GET /session?directory=`
// on it lists every session stored for the directory, so children launched into one directory all
// see each other's sessions there and a listing cannot say which one is theirs. `GET /event` carries
// only the sessions of that TUI (read on OpenCode 1.19.6: two TUIs in one directory, and each stream
// named its own session alone). A child whose server is reachable is bound to the root session its
// own stream names, never to a directory listing.
//
// Two properties of the stream shape the reader:
//   - Nothing is replayed. A subscriber hears an event only while it is connected, so a child is
//     read from its launch until its first session is named.
//   - A connection accepted before the TUI is ready can stay mute until the TUI exits. A live one
//     answers server.connected at once and server.heartbeat every 10 seconds, so a connection that
//     says nothing for openCodeStreamHello is dropped and made again.

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// openCodeStreamClient connects to a child's own event server. It follows no redirect: an answer
// from another server would name another child's session.
var openCodeStreamClient = &http.Client{
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Transport: &http.Transport{
		DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
	},
}

const (
	// openCodeStreamBindDeadline bounds how long a launch or a restart waits for the child's first
	// session. It is the window the directory-listing watcher had.
	openCodeStreamBindDeadline = 5 * time.Minute
	// openCodeStreamRestoredWindow bounds the wait of a TUI that restores a child without a session
	// id. Its status watcher reads the same stream after that.
	openCodeStreamRestoredWindow = 15 * time.Second
	// openCodeStreamIdle is how long a connection that greeted may stay silent.
	openCodeStreamIdle = 30 * time.Second
	// openCodeStreamRetry is the pause between two connections that failed or were dropped.
	openCodeStreamRetry = 500 * time.Millisecond
)

// openCodeStreamHello is how long a new connection may stay silent before it is dropped. A
// variable for tests.
var openCodeStreamHello = 3 * time.Second

// openCodePortFlag is the --port argument of a child's start command.
var openCodePortFlag = regexp.MustCompile(`--port[ =]+([0-9]+)`)

// openCodeStreamRoot returns the root session an event of a child's own stream names. Only
// session.created and session.updated count: they carry the session's info, and a sub-agent session
// has a parentID there. A message event does not say whether its session is a root.
func openCodeStreamRoot(ev openCodeEvent) (string, bool) {
	id := ev.Properties.SessionID
	if id == "" || (ev.Type != "session.created" && ev.Type != "session.updated") {
		return "", false
	}
	if ev.Properties.Info.ID != id || ev.Properties.Info.ParentID != "" {
		return "", false
	}
	return id, true
}

// openCodeStreamRoots records the root sessions a child's own stream named and when each was named
// last. A status pass binds an unbound child to the one named last, or moves a bound child to it
// when the pane started a new session.
type openCodeStreamRoots struct {
	port int
	last map[string]time.Time
}

var openCodeStream = struct {
	sync.Mutex
	byInstance map[string]*openCodeStreamRoots
}{byInstance: make(map[string]*openCodeStreamRoots)}

// noteOpenCodeStreamRoot records that the stream of instanceID on port named sessionID at at. A
// stream on another port is another server: the record starts over.
func noteOpenCodeStreamRoot(instanceID string, port int, sessionID string, at time.Time) {
	if instanceID == "" || sessionID == "" {
		return
	}
	openCodeStream.Lock()
	defer openCodeStream.Unlock()
	roots := openCodeStream.byInstance[instanceID]
	if roots == nil || roots.port != port {
		roots = &openCodeStreamRoots{port: port, last: make(map[string]time.Time)}
		openCodeStream.byInstance[instanceID] = roots
	}
	if prev, ok := roots.last[sessionID]; !ok || at.After(prev) {
		roots.last[sessionID] = at
	}
}

// latestOpenCodeStreamRoot is the root session the stream of instanceID on port named last.
func latestOpenCodeStreamRoot(instanceID string, port int) (string, bool) {
	openCodeStream.Lock()
	defer openCodeStream.Unlock()
	roots := openCodeStream.byInstance[instanceID]
	if roots == nil || roots.port != port {
		return "", false
	}
	var latest string
	var latestAt time.Time
	for id, at := range roots.last {
		if latest == "" || at.After(latestAt) || (at.Equal(latestAt) && id < latest) {
			latest, latestAt = id, at
		}
	}
	return latest, latest != ""
}

// forgetOpenCodeStreamRoots drops what was recorded for instanceID.
func forgetOpenCodeStreamRoots(instanceID string) {
	openCodeStream.Lock()
	defer openCodeStream.Unlock()
	delete(openCodeStream.byInstance, instanceID)
}

// readOpenCodeStream reads the child's own event stream, keeping a connection to it, until onRoot
// returns true for a root session the stream names or ctx ends.
func readOpenCodeStream(ctx context.Context, port int, onRoot func(root string) bool) {
	for ctx.Err() == nil {
		if readOpenCodeStreamOnce(ctx, port, onRoot) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(openCodeStreamRetry):
		}
	}
}

// readOpenCodeStreamOnce reads one connection to the child's stream until it ends. It returns true
// when onRoot asked to stop reading.
func readOpenCodeStreamOnce(ctx context.Context, port int, onRoot func(root string) bool) bool {
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	hello := time.AfterFunc(openCodeStreamHello, cancel)
	defer hello.Stop()

	req, err := http.NewRequestWithContext(connCtx, http.MethodGet, openCodeTUIServerSource(port).base+"/event", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := openCodeStreamClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		hello.Reset(openCodeStreamIdle)
		payload, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var ev openCodeEvent
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}
		if root, ok := openCodeStreamRoot(ev); ok && onRoot(root) {
			return true
		}
	}
	return false
}

// openCodeOwnServerPort is the port of the child's own event server: the one this process gave its
// launch, else the one in its pane's start command, which is where a process that did not launch
// the child (a reloaded TUI, the notify daemon, session show) finds it. A recovered port is kept,
// so the status watcher reaches the same server.
func (i *Instance) openCodeOwnServerPort() int {
	if i.Tool != "opencode" || i.IsSSH() || i.IsSandboxed() {
		return 0
	}
	if port := i.GetOpenCodePort(); port > 0 {
		return port
	}
	i.mu.RLock()
	tmuxSession := i.tmuxSession
	i.mu.RUnlock()
	if tmuxSession == nil || !tmuxSession.Exists() {
		return 0
	}
	command, err := tmuxSession.PaneStartCommand()
	if err != nil {
		sessionLog.Debug("opencode_pane_command_unreadable",
			slog.String("instance_id", i.ID),
			slog.String("error", err.Error()),
		)
		return 0
	}
	match := openCodePortFlag.FindStringSubmatch(command)
	if match == nil {
		return 0
	}
	port, err := strconv.Atoi(match[1])
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	i.setOpenCodePort(port)
	sessionLog.Debug("opencode_port_recovered",
		slog.String("instance_id", i.ID),
		slog.Int("port", port),
	)
	return port
}

// bindOpenCodeSessionFromStream reads the child's own event stream until it names a root session
// and binds that session, for at most wait.
func (i *Instance) bindOpenCodeSessionFromStream(port int, wait time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	sessionLog.Debug("opencode_stream_read_started",
		slog.String("instance_id", i.ID),
		slog.Int("port", port),
	)
	readOpenCodeStream(ctx, port, func(root string) bool {
		i.setOpenCodeSession(root)
		sessionLog.Debug("opencode_session_detected",
			slog.String("instance_id", i.ID),
			slog.String("session_id", root),
			slog.String("phase", "stream"),
			slog.Int("port", port),
		)
		return true
	})
}

// AwaitOpenCodeStreamBinding waits up to wait for the reader of the child's own event stream to bind
// its session, and returns the binding and when it was made. A process that started the child calls
// it before it exits, as the reader ends with that process. It returns "" for a child with no event
// server of its own, whose session is not read from a stream.
func (i *Instance) AwaitOpenCodeStreamBinding(wait time.Duration) (string, time.Time) {
	if i.openCodeOwnServerPort() == 0 {
		return "", time.Time{}
	}
	deadline := time.Now().Add(wait)
	for {
		i.mu.RLock()
		id, at := i.OpenCodeSessionID, i.OpenCodeDetectedAt
		i.mu.RUnlock()
		if id != "" || !time.Now().Before(deadline) {
			return id, at
		}
		time.Sleep(100 * time.Millisecond)
	}
}
