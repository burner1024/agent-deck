package session

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIssue151OpenCodeFreshToken(t *testing.T) {
	for _, tc := range []struct {
		name, command, resume    string
		remote, sandbox, wrapper bool
		mode                     string
		want                     bool
	}{
		{name: "fresh", command: "opencode", want: true},
		{name: "resume", command: "opencode", resume: "ses_old"},
		{name: "fork", command: "opencode --session ses_old --fork"},
		{name: "ssh", command: "opencode", remote: true},
		{name: "sandbox", command: "opencode", sandbox: true},
		{name: "wrapper", command: "opencode", wrapper: true},
		{name: "continue", command: "opencode", mode: "continue"},
		{name: "resume option", command: "opencode", mode: "resume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reviewTestHome(t, "default")
			i := &Instance{ID: "fresh-open", Tool: "opencode", OpenCodeSessionID: tc.resume}
			if tc.remote {
				i.SSHHost = "remote"
			}
			if tc.sandbox {
				i.Sandbox = &SandboxConfig{Enabled: true}
			}
			if tc.wrapper {
				i.Wrapper = "wrapper {command}"
			}
			if tc.mode != "" {
				if err := i.SetOpenCodeOptions(&OpenCodeOptions{SessionMode: tc.mode, ResumeSessionID: "ses_old"}); err != nil {
					t.Fatal(err)
				}
			}
			i.buildOpenCodeCommand(tc.command)
			noteFreshStart(i, time.Now())
			if got := hasFreshStartToken(i.ID); got != tc.want {
				t.Fatalf("token = %v, want %v", got, tc.want)
			}
		})
	}
}

// A token written before the stream connects must distinguish an empty history
// from a turn completed before subscription. These are real SSE decoder paths.
func TestIssue151OpenCodeBootAndTurn(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint("shared=", shared), func(t *testing.T) {
			f := newTurnTestFixture(t)
			st := stubRestartPIDs(t)
			st.substate = string(SubstateIdleAtEmptyPrompt)
			f.child.Tool = "opencode"
			f.child.ClaudeSessionID = ""
			at := time.Now().Add(-time.Second)
			writeTokenFile(freshStartDir, f.child.ID, ".json", struct {
				At       time.Time `json:"at"`
				OpenCode struct {
					TurnID string `json:"turn_id"`
				} `json:"opencode"`
			}{At: at})
			events := make(chan string, 16)
			connected := make(chan struct{})
			mux := http.NewServeMux()
			eventPath, messagePath := "/event", "/session/ses_new/message"
			if shared {
				eventPath = "/api/event"
				messagePath = "/api/session/ses_new/message"
			}
			mux.HandleFunc(messagePath, func(w http.ResponseWriter, r *http.Request) {
				if shared {
					fmt.Fprint(w, `{"data":[]}`)
				} else {
					fmt.Fprint(w, `[]`)
				}
			})
			mux.HandleFunc("/session/status", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
			mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) })
			mux.HandleFunc("/api/session/active", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":{}}`) })
			mux.HandleFunc(eventPath, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"server.connected\"}\n\n")
				w.(http.Flusher).Flush()
				close(connected)
				for {
					select {
					case ev := <-events:
						fmt.Fprintf(w, "data: %s\n\n", ev)
						w.(http.Flusher).Flush()
					case <-r.Context().Done():
						return
					}
				}
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			w := NewOpenCodeSSEWatcher(nil)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			src := openCodeTUIServerSource(1)
			src.base = srv.URL
			if shared {
				src = openCodeSharedServiceSource(openCodeService{URL: srv.URL}, "ses_new")
			}
			go func() { defer close(done); w.stream(ctx, f.child.ID, src) }()
			defer func() { cancel(); <-done }()
			<-connected
			events <- `{"type":"session.idle","properties":{"sessionID":"ses_new"}}`
			deadline := time.Now().Add(5 * time.Second)
			var verdict freshStartVerdict
			for {
				_, verdict = new(TransitionDaemon).judgeFreshStart("", f.child, "idle", time.Now(), st.substate)
				if verdict != freshStartNotYet || time.Now().After(deadline) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if verdict != freshStartBoot {
				t.Fatalf("first idle verdict = %v, want boot", verdict)
			}
			// Settle the banner and initial idle, plus the shared snapshot's
			// status write, before counting subsequent event writes.
			initialWrites := int64(2)
			if shared {
				initialWrites++
			}
			for w.writes.Load() < initialWrites {
				if time.Now().After(deadline) {
					t.Fatal("initial stream events did not settle")
				}
				time.Sleep(time.Millisecond)
			}
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
			if got := f.pending(t); len(got) != 1 || got[0] != freshStartLine {
				t.Fatalf("boot records = %q", got)
			}
			if *f.sends != 0 || InboxHasUrgentPending(f.parent.ID) {
				t.Fatal("boot woke parent")
			}
			// Liveness and edits of a title are not turns.
			send := func(event string) { events <- event }
			sendAndSettle(t, w, send, `{"type":"session.updated","properties":{"info":{"id":"ses_new"}}}`)
			if _, got := new(TransitionDaemon).judgeFreshStart("", f.child, "idle", time.Now(), st.substate); got != freshStartBoot {
				t.Fatalf("liveness spent token: %v", got)
			}
			if shared {
				sendAndSettle(t, w, send, sharedServiceEvent("session.execution.started", "ses_new"))
			} else {
				sendAndSettle(t, w, send, `{"type":"message.updated","properties":{"info":{"id":"msg_first","sessionID":"ses_new","role":"user"}}}`)
			}
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
			if got := f.pending(t); len(got) != 1 {
				t.Fatalf("running turn published as complete: %q", got)
			}
			if shared {
				sendAndSettle(t, w, send, sharedServiceEvent("session.execution.succeeded", "ses_new"))
			} else {
				sendAndSettle(t, w, send, `{"type":"session.idle","properties":{"sessionID":"ses_new"}}`)
			}
			// Busy -> idle can occur between polls, with no output-size signal.
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
			got := f.pending(t)
			if len(got) != 2 || !strings.HasPrefix(got[1], "urgent:") {
				t.Fatalf("first turn records = %q", got)
			}
			if hasFreshStartToken(f.child.ID) {
				t.Fatal("first turn retained token")
			}
			f.restartDaemon()
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
			if got := f.pending(t); len(got) != 2 {
				t.Fatalf("duplicate after restart: %q", got)
			}
			// Read the status through its public copy, including the decoded identity.
			data, err := json.Marshal(w.GetStatus(f.child.ID))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "msg_first") && !strings.Contains(string(data), "evt_test") {
				t.Fatalf("status lost turn identity: %s", data)
			}
			// Spending the startup token must not freeze later turn keys.
			if shared {
				sendAndSettle(t, w, send, strings.ReplaceAll(sharedServiceEvent("session.execution.started", "ses_new"), "evt_test", "evt_next_start"), strings.ReplaceAll(sharedServiceEvent("session.execution.succeeded", "ses_new"), "evt_test", "evt_next_end"))
			} else {
				sendAndSettle(t, w, send, `{"type":"message.updated","properties":{"info":{"id":"msg_next","sessionID":"ses_new","role":"user"}}}`, `{"type":"session.idle","properties":{"sessionID":"ses_new"}}`)
			}
			f.d.recordTerminalTurns("default", f.byID, f.idle(), nil)
			if got := f.pending(t); len(got) != 3 || !strings.HasPrefix(got[2], "urgent:") {
				t.Fatalf("next turn records = %q", got)
			}
		})
	}
}

func TestIssue151OpenCodeBootBounds(t *testing.T) {
	for _, operation := range []string{"timeout", "stop", "remove", "cancelled event"} {
		t.Run(operation, func(t *testing.T) {
			reviewTestHome(t, "default")
			i := &Instance{ID: "boot-bounds", Tool: "opencode"}
			at := time.Now()
			if operation == "timeout" {
				at = at.Add(-restartBootTimeout() - time.Second)
			}
			writeTokenFile(freshStartDir, i.ID, ".json", freshStartToken{At: at, OpenCode: &freshStartOpenCodeTurn{}})
			w := NewOpenCodeSSEWatcher(nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w.observeOpenCodeTurn(ctx, i.ID, "subscription", true, nil)
			w.observeOpenCodeTurn(ctx, i.ID, "subscription", false, func(obs *openCodeTurnObservation) { obs.Ready = true })
			want := freshStartNotYet
			switch operation {
			case "timeout":
				want = freshStartNews
			case "stop", "remove":
				w.conns[i.ID] = &openCodeSSEConn{cancel: cancel}
				if operation == "stop" {
					w.Stop()
				} else {
					w.Sync(nil)
				}
			case "cancelled event":
				late, cancelLate := context.WithCancel(ctx)
				cancelLate()
				w.observeOpenCodeTurn(late, i.ID, "subscription", false, func(obs *openCodeTurnObservation) { obs.TurnID = "msg_late"; obs.Ran = true })
				want = freshStartBoot
			}
			if _, got := new(TransitionDaemon).judgeFreshStart("", i, "idle", time.Now(), string(SubstateIdleAtEmptyPrompt)); got != want {
				t.Fatalf("verdict = %v, want %v", got, want)
			}
		})
	}
}

func TestIssue151OpenCodeHistoryProof(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, tc := range []struct {
			name, history string
			code          int
			want          freshStartVerdict
		}{
			{"empty", `[]`, http.StatusOK, freshStartBoot},
			{"completed before subscription", `[{"id":"msg_done"}]`, http.StatusOK, freshStartNews},
			{"missing array", `null`, http.StatusOK, freshStartNotYet},
			{"malformed", `{`, http.StatusOK, freshStartNotYet},
			{"unavailable", `[]`, http.StatusServiceUnavailable, freshStartNotYet},
		} {
			t.Run(fmt.Sprintf("shared=%v/%s", shared, tc.name), func(t *testing.T) {
				reviewTestHome(t, "default")
				i := &Instance{ID: "history-proof", Tool: "opencode"}
				at := time.Now().Add(-time.Second)
				writeTokenFile(freshStartDir, i.ID, ".json", freshStartToken{At: at, OpenCode: &freshStartOpenCodeTurn{}})
				mux := http.NewServeMux()
				mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprintf(w, `[{"id":"ses_new","time":{"created":%d}}]`, at.UnixMilli()+1)
				})
				path := "/session/ses_new/message"
				if shared {
					path = "/api" + path
				}
				mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(tc.code)
					if shared && tc.history != "{" {
						fmt.Fprintf(w, `{"data":%s}`, tc.history)
					} else {
						fmt.Fprint(w, tc.history)
					}
				})
				srv := httptest.NewServer(mux)
				defer srv.Close()
				src := openCodeTUIServerSource(1)
				src.base = srv.URL
				if shared {
					src = openCodeSharedServiceSource(openCodeService{URL: srv.URL}, "ses_new")
				}
				w := NewOpenCodeSSEWatcher(nil)
				ctx := context.Background()
				w.observeOpenCodeTurn(ctx, i.ID, "subscription", true, nil)
				w.seedOpenCodeFreshStart(ctx, i.ID, "subscription", src)
				_, got := new(TransitionDaemon).judgeFreshStart("", i, "idle", time.Now(), string(SubstateIdleAtEmptyPrompt))
				if got != tc.want {
					t.Fatalf("verdict = %v, want %v", got, tc.want)
				}
				if tc.want == freshStartBoot {
					// An old subscription's teardown cannot revoke a newer proof.
					w.observeOpenCodeTurn(ctx, i.ID, "replacement", true, nil)
					w.seedOpenCodeFreshStart(ctx, i.ID, "replacement", src)
					w.observeOpenCodeTurn(ctx, i.ID, "subscription", false, func(obs *openCodeTurnObservation) { obs.Ready = false })
					if _, got := new(TransitionDaemon).judgeFreshStart("", i, "idle", time.Now(), string(SubstateIdleAtEmptyPrompt)); got != freshStartBoot {
						t.Fatalf("replaced stream erased proof: %v", got)
					}
					w.observeOpenCodeTurn(ctx, i.ID, "replacement", false, func(obs *openCodeTurnObservation) { obs.Ready = false })
					if _, got := new(TransitionDaemon).judgeFreshStart("", i, "idle", time.Now(), string(SubstateIdleAtEmptyPrompt)); got != freshStartNotYet {
						t.Fatalf("disconnected stream retained proof: %v", got)
					}
				}
			})
		}
	}
}
