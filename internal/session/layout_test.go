package session

import (
	"github.com/agentsdance/warp-agent-session-restore/internal/warpdb"
	"testing"
	"time"
)

func leaf(id string) *warpdb.Node { return &warpdb.Node{Pane: &warpdb.Pane{UUID: id}} }
func layout(tabs ...warpdb.Tab) *warpdb.Layout {
	return &warpdb.Layout{Windows: []warpdb.Window{{ID: 1, Tabs: tabs}}}
}
func TestLiveAgentsKeepMissingSplitAndNewTabs(t *testing.T) {
	now := time.Now()
	old := &Snapshot{WarpID: "warp", Layout: layout(warpdb.Tab{ID: 1, Title: "original", Root: &warpdb.Node{Horizontal: true, Children: []*warpdb.Node{leaf("a"), leaf("b")}}}), Sessions: []Session{{Pane: "a"}, {Pane: "b"}}}
	next := &Snapshot{WarpID: "warp", Layout: layout(warpdb.Tab{ID: 2, Title: "new tab", Root: leaf("c")}), Sessions: []Session{{Pane: "a"}, {Pane: "b"}, {Pane: "c"}}}
	Merge(old, next, now, 20*time.Second)
	if len(next.Layout.Panes()) != 3 || len(next.Layout.Windows[0].Tabs) != 2 {
		t.Fatal("lost old split or new tab")
	}
	tabs := next.Layout.Windows[0].Tabs
	if tabs[0].Title != "new tab" || tabs[1].Title != "original" || !tabs[1].Root.Horizontal || len(tabs[1].Root.Children) != 2 {
		t.Fatal("split/title lost")
	}
	if len(old.Layout.Panes()) != 2 {
		t.Fatal("mutated history")
	}
}
func TestMissingPaneDoesNotUndoMoveOrTitleChange(t *testing.T) {
	now := time.Now()
	old := &Snapshot{WarpID: "warp", Layout: layout(warpdb.Tab{ID: 1, Title: "old", Root: &warpdb.Node{Horizontal: true, Children: []*warpdb.Node{leaf("a"), leaf("b")}}}), Sessions: []Session{{Pane: "a"}, {Pane: "b"}}}
	next := &Snapshot{WarpID: "warp", Layout: layout(warpdb.Tab{ID: 1, Title: "renamed", Root: leaf("c")}, warpdb.Tab{ID: 2, Title: "moved", Root: leaf("a")}), Sessions: []Session{{Pane: "a"}, {Pane: "b"}, {Pane: "c"}}}
	Merge(old, next, now, 20*time.Second)
	counts := map[string]int{}
	for _, p := range next.Layout.Panes() {
		counts[p.UUID]++
	}
	for _, id := range []string{"a", "b", "c"} {
		if counts[id] != 1 {
			t.Fatal("lost/duplicated pane", counts)
		}
	}
	if next.Layout.Windows[0].Tabs[0].Title != "renamed" || next.Layout.Windows[0].Tabs[1].Root.Pane.UUID != "a" {
		t.Fatal("latest title or move undone")
	}
}
func TestRetainedLayoutExpiresWithClosedSession(t *testing.T) {
	now := time.Now()
	old := &Snapshot{WarpID: "warp", Layout: layout(warpdb.Tab{ID: 1, Root: leaf("a")}), Sessions: []Session{{Pane: "a"}}}
	next := &Snapshot{WarpID: "warp", Layout: layout(warpdb.Tab{ID: 2, Root: leaf("b")})}
	Merge(old, next, now, 20*time.Second)
	if len(next.Sessions) != 1 || len(next.Layout.Panes()) != 2 {
		t.Fatal("grace failed")
	}
	expired := &Snapshot{WarpID: "warp", Layout: layout(warpdb.Tab{ID: 2, Root: leaf("b")})}
	Merge(next, expired, now.Add(21*time.Second), 20*time.Second)
	if len(expired.Sessions) != 0 || len(expired.Layout.Panes()) != 1 {
		t.Fatal("closed session/layout retained forever")
	}
}
