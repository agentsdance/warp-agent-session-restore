package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
	"github.com/agentsdance/warp-agent-session-restore/internal/warpdb"
	"github.com/google/uuid"
)

func recoveryPair(t *testing.T) (*App, *session.Snapshot, *session.Snapshot, restoreState) {
	t.Helper()
	a, saved := fixture(t)
	other := saved.Sessions[0]
	other.ID, other.Pane = uuid.NewString(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	saved.Sessions = append(saved.Sessions, other)
	next := *saved
	next.WarpID, next.Taken = "after", time.Now()
	next.Sessions = append([]session.Session(nil), saved.Sessions[:1]...)
	done := restoreState{WarpID: "after", Batch: uuid.NewString()}
	return a, saved, &next, done
}

func TestRecoveryRemembersVerifiedSessionsAcrossWatcherRestarts(t *testing.T) {
	a, saved, next, done := recoveryPair(t)
	state.Save(filepath.Join(a.Config.StateDir, "recoveries", done.Batch+".json"), saved)
	state.Save(a.restoreStatePath(), done)
	if err := a.verifyRecovery(saved, next); err == nil {
		t.Fatal("partial recovery accepted")
	}
	// The first conversation was verified and then closed before the second
	// finished loading. They need not remain open simultaneously.
	next.Sessions = append([]session.Session(nil), saved.Sessions[1:]...)
	restarted := *a
	if err := restarted.verifyRecovery(saved, next); err != nil {
		t.Fatal(err)
	}
	state.Load(a.restoreStatePath(), &done)
	if !done.Complete || len(done.Progress) != 2 {
		t.Fatalf("lost recovery progress: %+v", done)
	}
}

func TestRecoveryRetiresClosedAttemptOnlyWithEvidenceAndGrace(t *testing.T) {
	_, saved, next, done := recoveryPair(t)
	missing := saved.Sessions[1]
	key := missing.Agent + ":" + missing.ID
	receipt := recoveryReceipt{WarpID: "after", Agent: missing.Agent, ID: missing.ID, Pane: "cccccccccccccccccccccccccccccccc", PID: 123}
	for _, tc := range []struct {
		name       string
		receipt    recoveryReceipt
		alive      bool
		empty      bool
		wantClosed bool
	}{
		{"closed", receipt, false, false, true},
		{"child still alive", receipt, true, false, false},
		{"empty capture during shutdown", receipt, false, true, false},
		{"legacy marker has no receipt", recoveryReceipt{}, false, false, false},
		{"receipt from another Warp lifetime", recoveryReceipt{WarpID: "before", Agent: missing.Agent, ID: missing.ID, Pane: receipt.Pane, PID: 123}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := done
			current := *next
			if tc.empty {
				current.Sessions = nil
				current.Layout = &warpdb.Layout{}
			}
			receipts := map[string]recoveryReceipt{key: tc.receipt}
			alive := map[int]bool{123: tc.alive}
			advanceRecovery(&d, saved, &current, receipts, alive, 20*time.Second)
			if d.Progress[key].Closed {
				t.Fatal("closed before grace")
			}
			current.Taken = current.Taken.Add(21 * time.Second)
			advanceRecovery(&d, saved, &current, receipts, alive, 20*time.Second)
			if d.Progress[key].Closed != tc.wantClosed || d.Progress[key].Verified {
				t.Fatalf("wrong retirement: %+v", d.Progress[key])
			}
		})
	}
}

func TestRecoveryDoesNotRetireOpenOrTemporarilyMissingPane(t *testing.T) {
	_, saved, next, done := recoveryPair(t)
	s := saved.Sessions[1]
	key := s.Agent + ":" + s.ID
	receipts := map[string]recoveryReceipt{key: {WarpID: "after", Agent: s.Agent, ID: s.ID, Pane: s.Pane, PID: 123}}
	// The target pane is still open even if its child cannot be identified.
	advanceRecovery(&done, saved, next, receipts, nil, 20*time.Second)
	next.Taken = next.Taken.Add(time.Minute)
	advanceRecovery(&done, saved, next, receipts, nil, 20*time.Second)
	if done.Progress[key].Closed || !done.Progress[key].MissingSince.IsZero() {
		t.Fatal("retired an open pane")
	}
	full := next.Layout
	next.Layout = &warpdb.Layout{Windows: []warpdb.Window{{Tabs: []warpdb.Tab{{Root: &warpdb.Node{Pane: &warpdb.Pane{UUID: next.Sessions[0].Pane}}}}}}}
	advanceRecovery(&done, saved, next, receipts, nil, 20*time.Second)
	next.Taken = next.Taken.Add(10 * time.Second)
	next.Layout = full
	advanceRecovery(&done, saved, next, receipts, nil, 20*time.Second)
	if !done.Progress[key].MissingSince.IsZero() {
		t.Fatal("temporary loss did not reset grace")
	}
}

func TestAcceptCurrentArchivesOriginalRecovery(t *testing.T) {
	a, saved, _, done := recoveryPair(t)
	if err := state.Save(a.Config.SnapshotPath(), saved); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(a.restoreStatePath(), done); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(a.restoreStatePath())
	if err := a.archiveRecovery(saved); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(a.Config.StateDir, "reconciliations", "*", "snapshot.json"))
	if len(paths) != 1 {
		t.Fatalf("missing archive: %v", paths)
	}
	var backup session.Snapshot
	if err := state.Load(paths[0], &backup); err != nil || !equivalent(saved, &backup) {
		t.Fatalf("snapshot not preserved: %v", err)
	}
	copy, err := os.ReadFile(filepath.Join(filepath.Dir(paths[0]), "restore-state.json"))
	if err != nil || string(copy) != string(before) {
		t.Fatal("recovery state not preserved")
	}
}

func TestStatusSeparatesSavedSessionsFromLiveTabs(t *testing.T) {
	_, saved, live, _ := recoveryPair(t)
	live.Layout = &warpdb.Layout{Windows: []warpdb.Window{{Tabs: []warpdb.Tab{{}, {}, {}, {}, {}}}}}
	r := describeStatus(saved, live, nil)
	if r.SavedTotal != 2 || r.Live.AgentSessions != 1 || r.Live.Tabs != 5 {
		t.Fatalf("confused saved and live counts: %+v", r)
	}
	r = describeStatus(saved, nil, errors.New("layout access denied"))
	if r.Live != nil || r.LiveError != "layout access denied" || r.SavedTotal != 2 {
		t.Fatal("unavailable live data reported as zero sessions")
	}
}

func TestAcceptCurrentRejectsIncompleteObservation(t *testing.T) {
	_, saved := fixture(t)
	if err := validateCurrent(saved); err != nil {
		t.Fatal(err)
	}
	saved.Warnings = []string{"agent not identified"}
	if err := validateCurrent(saved); err == nil {
		t.Fatal("unidentified agent would be lost")
	}
	saved.Warnings = nil
	saved.Layout = &warpdb.Layout{}
	if err := validateCurrent(saved); err == nil {
		t.Fatal("empty layout accepted")
	}
	_, live := fixture(t)
	live.Sessions[0].Pane = "cccccccccccccccccccccccccccccccc"
	if err := validateCurrent(live); err == nil {
		t.Fatal("missing agent pane accepted")
	}
}
