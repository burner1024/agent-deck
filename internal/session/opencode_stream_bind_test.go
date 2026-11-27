package session

// Tests for binding an OpenCode 1.x child that has its own event server (--port).
//
// `GET /session?directory=` on that server lists every session stored for the directory, siblings
// launched into it included; `GET /event` carries the sessions of its own TUI only (OpenCode 1.19.6).

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ownStreamFirstTurn is the session-bearing part of the /event stream of an OpenCode 1.19.6 TUI
// started with --port, from its first prompt to the end of that turn. Captured from the real
// server; ids are shortened.
var ownStreamFirstTurn = []string{
	`{"id":"evt_01","type":"session.created","properties":{"sessionID":"ses_OWN","info":{"id":"ses_OWN","version":"1.19.6","projectID":"global","directory":"/work","title":"New session - 2026-11-27T15:39:47.570Z","agent":"build","time":{"created":1795793987570,"updated":1795793987570}}}}`,
	`{"id":"evt_02","type":"message.updated","properties":{"sessionID":"ses_OWN","info":{"id":"msg_01","role":"user","sessionID":"ses_OWN","time":{"created":1795793987606},"agent":"build"}}}`,
	`{"id":"evt_03","type":"message.part.updated","properties":{"sessionID":"ses_OWN","part":{"type":"text","text":"Reply with the single word ALPHA","messageID":"msg_01","sessionID":"ses_OWN","id":"prt_01"},"time":1795793987618}}`,
	`{"id":"evt_04","type":"session.updated","properties":{"sessionID":"ses_OWN","info":{"id":"ses_OWN","version":"1.19.6","projectID":"global","directory":"/work","title":"New session - 2026-11-27T15:39:47.570Z","agent":"build","time":{"created":1795793987570,"updated":1795793987624}}}}`,
	`{"id":"evt_05","type":"session.status","properties":{"sessionID":"ses_OWN","status":{"type":"busy"}}}`,
	`{"id":"evt_06","type":"message.updated","properties":{"sessionID":"ses_OWN","info":{"id":"msg_02","role":"assistant","sessionID":"ses_OWN","time":{"created":1795793987640},"agent":"build"}}}`,
	`{"id":"evt_07","type":"session.updated","properties":{"sessionID":"ses_OWN","info":{"id":"ses_OWN","version":"1.19.6","projectID":"global","directory":"/work","title":"New session - 2026-11-27T15:39:47.570Z","agent":"build","time":{"created":1795793987570,"updated":1795793987641}}}}`,
	`{"id":"evt_08","type":"message.part.delta","properties":{"sessionID":"ses_OWN","part":{"type":"text","text":"do","messageID":"msg_02","sessionID":"ses_OWN","id":"prt_02"}}}`,
	`{"id":"evt_09","type":"message.part.updated","properties":{"sessionID":"ses_OWN","part":{"type":"text","text":"done","messageID":"msg_02","sessionID":"ses_OWN","id":"prt_02"}}}`,
	`{"id":"evt_10","type":"session.status","properties":{"sessionID":"ses_OWN","status":{"type":"idle"}}}`,
	`{"id":"evt_11","type":"session.idle","properties":{"sessionID":"ses_OWN"}}`,
	`{"id":"evt_12","type":"session.updated","properties":{"sessionID":"ses_OWN","info":{"id":"ses_OWN","version":"1.19.6","projectID":"global","directory":"/work","title":"New session - 2026-11-27T15:39:47.570Z","agent":"build","time":{"created":1795793987570,"updated":1795793987700}}}}`,
}

// ownServerStub is one child's --port server: a directory listing and the event stream of its own
// TUI. The real server sends server.connected at once and no past event to a new subscriber.
type ownServerStub struct {
	server          *httptest.Server
	listingRequests atomic.Int32
	streamRequests  atomic.Int32
	// muteFirst keeps the first /event connection silent, as the real server does for a
	// connection accepted before its TUI is ready.
	muteFirst bool
	// redirectTo answers /event with a redirect to this URL.
	redirectTo string
	connected  chan struct{}
	events     <-chan string
}

func newOwnServerStub(t *testing.T, listing string, events <-chan string) *ownServerStub {
	t.Helper()
	stub := newOwnServerStubUnstarted(t, 0, listing, events)
	stub.server.Start()
	return stub
}

// newOwnServerStubUnstarted prepares the server on port (any port when 0) without serving; the
// caller starts it with its Start method.
func newOwnServerStubUnstarted(t *testing.T, port int, listing string, events <-chan string) *ownServerStub {
	t.Helper()
	stub := &ownServerStub{connected: make(chan struct{}, 8), events: events}
	mux := http.NewServeMux()
	mux.HandleFunc("/session", func(w http.ResponseWriter, _ *http.Request) {
		stub.listingRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, listing)
	})
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		stub.streamRequests.Add(1)
		if stub.redirectTo != "" {
			http.Redirect(w, r, stub.redirectTo, http.StatusTemporaryRedirect)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer is not a flusher")
			return
		}
		if stub.muteFirst {
			stub.muteFirst = false
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		const hello = "data: {\"id\":\"evt_00\",\"type\":\"server.connected\",\"properties\":{}}\n\n"
		fmt.Fprint(w, hello)
		flusher.Flush()
		select {
		case stub.connected <- struct{}{}:
		default:
		}
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", ev)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	if port == 0 {
		stub.server = httptest.NewUnstartedServer(mux)
	} else {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("listen on 127.0.0.1:%d: %v", port, err)
		}
		stub.server = &httptest.Server{Listener: listener, Config: &http.Server{Handler: mux}}
	}
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *ownServerStub) port() int {
	return s.server.Listener.Addr().(*net.TCPAddr).Port
}

// listingWithSibling is what GET /session?directory= answers on either child's server once a
// sibling in the same directory is busy: both sessions, the sibling updated last.
func listingWithSibling(projectPath string) string {
	now := time.Now().UnixMilli()
	return fmt.Sprintf(`[
		{"id":"ses_OWN","directory":%q,"time":{"created":%d,"updated":%d}},
		{"id":"ses_SIBLING","directory":%q,"time":{"created":%d,"updated":%d}}
	]`, projectPath, now-4000, now-3000, projectPath, now-2000, now-1000)
}

func boundOpenCodeSession(inst *Instance) string {
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	return inst.OpenCodeSessionID
}

// awaitBinding waits until the instance is bound or the reader ended.
func awaitBinding(t *testing.T, inst *Instance, done <-chan struct{}) string {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for boundOpenCodeSession(inst) == "" && time.Now().Before(deadline) {
		select {
		case <-done:
			return boundOpenCodeSession(inst)
		case <-time.After(10 * time.Millisecond):
		}
	}
	return boundOpenCodeSession(inst)
}

func TestOpenCodeStreamRoot(t *testing.T) {
	tests := []struct {
		name  string
		event string
		want  string
	}{
		{"session.created of a root names it", `{"type":"session.created","properties":{"sessionID":"ses_OWN","info":{"id":"ses_OWN"}}}`, "ses_OWN"},
		{"session.updated of a root names it", `{"type":"session.updated","properties":{"sessionID":"ses_OWN","info":{"id":"ses_OWN","parentID":""}}}`, "ses_OWN"},
		{"a sub-agent session has a parent", `{"type":"session.created","properties":{"sessionID":"ses_CHILD","info":{"id":"ses_CHILD","parentID":"ses_OWN"}}}`, ""},
		{"a message event does not say whether its session is a root", `{"type":"message.updated","properties":{"sessionID":"ses_OWN","info":{"id":"msg_01","role":"user","sessionID":"ses_OWN"}}}`, ""},
		{"info of another session", `{"type":"session.updated","properties":{"sessionID":"ses_OWN","info":{"id":"ses_OTHER"}}}`, ""},
		{"a status event", `{"type":"session.status","properties":{"sessionID":"ses_OWN","status":{"type":"busy"}}}`, ""},
		{"server.connected", `{"type":"server.connected","properties":{}}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ev openCodeEvent
			if err := json.Unmarshal([]byte(tt.event), &ev); err != nil {
				t.Fatalf("decode event: %v", err)
			}
			got, ok := openCodeStreamRoot(ev)
			if got != tt.want || ok != (tt.want != "") {
				t.Fatalf("openCodeStreamRoot() = %q, %t; want %q", got, ok, tt.want)
			}
		})
	}
}

func TestLatestOpenCodeStreamRoot(t *testing.T) {
	const instance = "inst-latest"
	t.Cleanup(func() { forgetOpenCodeStreamRoots(instance) })
	t0 := time.Now()
	noteOpenCodeStreamRoot(instance, 41001, "ses_OWN", t0)
	noteOpenCodeStreamRoot(instance, 41001, "ses_NEW", t0.Add(time.Second))
	noteOpenCodeStreamRoot(instance, 41001, "ses_OWN", t0.Add(2*time.Millisecond))
	if got, _ := latestOpenCodeStreamRoot(instance, 41001); got != "ses_NEW" {
		t.Fatalf("latest root = %q, want ses_NEW (named last)", got)
	}
	if got, ok := latestOpenCodeStreamRoot("inst-other", 41001); ok {
		t.Fatalf("latest root of another instance = %q, want none", got)
	}
	if got, ok := latestOpenCodeStreamRoot(instance, 41002); ok {
		t.Fatalf("latest root = %q on another port, want none: another port is another server", got)
	}
	noteOpenCodeStreamRoot(instance, 41002, "ses_RESUMED", t0)
	if got, _ := latestOpenCodeStreamRoot(instance, 41002); got != "ses_RESUMED" {
		t.Fatalf("latest root = %q on the new port, want ses_RESUMED", got)
	}
	forgetOpenCodeStreamRoots(instance)
	if got, ok := latestOpenCodeStreamRoot(instance, 41002); ok {
		t.Fatalf("latest root = %q after forget, want none", got)
	}
	noteOpenCodeStreamRoot("", 41001, "ses_X", t0)
	noteOpenCodeStreamRoot(instance, 41001, "", t0)
	openCodeStream.Lock()
	defer openCodeStream.Unlock()
	if len(openCodeStream.byInstance) != 0 {
		t.Fatalf("a record without an instance or without a session was kept: %v", openCodeStream.byInstance)
	}
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for a free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close the free port: %v", err)
	}
	return port
}

// TestBindOpenCodeSessionFromStream_WaitsForTheServer: the child's server listens only once its
// TUI is up, so the reader makes its connection again until one answers.
func TestBindOpenCodeSessionFromStream_WaitsForTheServer(t *testing.T) {
	projectPath := t.TempDir()
	events := make(chan string)
	stub := newOwnServerStubUnstarted(t, freeLocalPort(t), listingWithSibling(projectPath), events)
	inst := &Instance{ID: "inst-wait", Tool: "opencode", ProjectPath: projectPath, OpenCodePort: stub.port()}
	done := make(chan struct{})
	go func() {
		inst.bindOpenCodeSessionFromStream(stub.port(), 10*time.Second)
		close(done)
	}()
	time.Sleep(1200 * time.Millisecond)
	if got := boundOpenCodeSession(inst); got != "" {
		t.Fatalf("OpenCodeSessionID = %q before the server listens", got)
	}
	stub.server.Start()

	select {
	case <-stub.connected:
	case <-done:
		t.Fatalf("reader ended before the server answered; stream requests = %d", stub.streamRequests.Load())
	case <-time.After(8 * time.Second):
		t.Fatalf("no stream connection after the server started; stream requests = %d", stub.streamRequests.Load())
	}
	events <- ownStreamFirstTurn[0]

	if got := awaitBinding(t, inst, done); got != "ses_OWN" {
		t.Fatalf("OpenCodeSessionID = %q, want ses_OWN", got)
	}
	close(events)
}

// TestBindOpenCodeSessionFromStream_DropsMuteConnection: a connection accepted before the TUI is
// ready stays silent. The reader drops it and makes a new one, which then carries the session.
func TestBindOpenCodeSessionFromStream_DropsMuteConnection(t *testing.T) {
	projectPath := t.TempDir()
	events := make(chan string)
	stub := newOwnServerStubUnstarted(t, 0, listingWithSibling(projectPath), events)
	stub.muteFirst = true
	stub.server.Start()
	inst := &Instance{ID: "inst-mute", Tool: "opencode", ProjectPath: projectPath, OpenCodePort: stub.port()}
	previous := openCodeStreamHello
	openCodeStreamHello = 200 * time.Millisecond
	t.Cleanup(func() { openCodeStreamHello = previous })
	done := make(chan struct{})
	go func() {
		inst.bindOpenCodeSessionFromStream(stub.port(), 10*time.Second)
		close(done)
	}()

	select {
	case <-stub.connected:
	case <-time.After(8 * time.Second):
		t.Fatalf("the mute connection was kept: stream connections = %d", stub.streamRequests.Load())
	}
	if got := stub.streamRequests.Load(); got < 2 {
		t.Fatalf("stream connections = %d, want a second one after the mute connection", got)
	}
	events <- ownStreamFirstTurn[0]

	if got := awaitBinding(t, inst, done); got != "ses_OWN" {
		t.Fatalf("OpenCodeSessionID = %q, want ses_OWN", got)
	}
	close(events)
}

// TestBindOpenCodeSessionFromStream_RefusesRedirect: a redirect is not followed, as its target
// would be another child's server, naming another child's session.
func TestBindOpenCodeSessionFromStream_RefusesRedirect(t *testing.T) {
	projectPath := t.TempDir()
	events := make(chan string)
	target := newOwnServerStub(t, "[]", events)
	stub := newOwnServerStub(t, listingWithSibling(projectPath), events)
	stub.redirectTo = target.server.URL + "/event"
	inst := &Instance{ID: "inst-redirect", Tool: "opencode", ProjectPath: projectPath, OpenCodePort: stub.port()}
	done := make(chan struct{})
	go func() {
		inst.bindOpenCodeSessionFromStream(stub.port(), 1500*time.Millisecond)
		close(done)
	}()
	<-done

	if got := target.streamRequests.Load(); got != 0 {
		t.Fatalf("redirect target received %d stream requests, want 0", got)
	}
	if got := boundOpenCodeSession(inst); got != "" {
		t.Fatalf("OpenCodeSessionID = %q from a redirected stream, want empty", got)
	}
	close(events)
}

// The three tests below are the regression for children of one directory sharing a session id.
// They read as the contract: a child with an event server of its own is bound by that server's
// stream, never by the directory listing, where a sibling updated last wins.

func TestDetectOpenCodeSession_ManagedPortBindsOwnStreamNotDirectory(t *testing.T) {
	projectPath := t.TempDir()
	events := make(chan string)
	stub := newOwnServerStub(t, listingWithSibling(projectPath), events)
	inst := &Instance{
		ID:                "inst-own",
		Tool:              "opencode",
		ProjectPath:       projectPath,
		OpenCodePort:      stub.port(),
		OpenCodeStartedAt: time.Now().UnixMilli(),
	}

	go inst.detectOpenCodeSessionAsync()

	// The child's session is created when its first prompt is submitted, after the stream is read.
	select {
	case <-stub.connected:
	case <-time.After(5 * time.Second):
		t.Fatalf("stream subscriber did not connect; binding = %q", boundOpenCodeSession(inst))
	}
	// The reader stops at the first root session the stream names, so only that event is delivered.
	events <- ownStreamFirstTurn[0]
	deadline := time.Now().Add(5 * time.Second)
	for boundOpenCodeSession(inst) == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	close(events)

	if got := boundOpenCodeSession(inst); got != "ses_OWN" {
		t.Fatalf("OpenCodeSessionID = %q, want ses_OWN from the child's own stream", got)
	}
	if got := stub.listingRequests.Load(); got != 0 {
		t.Fatalf("directory listing requested %d times, want 0", got)
	}
}

func TestQueryOpenCodeSession_ManagedPortNeverAdoptsDirectoryListing(t *testing.T) {
	projectPath := t.TempDir()
	stub := newOwnServerStub(t, listingWithSibling(projectPath), make(chan string))
	inst := &Instance{
		ID:                "inst-own",
		Tool:              "opencode",
		ProjectPath:       projectPath,
		OpenCodePort:      stub.port(),
		OpenCodeStartedAt: time.Now().UnixMilli(),
	}

	if got := inst.queryOpenCodeSession(); got != "" {
		t.Fatalf("unbound query session ID = %q from a directory listing a sibling can win, want empty", got)
	}
	if got := stub.listingRequests.Load(); got != 0 {
		t.Fatalf("directory listing requested %d times, want 0", got)
	}
}

func TestUpdateOpenCodeSession_ManagedPortKeepsBindingOverNewerSibling(t *testing.T) {
	projectPath := t.TempDir()
	stub := newOwnServerStub(t, listingWithSibling(projectPath), make(chan string))
	inst := &Instance{
		ID:                "inst-own",
		Tool:              "opencode",
		ProjectPath:       projectPath,
		OpenCodePort:      stub.port(),
		OpenCodeSessionID: "ses_OWN",
		// The activity fallback: without a tmux tracker the instance counts as active since CreatedAt.
		CreatedAt: time.Now(),
	}

	inst.UpdateOpenCodeSession()

	if got := boundOpenCodeSession(inst); got != "ses_OWN" {
		t.Fatalf("OpenCodeSessionID = %q after a status pass with a busier sibling in the listing, want ses_OWN", got)
	}
	if got := stub.listingRequests.Load(); got != 0 {
		t.Fatalf("directory listing requested %d times, want 0", got)
	}
}

// TestOpenCodeOwnServerPort_RecoversFromPaneStartCommand: a process that did not launch the child
// finds its server by the --port of its pane's start command, and then reads no directory listing
// for it either.
func TestOpenCodeOwnServerPort_RecoversFromPaneStartCommand(t *testing.T) {
	skipIfNoTmuxBinary(t)
	projectPath := t.TempDir()
	stub := newOwnServerStub(t, listingWithSibling(projectPath), make(chan string))
	socket := fmt.Sprintf("ad-oc-own-port-%d", os.Getpid())
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	start := func(id, command string) *Instance {
		inst := &Instance{ID: id, Tool: "opencode", ProjectPath: projectPath, CreatedAt: time.Now()}
		inst.tmuxSession = tmux.NewSession(id, projectPath)
		inst.tmuxSession.SocketName = socket
		// A tool session is launched with its command as the pane's initial process.
		inst.tmuxSession.RunCommandAsInitialProcess = true
		require.NoError(t, inst.tmuxSession.Start(command))
		return inst
	}

	withPort := start("oc-own-port-with", fmt.Sprintf("sh -c 'sleep 3600 # opencode --port %d'", stub.port()))
	if got := withPort.openCodeOwnServerPort(); got != stub.port() {
		t.Fatalf("own server port = %d, want %d from the pane's start command", got, stub.port())
	}
	if got := withPort.GetOpenCodePort(); got != stub.port() {
		t.Fatalf("OpenCodePort = %d after recovery, want %d kept for the status watcher", got, stub.port())
	}
	withPort.OpenCodeSessionID = "ses_OWN"
	if got := withPort.queryOpenCodeSession(); got != "ses_OWN" {
		t.Fatalf("query session ID = %q with no stream read in this process, want the binding ses_OWN", got)
	}
	if got := stub.listingRequests.Load(); got != 0 {
		t.Fatalf("directory listing requested %d times, want 0", got)
	}

	withoutPort := start("oc-own-port-without", "sleep 3600")
	if got := withoutPort.openCodeOwnServerPort(); got != 0 {
		t.Fatalf("own server port = %d for a pane started without --port, want 0", got)
	}
	remote := &Instance{ID: "oc-own-port-remote", Tool: "opencode", ProjectPath: projectPath, SSHHost: "host"}
	remote.tmuxSession = withPort.tmuxSession
	if got := remote.openCodeOwnServerPort(); got != 0 {
		t.Fatalf("own server port = %d for a remote child, want 0: its server is not on this host", got)
	}
}

// TestQueryOpenCodeSession_ManagedPortTakesSessionRecordedInPane: the process that read the
// child's own stream at launch exits after recording the session in the pane's environment. A
// later process (the notify daemon, session show) has no stream record and must take that ID,
// still without the directory listing.
func TestQueryOpenCodeSession_ManagedPortTakesSessionRecordedInPane(t *testing.T) {
	skipIfNoTmuxBinary(t)
	pinOpenCodeMajorVersion(t, 1, true)
	projectPath := t.TempDir()
	stub := newOwnServerStub(t, listingWithSibling(projectPath), make(chan string))
	inst := NewInstanceWithTool("open-code-pane-recorded", projectPath, "opencode")
	inst.OpenCodePort = stub.port()
	inst.tmuxSession = tmux.NewSession(inst.Title, inst.ProjectPath)
	if err := inst.tmuxSession.Start("sleep 300"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inst.tmuxSession.Kill() })
	// The launching process writes through its own handle.
	launcher := &tmux.Session{Name: inst.tmuxSession.TmuxName(), SocketName: inst.tmuxSession.SocketName}
	if err := launcher.SetEnvironment("OPENCODE_SESSION_ID", "ses_OWN"); err != nil {
		t.Fatal(err)
	}

	if got := inst.queryOpenCodeSession(); got != "ses_OWN" {
		t.Fatalf("unbound query session ID = %q, want ses_OWN recorded in the child's pane", got)
	}
	if got := stub.listingRequests.Load(); got != 0 {
		t.Fatalf("directory listing requested %d times, want 0", got)
	}
}
