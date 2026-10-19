package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentsdance/warp-agent-session-restore/internal/proc"
	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
	"github.com/agentsdance/warp-agent-session-restore/internal/warpdb"
	"github.com/google/uuid"
)

func TestTabConfigsKeepSplitTitlesAndWindowBoundaries(t *testing.T) {
	a, s := fixture(t)
	a.Config.RestoreMode = "tabs"
	s.Layout.Windows[0].Tabs[0].Title = "quotes \" ' \\ \n\t\x01 🚀"
	s.Layout.Windows[0].Tabs[0].Color = "blue"
	other := s.Sessions[0]
	other.ID = uuid.NewString()
	other.Pane = "cccccccccccccccccccccccccccccccc"
	s.Sessions = append(s.Sessions, other)
	s.Layout.Windows = append(s.Layout.Windows, warpdb.Window{ID: 2, Tabs: []warpdb.Tab{{Root: &warpdb.Node{Pane: &warpdb.Pane{UUID: other.Pane}}}}})
	plan, err := a.prepareLaunch(s, "/snapshot with ' quotes.json", uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 2 || strings.Contains(plan.Entries[0].URI, "new_window") || !strings.HasSuffix(plan.Entries[1].URI, "?new_window=true") {
		t.Fatalf("wrong windows: %+v", plan.Entries)
	}
	text := string(plan.Files[0].Data)
	for _, want := range []string{`split = "horizontal"`, `children = ["pane_1","pane_2"]`, `type = "terminal"`, `is_focused = true`, `color = "blue"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s", want)
		}
	}
	var title string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "title = ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "title = ")), &title); err != nil {
				t.Fatal(err)
			}
		}
	}
	if title != s.Layout.Windows[0].Tabs[0].Title || strings.Contains(text, `\x01`) || strings.Contains(text, "tab_group") {
		t.Fatal("title escaping or unsupported groups")
	}
	for _, sess := range s.Sessions {
		if strings.Contains(text, sess.ID) {
			t.Fatal("agent ID embedded in config")
		}
	}
	if !strings.Contains(text, `'\"'\"'`) {
		t.Fatal("shell quote boundaries lost")
	}
	if plan.Entries[0].Pane != s.Sessions[0].Pane {
		t.Fatal("wrong acknowledgement pane")
	}
}

func TestTabDispatchResumesPartialBatchWithoutDuplicateURIs(t *testing.T) {
	a, s := fixture(t)
	a.Config.RestoreMode = "tabs"
	extra := s.Sessions[0]
	extra.ID = uuid.NewString()
	extra.Pane = "cccccccccccccccccccccccccccccccc"
	s.Sessions = append(s.Sessions, extra)
	// Missing layout panes intentionally exercise the fallback tab path.
	calls := []string{}
	fail := true
	a.launch = func(uri string) error {
		calls = append(calls, uri)
		var d restoreState
		if err := state.Load(a.restoreStatePath(), &d); err != nil {
			t.Fatal(err)
		}
		if len(calls) == 2 && fail {
			return errors.New("open failed")
		}
		entry := d.Entries[d.Next-1]
		return state.Write(a.openedPath(d.Batch, entry.Pane), []byte("opened"), 0600)
	}
	if _, err := a.autoRestore(s, "after"); err == nil {
		t.Fatal("missing partial failure")
	}
	var partial restoreState
	state.Load(a.restoreStatePath(), &partial)
	if partial.Next != 1 {
		t.Fatalf("progress %d", partial.Next)
	}
	fail = false
	restarted := *a
	if ok, err := restarted.autoRestore(s, "after"); err != nil || !ok {
		t.Fatalf("retry %v %v", ok, err)
	}
	if len(calls) != 3 || calls[0] == calls[2] || calls[1] != calls[2] {
		t.Fatal("successful tab redispatched", calls)
	}
	if ok, err := restarted.autoRestore(s, "after"); err != nil || ok || len(calls) != 3 {
		t.Fatal("completed batch repeated")
	}
	var complete restoreState
	state.Load(a.restoreStatePath(), &complete)
	if complete.Batch != partial.Batch {
		t.Fatal("retry changed immutable recovery batch")
	}
}

func TestPendingRecoveryRequiresExactSessionAndLivePane(t *testing.T) {
	a, s := fixture(t)
	batch := uuid.NewString()
	if err := state.Save(filepath.Join(a.Config.StateDir, "recoveries", batch+".json"), s); err != nil {
		t.Fatal(err)
	}
	state.Save(a.restoreStatePath(), restoreState{WarpID: "after", Batch: batch})
	next := *s
	next.WarpID = "after"
	next.Sessions = nil
	if err := a.verifyRecovery(s, &next); err == nil {
		t.Fatal("empty capture accepted")
	}
	next.Sessions = append([]session.Session(nil), s.Sessions...)
	next.Sessions[0].Pane = "cccccccccccccccccccccccccccccccc"
	if err := a.verifyRecovery(s, &next); err == nil {
		t.Fatal("stale pane accepted")
	}
	next.Layout = &warpdb.Layout{Windows: []warpdb.Window{{Tabs: []warpdb.Tab{{Root: &warpdb.Node{Pane: &warpdb.Pane{UUID: next.Sessions[0].Pane}}}}}}}
	if err := a.verifyRecovery(s, &next); err != nil {
		t.Fatal(err)
	}
	var done restoreState
	state.Load(a.restoreStatePath(), &done)
	if !done.Complete {
		t.Fatal("verified recovery not recorded")
	}
	live := sessionsInWarp([]session.Session{{PID: 11}, {PID: 12}}, []proc.Process{{PID: 10, PPID: 1}, {PID: 11, PPID: 10}, {PID: 12, PPID: 1}}, "10-123")
	if len(live) != 1 || live[0].PID != 11 {
		t.Fatal("orphan counted as restored")
	}
}

func TestTabConfigVersionGate(t *testing.T) {
	for version, want := range map[string]bool{"0.2026.10.14.08.27.01": true, "0.2026.10.15.01.00.00": true, "0.2026.10.13.08.27.01": false, "bad": false, "0.2026.10.14.08.27.bad": false} {
		if got := supportsTabConfigs(version); got != want {
			t.Fatalf("%s: %v", version, got)
		}
	}
	a, s := fixture(t)
	a.Config.RestoreMode = "tabs"
	if _, err := a.Restore(s, "after", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Config.StateDir); !os.IsNotExist(err) {
		t.Fatal("dry run wrote state")
	}
}
