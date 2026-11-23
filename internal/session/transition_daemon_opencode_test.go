package session

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitOpenCodeDaemon(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("OpenCode daemon condition did not settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestIssue151DaemonOpenCodeBootAndTurn(t *testing.T) {
	f := newTurnTestFixture(t)
	st := stubRestartPIDs(t)
	st.substate = string(SubstateIdleAtEmptyPrompt)
	f.child.Tool, f.child.ClaudeSessionID = "opencode", ""
	events := make(chan string, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/session/status", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) })
	mux.HandleFunc("/session", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `[]`) })
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"session.idle\",\"properties\":{\"sessionID\":\"ses_new\"}}\n\n")
		w.(http.Flusher).Flush()
		for {
			select {
			case event := <-events:
				fmt.Fprintf(w, "data: %s\n\n", event)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f.child.buildOpenCodeCommand("opencode")
	f.child.OpenCodePort = srv.Listener.Addr().(*net.TCPAddr).Port
	noteFreshStart(f.child, time.Now())
	storage, err := NewStorageWithProfile(GetEffectiveProfile(""))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	if err := storage.SaveWithGroups([]*Instance{f.parent, f.child}, nil); err != nil {
		t.Fatal(err)
	}
	d := NewTransitionDaemon()
	d.notifier, d.turnLiveCheck = f.d.notifier, f.d.turnLiveCheck
	// Model a child arriving after the daemon established its baseline.
	d.initialized[storage.Profile()] = true
	oldProbe := updateInstanceStatus.Load().(statusProbeFunc)
	defer updateInstanceStatus.Store(oldProbe)
	updateInstanceStatus.Store(statusProbeFunc(func(inst *Instance) error {
		if inst.ID == f.child.ID {
			// Let subscription settle before the first idle probe. With no
			// watcher, the old daemon instead publishes this idle as news.
			deadline := time.Now().Add(time.Second)
			for !readOpenCodeTurn(inst.ID).Ready && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
		}
		inst.mu.Lock()
		inst.Status = StatusIdle
		inst.mu.Unlock()
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	before := openCodeStreamGoroutines()
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()
	defer func() { cancel(); <-done }()
	waitOpenCodeDaemon(t, func() bool { return len(f.pending(t)) > 0 })
	if got := f.pending(t); len(got) != 1 || got[0] != freshStartLine {
		t.Fatalf("boot records = %q, want info line", got)
	}
	if InboxHasUrgentPending(f.parent.ID) {
		t.Fatal("boot woke parent")
	}
	events <- `{"type":"message.updated","properties":{"info":{"id":"msg_first","sessionID":"ses_new","role":"user"}}}`
	events <- `{"type":"session.status","properties":{"sessionID":"ses_new","status":{"type":"busy"}}}`
	events <- `{"type":"session.idle","properties":{"sessionID":"ses_new"}}`
	waitOpenCodeDaemon(t, func() bool { return InboxHasUrgentPending(f.parent.ID) })
	if got := f.pending(t); len(got) != 2 {
		t.Fatalf("first turn records = %q, want boot plus news", got)
	}
	cancel()
	<-done
	if readOpenCodeTurn(f.child.ID).Ready {
		t.Fatal("daemon returned without revoking its subscription proof")
	}
	if got := openCodeStreamGoroutines(); got != before {
		t.Fatalf("stream goroutines after daemon exit = %d, before = %d", got, before)
	}
}

func openCodeStreamGoroutines() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "(*OpenCodeSSEWatcher).run(")
}

type openCodeBlockingTransport struct {
	started, cancelled, release, exited chan struct{}
	once                                sync.Once
}

func (tr *openCodeBlockingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path != "/event" {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}
	tr.once.Do(func() { close(tr.started) })
	<-r.Context().Done()
	close(tr.cancelled)
	<-tr.release
	close(tr.exited)
	return nil, r.Context().Err()
}

func TestIssue151OpenCodeWatchersShareOwnership(t *testing.T) {
	reviewTestHome(t, "default")
	i := &Instance{ID: "owned-child", Tool: "opencode"}
	i.buildOpenCodeCommand("opencode")
	noteFreshStart(i, time.Now())
	events := make(chan string, 8)
	_, port := sseTestServer(t, `{}`, events)
	first, second := NewOpenCodeSSEWatcher(nil), NewOpenCodeSSEWatcher(nil)
	defer first.Stop()
	defer second.Stop()
	targets := []SSETarget{{InstanceID: i.ID, Port: port}}
	first.Sync(targets)
	waitOpenCodeDaemon(t, func() bool { return readOpenCodeTurn(i.ID).Stream != "" })
	stream := readOpenCodeTurn(i.ID).Stream
	second.Sync(targets)
	// Let the second connection attempt run before stopping that watcher.
	time.Sleep(100 * time.Millisecond)
	second.Stop()
	if obs := readOpenCodeTurn(i.ID); obs.Stream != stream {
		t.Fatalf("second watcher replaced the first subscription: %q -> %q", stream, obs.Stream)
	}
	events <- `{"type":"session.idle","properties":{"sessionID":"ses_new"}}`
	waitForStatus(t, first, i.ID, "waiting")
}

func TestIssue151OpenCodeWatcherStopJoins(t *testing.T) {
	for _, owner := range []string{"watcher", "daemon", "removed target"} {
		t.Run(owner, func(t *testing.T) {
			reviewTestHome(t, "default")
			w := NewOpenCodeSSEWatcher(nil)
			tr := &openCodeBlockingTransport{make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), sync.Once{}}
			w.client.Transport = tr
			w.Sync([]SSETarget{{InstanceID: "joined-child", Port: 1}})
			<-tr.started
			stop := w.Stop
			if owner == "daemon" {
				d := NewTransitionDaemon()
				d.openCodeWatchers = map[string]*OpenCodeSSEWatcher{"work": w}
				stop = d.shutdown
			} else if owner == "removed target" {
				stop = func() { w.Sync(nil) }
			}
			done := make(chan struct{})
			go func() { stop(); close(done) }()
			<-tr.cancelled
			select {
			case <-done:
				close(tr.release)
				<-tr.exited
				t.Fatal("Stop returned before the stream goroutine exited")
			case <-time.After(30 * time.Millisecond):
			}
			close(tr.release)
			<-done
			select {
			case <-tr.exited:
			default:
				t.Fatal("Stop did not join its stream")
			}
		})
	}
}

func TestIssue151DaemonOpenCodeProfilesAndFilters(t *testing.T) {
	reviewTestHome(t, "work")
	events := make(chan string)
	_, port := sseTestServer(t, `{}`, events)
	d := NewTransitionDaemon()
	d.openCodeWatchers = make(map[string]*OpenCodeSSEWatcher)
	d.turnLiveCheck = func(*Instance) bool { return true }
	defer d.shutdown()
	child := func(id string) *Instance {
		return &Instance{ID: id, Tool: "opencode", ParentSessionID: "parent", Status: StatusWaiting, OpenCodePort: port}
	}
	first, second := child("first-profile"), child("second-profile")
	ssh, sandbox, wrapper, stopped, queued, root := child("ssh"), child("sandbox"), child("wrapper"), child("stopped"), child("queued"), child("root")
	ssh.SSHHost = "remote"
	sandbox.Sandbox = &SandboxConfig{Enabled: true}
	wrapper.Wrapper = "wrapper {command}"
	stopped.Status, queued.Status, root.ParentSessionID = StatusStopped, StatusQueued, ""
	d.syncOpenCodeWatcher("work", []*Instance{first, ssh, sandbox, wrapper, stopped, queued, root})
	d.syncOpenCodeWatcher("other", []*Instance{second})
	waitOpenCodeDaemon(t, func() bool {
		return readOpenCodeTurn(first.ID).Stream != "" && readOpenCodeTurn(second.ID).Stream != ""
	})
	for _, inst := range []*Instance{ssh, sandbox, wrapper, stopped, queued, root} {
		if readOpenCodeTurn(inst.ID).Stream != "" {
			t.Fatalf("excluded child %q acquired a subscription", inst.ID)
		}
	}
	firstStream := readOpenCodeTurn(first.ID).Stream
	d.syncOpenCodeWatcher("other", nil)
	if got := readOpenCodeTurn(first.ID).Stream; got != firstStream {
		t.Fatal("reconciling another profile replaced the first stream")
	}
	if len(d.openCodeWatchers) != 1 {
		t.Fatalf("watchers after removing one profile's targets = %d", len(d.openCodeWatchers))
	}
	d.pruneOpenCodeWatchers(nil)
	if len(d.openCodeWatchers) != 0 {
		t.Fatal("removed profile kept a watcher")
	}
	// Standalone diagnostic passes cannot create a watcher.
	d.openCodeWatchers = nil
	d.syncOpenCodeWatcher("work", []*Instance{first})
	if d.openCodeWatchers != nil {
		t.Fatal("one-shot reconciliation started a watcher")
	}
}
