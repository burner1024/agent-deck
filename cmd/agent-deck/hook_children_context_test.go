package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func saveContextFleet(t *testing.T, children ...*session.Instance) string {
	t.Helper()
	parent := session.NewInstance("parent", t.TempDir())
	instances := []*session.Instance{parent}
	for _, child := range children {
		child.ParentSessionID = parent.ID
		instances = append(instances, child)
	}
	storage, err := session.NewStorageWithProfile("")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storage.Close() }()
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, nil)); err != nil {
		t.Fatal(err)
	}
	return parent.ID
}

func TestChildrenContextExcludesArchived(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "all archived", true: "mixed fleet"}[active], func(t *testing.T) {
			cliInboxTestHome(t)
			archived := session.NewInstance("archived result", t.TempDir())
			archived.SetArchivedAtThreadSafe(time.Now())
			if err := session.WriteLedgerEntry(session.CompletionLedgerEntry{ChildID: archived.ID, Status: "ok", Summary: "old result"}); err != nil {
				t.Fatal(err)
			}
			children := []*session.Instance{archived}
			if active {
				children = append(children, session.NewInstance("active child", t.TempDir()))
			}
			parentID := saveContextFleet(t, children...)
			got := buildChildrenContextSummary(parentID, false)
			if !active {
				if got != "" {
					t.Fatalf("all archived must be silent: %q", got)
				}
				return
			}
			if !strings.HasPrefix(got, "[agent-deck fleet] 1 children:") || !strings.Contains(got, "0 done") {
				t.Errorf("archived child entered counts: %q", got)
			}
			if strings.Contains(got, "archived result") || strings.Contains(got, "old result") {
				t.Errorf("archived completion listed: %q", got)
			}
		})
	}
}

func TestChildrenContextCompletionOnce(t *testing.T) {
	cliInboxTestHome(t)
	child := session.NewInstance("completed child", t.TempDir())
	parentID := saveContextFleet(t, child)
	entry := session.CompletionLedgerEntry{ChildID: child.ID, Status: "ok", Summary: "result", FinishedAt: time.Now().Add(-time.Hour), TurnUUID: "result-1"}
	if err := session.WriteLedgerEntry(entry); err != nil {
		t.Fatal(err)
	}
	queueResult := func() {
		t.Helper()
		if err := session.WriteInboxEvent(parentID, session.TransitionNotificationEvent{
			ChildSessionID: child.ID, Kind: "finished", DoneStatus: entry.Status,
			DoneSummary: entry.Summary, Timestamp: entry.FinishedAt,
			TurnUUID: entry.TurnUUID, LastOutputHash: "turn:" + entry.TurnUUID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	queueResult()
	if got := buildChildrenContextSummary(parentID, false); !strings.Contains(got, "completed: completed child") {
		t.Fatalf("first prompt must list completion: %q", got)
	}
	if events, err := session.ReadInboxEvents(parentID); err != nil || len(events) != 1 {
		t.Fatalf("summary must leave the full inbox result pending: events=%v, err=%v", events, err)
	}
	for _, force := range []bool{false, true, false} {
		if got := buildChildrenContextSummary(parentID, force); strings.Contains(got, "completed:") {
			t.Errorf("completion repeated (force=%v): %q", force, got)
		}
	}
	// A later completion with the same text still needs a new notice.
	entry.FinishedAt = entry.FinishedAt.Add(time.Nanosecond)
	if err := session.WriteLedgerEntry(entry); err != nil {
		t.Fatal(err)
	}
	if got := buildChildrenContextSummary(parentID, false); !strings.Contains(got, "completed: completed child") {
		t.Fatalf("new completion must be listed: %q", got)
	}
	if got := buildChildrenContextSummary(parentID, true); strings.Contains(got, "completed:") {
		t.Errorf("new completion repeated: %q", got)
	}
	if events, err := session.DrainInboxForParent(parentID); err != nil || len(events) != 1 {
		t.Fatalf("summary must not consume the full result: events=%v, err=%v", events, err)
	}
	// A full result already delivered by the inbox needs no fleet bullet.
	entry.TurnUUID = "result-2"
	if err := session.WriteLedgerEntry(entry); err != nil {
		t.Fatal(err)
	}
	queueResult()
	if events, err := session.DrainInboxForParent(parentID); err != nil || len(events) != 1 {
		t.Fatalf("drain next result: events=%v, err=%v", events, err)
	}
	if got := buildChildrenContextSummary(parentID, true); strings.Contains(got, "completed:") {
		t.Errorf("inbox acknowledgement ignored: %q", got)
	}
	entry.TurnUUID = "result-3"
	if err := session.WriteLedgerEntry(entry); err != nil {
		t.Fatal(err)
	}
	if err := session.NoteResultRead(parentID, child.ID, entry.TurnUUID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := buildChildrenContextSummary(parentID, true); strings.Contains(got, "completed:") {
		t.Errorf("result read acknowledgement ignored: %q", got)
	}
}

func TestChildrenContextCompletionOverflow(t *testing.T) {
	cliInboxTestHome(t)
	var children []*session.Instance
	for i := 0; i < maxContextBullets+2; i++ {
		child := session.NewInstance("result", t.TempDir())
		children = append(children, child)
		if err := session.WriteLedgerEntry(session.CompletionLedgerEntry{ChildID: child.ID, Status: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	parentID := saveContextFleet(t, children...)
	seen := make(map[string]bool)
	for _, want := range []int{maxContextBullets, 2, 0} {
		got := buildChildrenContextSummary(parentID, true)
		if count := strings.Count(got, "\n- completed:"); count != want {
			t.Errorf("completed bullets = %d, want %d: %q", count, want, got)
		}
		for _, child := range children {
			if strings.Contains(got, "session output "+child.ID+" --json") {
				if seen[child.ID] {
					t.Errorf("completion repeated for %s", child.ID)
				}
				seen[child.ID] = true
			}
		}
	}
	if len(seen) != len(children) {
		t.Errorf("listed %d distinct completions, want %d", len(seen), len(children))
	}
}

func TestFormatChildrenContext(t *testing.T) {
	t.Run("no children yields empty", func(t *testing.T) {
		if got := formatChildrenContext(nil); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("all running yields header only", func(t *testing.T) {
		rows := []childRow{
			{ID: "a1", Title: "lint-a", Status: "running"},
			{ID: "b2", Title: "lint-b", Status: "running"},
		}
		got := formatChildrenContext(rows)
		if !strings.Contains(got, "2 children") || !strings.Contains(got, "2 running") {
			t.Errorf("missing counts in %q", got)
		}
		if strings.Contains(got, "\n- ") {
			t.Errorf("no bullets expected for running-only fleet: %q", got)
		}
	})

	t.Run("waiting child gets an actionable bullet", func(t *testing.T) {
		rows := []childRow{
			{ID: "a1", Title: "lint-a", Status: "waiting"},
		}
		got := formatChildrenContext(rows)
		if !strings.Contains(got, "waiting on input: lint-a") {
			t.Errorf("missing waiting bullet in %q", got)
		}
		if !strings.Contains(got, "session output a1") || !strings.Contains(got, "session send a1") {
			t.Errorf("missing next-step commands in %q", got)
		}
	})

	t.Run("done child shows sentinel status and summary", func(t *testing.T) {
		rows := []childRow{
			{ID: "a1", Title: "lint-a", Status: "idle", DoneStatus: "fail", DoneSummary: "tests broke"},
		}
		got := formatChildrenContext(rows)
		if !strings.Contains(got, "completed: lint-a") || !strings.Contains(got, "fail") || !strings.Contains(got, "tests broke") {
			t.Errorf("missing done details in %q", got)
		}
		if !strings.Contains(got, "session output a1 --json") {
			t.Errorf("collect command should target the ID in %q", got)
		}
	})

	// The bullets are injected into an agent's context as runnable commands, so
	// they must target the ID: a title is a display string that may contain
	// spaces (and may repeat across sessions under allow_multiple), which would
	// make the command parse as the wrong session or fail outright.
	t.Run("commands target the id, not a title with spaces", func(t *testing.T) {
		rows := []childRow{
			{ID: "a1", Title: "fix the flaky test", Status: "waiting"},
			{ID: "b2", Title: "ship it", Status: "idle", DoneStatus: "ok"},
		}
		got := formatChildrenContext(rows)
		for _, want := range []string{
			"session output a1 --json", "session send a1", "session output b2 --json",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
		for _, unwanted := range []string{
			"session output fix the flaky test", "session send fix the flaky test",
			"session output ship it",
		} {
			if strings.Contains(got, unwanted) {
				t.Errorf("command built from a title with spaces: %q in %q", unwanted, got)
			}
		}
		// The titles still name the children for the reader.
		if !strings.Contains(got, "waiting on input: fix the flaky test") ||
			!strings.Contains(got, "completed: ship it") {
			t.Errorf("titles should remain as display names in %q", got)
		}
	})

	t.Run("mixed fleet counts each state", func(t *testing.T) {
		rows := []childRow{
			{ID: "a1", Title: "a", Status: "running"},
			{ID: "b2", Title: "b", Status: "waiting"},
			{ID: "c3", Title: "c", Status: "idle", DoneStatus: "ok", DoneSummary: "done"},
		}
		got := formatChildrenContext(rows)
		for _, want := range []string{"3 children", "1 running", "1 waiting", "1 done"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %q", want, got)
			}
		}
		if strings.Contains(got, "stopped/other") {
			t.Errorf("no other bucket expected in %q", got)
		}
	})

	t.Run("stopped children surface in the other bucket", func(t *testing.T) {
		rows := []childRow{
			{ID: "a1", Title: "a", Status: "stopped"},
			{ID: "b2", Title: "b", Status: "running"},
		}
		got := formatChildrenContext(rows)
		if !strings.Contains(got, "1 stopped/other") {
			t.Errorf("missing other bucket in %q", got)
		}
	})

	t.Run("bullet lists are capped", func(t *testing.T) {
		var rows []childRow
		for i := 0; i < 12; i++ {
			rows = append(rows, childRow{ID: string(rune('a' + i)), Title: "w", Status: "waiting"})
		}
		got := formatChildrenContext(rows)
		if strings.Count(got, "waiting on input:") > maxContextBullets {
			t.Errorf("expected at most %d waiting bullets: %q", maxContextBullets, got)
		}
		if !strings.Contains(got, "more") {
			t.Errorf("expected overflow marker in %q", got)
		}
	})
}

func TestClaudeContextEventName(t *testing.T) {
	cases := map[string]string{
		"UserPromptSubmit": "UserPromptSubmit",
		"SessionStart":     "SessionStart",
		"Stop":             "",
		"PreToolUse":       "",
		"before_agent":     "",
	}
	for in, want := range cases {
		if got := claudeContextEventName(in); got != want {
			t.Errorf("claudeContextEventName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChildrenContextJSON(t *testing.T) {
	out := childrenContextJSON("UserPromptSubmit", "fleet: 1 running")
	var m map[string]map[string]string
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	hso := m["hookSpecificOutput"]
	if hso["hookEventName"] != "UserPromptSubmit" {
		t.Errorf("wrong hookEventName: %q", out)
	}
	if hso["additionalContext"] != "fleet: 1 running" {
		t.Errorf("wrong additionalContext: %q", out)
	}
}
