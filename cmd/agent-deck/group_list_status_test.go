package main

import (
	"encoding/json"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestBuildGroupListJSON_CountsStatusWithoutRefreshing pins that the group
// counts read the status the caller's listing pass already produced. A
// refresh inside the builder ran the full UpdateStatus per session and per
// enclosing group, whose metadata sync can run the opencode CLI and made every
// remote poll of such a host take seconds.
func TestBuildGroupListJSON_CountsStatusWithoutRefreshing(t *testing.T) {
	inst := session.NewInstanceWithGroup("w1", t.TempDir(), "work")
	inst.Status = session.StatusWaiting
	tree := session.NewGroupTreeWithGroups([]*session.Instance{inst}, nil)

	out, err := buildGroupListJSON(tree)
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Groups []struct {
			Path   string         `json:"path"`
			Status map[string]int `json:"status"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal group list: %v\n%s", err, out)
	}
	if len(resp.Groups) != 1 || resp.Groups[0].Path != "work" {
		t.Fatalf("groups = %+v, want the one group work", resp.Groups)
	}
	want := map[string]int{"running": 0, "waiting": 1, "idle": 0, "error": 0, "stopped": 0}
	got := resp.Groups[0].Status
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("work status = %v, want %v", got, want)
		}
	}
	if inst.Status != session.StatusWaiting {
		t.Fatalf("instance status = %q after building the listing, want it untouched (waiting)", inst.Status)
	}
}
