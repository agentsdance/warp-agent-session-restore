package app

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/proc"
	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
	"github.com/agentsdance/warp-agent-session-restore/internal/warpdb"
	"github.com/google/uuid"
)

func windowObservation(saved *session.Snapshot, open bool, sessions ...session.Session) *session.Snapshot {
	s := &session.Snapshot{Version: 1, ID: uuid.NewString(), WarpID: saved.WarpID, Taken: time.Now(), Layout: &warpdb.Layout{}, Sessions: sessions}
	if open {
		w := warpdb.Window{}
		for _, v := range sessions {
			w.Tabs = append(w.Tabs, warpdb.Tab{Root: &warpdb.Node{Pane: &warpdb.Pane{UUID: v.Pane, CWD: v.CWD}}})
		}
		if len(w.Tabs) == 0 {
			w.Tabs = []warpdb.Tab{{Root: &warpdb.Node{Pane: &warpdb.Pane{UUID: "dddddddddddddddddddddddddddddddd"}}}}
		}
		s.Layout.Windows = []warpdb.Window{w}
	}
	return s
}

func closeWindow(t *testing.T, a *App, saved *session.Snapshot) *windowCloseState {
	t.Helper()
	if err := state.Save(a.Config.SnapshotPath(), saved); err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveCapture(saved, windowObservation(saved, false), false); err == nil {
		t.Fatal("empty layout replaced the saved sessions")
	}
	closed, err := a.pendingWindowClose(saved, saved.WarpID)
	if err != nil || closed == nil {
		t.Fatalf("missing close checkpoint: %v", err)
	}
	return closed
}

func TestLastWindowClosePreservesSnapshotBeyondGrace(t *testing.T) {
	a, saved := fixture(t)
	closed := closeWindow(t, a, saved)
	before, _ := os.ReadFile(a.Config.SnapshotPath())
	restarted := *a
	live := windowObservation(saved, false)
	live.Taken = time.Now().Add(24 * time.Hour)
	if _, err := restarted.saveCapture(saved, live, false); err == nil {
		t.Fatal("closed window accepted after grace")
	}
	after, _ := os.ReadFile(a.Config.SnapshotPath())
	if string(before) != string(after) {
		t.Fatal("saved conversations changed while all windows were closed")
	}
	again, err := restarted.pendingWindowClose(saved, saved.WarpID)
	if err != nil || again.ID != closed.ID {
		t.Fatal("watcher restart lost the close cycle")
	}
	a.launch = func(string) error { t.Fatal("reopened window without user action"); return nil }
	if launched, err := a.restoreReopenedWindow(saved, live); launched || err != nil {
		t.Fatalf("closed window launched: %v", err)
	}
}

func TestWindowReopenRestoresOnceAndVerifiesNewPanes(t *testing.T) {
	a, saved, _, _ := recoveryPair(t)
	closeWindow(t, a, saved)
	calls := 0
	a.launch = func(string) error { calls++; return nil }
	live := windowObservation(saved, true)
	if _, err := a.saveCapture(saved, live, false); err == nil {
		t.Fatal("blank reopened window overwrote saved conversations")
	}
	if launched, err := a.restoreReopenedWindow(saved, live); !launched || err != nil {
		t.Fatalf("same-process reopen not restored: %v", err)
	}
	restarted := *a
	if launched, err := restarted.restoreReopenedWindow(saved, live); launched || err != nil || calls != 1 {
		t.Fatalf("duplicated restore after watcher restart: %v calls=%d", err, calls)
	}
	first, second := saved.Sessions[0], saved.Sessions[1]
	first.Pane, second.Pane = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "ffffffffffffffffffffffffffffffff"
	partial := windowObservation(saved, true, first)
	if _, err := a.saveCapture(saved, partial, false); err == nil {
		t.Fatal("partial recovery replaced snapshot")
	}
	complete := windowObservation(saved, true, first, second)
	adopted, err := a.saveCapture(saved, complete, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(adopted.Sessions) != 2 || len(adopted.Layout.Panes()) != 2 {
		t.Fatal("old panes merged back into restored layout")
	}
	if closed, err := a.pendingWindowClose(adopted, adopted.WarpID); err != nil || closed != nil {
		t.Fatal("completed recovery checkpoint remains active")
	}
	// Closing and reopening again in the SAME Warp process is a new cycle.
	closeWindow(t, a, adopted)
	if launched, err := a.restoreReopenedWindow(adopted, windowObservation(adopted, true)); !launched || err != nil || calls != 2 {
		t.Fatalf("second window cycle suppressed: %v calls=%d", err, calls)
	}
}

func TestNativeWindowUndoDoesNotLaunchDuplicateAgents(t *testing.T) {
	a, saved := fixture(t)
	closeWindow(t, a, saved)
	a.launch = func(string) error { t.Fatal("native undo duplicated agent"); return nil }
	agent := saved.Sessions[0]
	agent.Pane = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	live := windowObservation(saved, true, agent)
	if launched, err := a.restoreReopenedWindow(saved, live); launched || err != nil {
		t.Fatal(err)
	}
	adopted, err := a.saveCapture(saved, live, false)
	if err != nil || len(adopted.Sessions) != 1 || adopted.Sessions[0].Pane != agent.Pane {
		t.Fatalf("native undo did not replace old pane identity: %v", err)
	}
}

func TestPartialNativeUndoOnlyLaunchesMissingConversation(t *testing.T) {
	a, saved, _, _ := recoveryPair(t)
	closeWindow(t, a, saved)
	calls := 0
	a.launch = func(string) error { calls++; return nil }
	live := windowObservation(saved, true, saved.Sessions[0])
	if launched, err := a.restoreReopenedWindow(saved, live); !launched || err != nil || calls != 1 {
		t.Fatal(err)
	}
	var done restoreState
	if err := state.Load(a.restoreStatePath(), &done); err != nil {
		t.Fatal(err)
	}
	recovery, err := a.LoadSnapshot(a.Config.StateDir + "/recoveries/" + done.Batch + ".json")
	if err != nil || len(recovery.Sessions) != 1 || recovery.Sessions[0].ID != saved.Sessions[1].ID {
		t.Fatalf("relaunched native-restored conversation: %v", err)
	}
}

func TestClosingIndividualTabDoesNotArmWindowRecovery(t *testing.T) {
	a, saved, _, _ := recoveryPair(t)
	start := time.Now()
	live := windowObservation(saved, true, saved.Sessions[0])
	live.Taken = start
	next, err := a.saveCapture(saved, live, false)
	if err != nil || len(next.Sessions) != 2 {
		t.Fatal("missing shutdown grace")
	}
	later := windowObservation(saved, true, saved.Sessions[0])
	later.Taken = start.Add(21 * time.Second)
	next, err = a.saveCapture(next, later, false)
	if err != nil || len(next.Sessions) != 1 {
		t.Fatalf("deliberately closed tab retained: %v", err)
	}
	if _, err := os.Stat(a.windowClosePath()); !os.IsNotExist(err) {
		t.Fatal("tab closure treated as window closure")
	}
}

func TestReopenedWindowWaitsForLingeringAgentAndHonorsAutoResume(t *testing.T) {
	a, saved := fixture(t)
	closeWindow(t, a, saved)
	a.launch = func(string) error { t.Fatal("unsafe automatic launch"); return nil }
	live := windowObservation(saved, true)
	live.Sessions = saved.Sessions // Alive in undo stack, absent from new layout.
	if launched, err := a.restoreReopenedWindow(saved, live); launched || err == nil {
		t.Fatal("restarted original agent while still alive")
	}
	live.Sessions = nil
	live.Warnings = []string{"unidentified agent"}
	if launched, err := a.restoreReopenedWindow(saved, live); launched || err == nil {
		t.Fatal("restored while agent identity was uncertain")
	}
	live.Warnings = nil
	a.Config.AutoResume = false
	if launched, err := a.restoreReopenedWindow(saved, live); launched || err == nil {
		t.Fatal("ignored auto_resume=false")
	}
	if _, err := a.saveCapture(saved, live, false); err == nil {
		t.Fatal("disabled automatic resume erased saved sessions")
	}
}

func TestWindowCheckpointDoesNotApplyToNewAppLifetime(t *testing.T) {
	a, saved := fixture(t)
	closeWindow(t, a, saved)
	closed, err := a.pendingWindowClose(saved, "another-app-lifetime")
	if err != nil || closed != nil {
		t.Fatal("old window checkpoint used for new app process")
	}
}

func TestWindowReopenRetriesFailedDispatch(t *testing.T) {
	a, saved := fixture(t)
	closeWindow(t, a, saved)
	live := windowObservation(saved, true)
	a.launch = func(string) error { return errors.New("launch failed") }
	if launched, err := a.restoreReopenedWindow(saved, live); launched || err == nil {
		t.Fatal("failed launch accepted")
	}
	var before restoreState
	if err := state.Load(a.restoreStatePath(), &before); err != nil {
		t.Fatal(err)
	}
	a.launch = func(string) error { return nil }
	if launched, err := a.restoreReopenedWindow(saved, live); !launched || err != nil {
		t.Fatalf("failed launch not retried: %v", err)
	}
	var after restoreState
	state.Load(a.restoreStatePath(), &after)
	if before.Batch != after.Batch || before.WindowCloseID != after.WindowCloseID {
		t.Fatal("retry created a duplicate recovery batch")
	}
}

func TestEmptyNewAppDoesNotArmWindowReopen(t *testing.T) {
	a, saved := fixture(t)
	live := windowObservation(saved, false)
	live.WarpID = "new-lifetime"
	if _, err := a.saveCapture(saved, live, false); err == nil {
		t.Fatal("startup erased previous sessions")
	}
	if _, err := os.Stat(a.windowClosePath()); !os.IsNotExist(err) {
		t.Fatal("new app startup mistaken for window closure")
	}
}

func TestAcceptCurrentClearsPendingWindowReopen(t *testing.T) {
	a, saved := fixture(t)
	closeWindow(t, a, saved)
	live := windowObservation(saved, true)
	adopted, err := a.saveCapture(saved, live, true)
	if err != nil || len(adopted.Sessions) != 0 {
		t.Fatalf("explicit new baseline not accepted: %v", err)
	}
	if _, err := os.Stat(a.windowClosePath()); !os.IsNotExist(err) {
		t.Fatal("explicit acceptance left window recovery armed")
	}
}

func TestWindowClosesAgainBeforeRecoveryCompletes(t *testing.T) {
	a, saved := fixture(t)
	firstClose := closeWindow(t, a, saved)
	calls := 0
	a.launch = func(string) error { calls++; return nil }
	if _, err := a.restoreReopenedWindow(saved, windowObservation(saved, true)); err != nil {
		t.Fatal(err)
	}
	secondClose := closeWindow(t, a, saved)
	if firstClose.ID == secondClose.ID {
		t.Fatal("second closure reused the first checkpoint")
	}
	if launched, err := a.restoreReopenedWindow(saved, windowObservation(saved, true)); !launched || err != nil || calls != 2 {
		t.Fatalf("second reopen suppressed by interrupted recovery: %v", err)
	}
}

func TestWindowReopenChecksOriginalProcessEvenIfDiscoveryMissesIt(t *testing.T) {
	a, saved := fixture(t)
	saved.Sessions[0].PID = os.Getpid()
	saved.Sessions[0].Started = proc.StartTime(os.Getpid())
	closeWindow(t, a, saved)
	a.launch = func(string) error { t.Fatal("live original process duplicated"); return nil }
	if launched, err := a.restoreReopenedWindow(saved, windowObservation(saved, true)); launched || err == nil {
		t.Fatal("original process was ignored")
	}
}
