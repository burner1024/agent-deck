package statedb

import "testing"

func TestLoadInstanceIDByTmuxSession(t *testing.T) {
	db := newTestDB(t)
	for _, row := range []*InstanceRow{
		{ID: "pane-a", Title: "a", TmuxSession: "tmux-a"},
		{ID: "pane-b", Title: "b", TmuxSession: "tmux-b"},
	} {
		if err := db.InsertInstanceRow(row); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := OpenReadOnlyLive(db.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, tc := range []struct{ tmux, want string }{
		{"tmux-a", "pane-a"}, {"tmux-b", "pane-b"}, {"unowned", ""}, {"' OR 1=1 --", ""},
	} {
		t.Run(tc.tmux, func(t *testing.T) {
			got, err := reader.LoadInstanceIDByTmuxSession(tc.tmux)
			if err != nil || got != tc.want {
				t.Fatalf("lookup = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
