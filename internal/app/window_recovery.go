package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/proc"
	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
	"github.com/google/uuid"
)

// Keep the last useful snapshot while Warp is running without windows. A
// separate ID distinguishes successive close/reopen cycles in one app lifetime.
type windowCloseState struct {
	ID         string    `json:"id"`
	WarpID     string    `json:"warp_id"`
	SnapshotID string    `json:"snapshot_id"`
	Time       time.Time `json:"time"`
	Reopened   bool      `json:"reopened,omitempty"`
}

func (a *App) windowClosePath() string {
	return filepath.Join(a.Config.StateDir, "closed-window.json")
}

// Persist the already observed zero-window transition before slow agent
// discovery can miss a rapid reopen. Serialize with manual captures, and never
// associate the observation with a different snapshot or Warp process.
func (a *App) recordWindowClose(saved *session.Snapshot, warp string) error {
	lock, err := state.Lock(filepath.Join(a.Config.StateDir, "capture.lock"), true)
	if err != nil {
		return err
	}
	defer lock.Close()
	current, err := a.LoadSnapshot("")
	if err != nil {
		return err
	}
	if current.ID != saved.ID {
		return nil
	}
	ps, err := proc.List()
	if err != nil {
		return err
	}
	if proc.WarpID(ps) != warp {
		return fmt.Errorf("Warp changed while its last window closed; saved sessions preserved")
	}
	closed, err := a.pendingWindowClose(current, warp)
	if err != nil {
		return err
	}
	if closed == nil || closed.Reopened {
		closed = &windowCloseState{ID: uuid.NewString(), WarpID: warp, SnapshotID: current.ID, Time: time.Now()}
		return state.Save(a.windowClosePath(), closed)
	}
	return nil
}

func (a *App) pendingWindowClose(saved *session.Snapshot, warp string) (*windowCloseState, error) {
	var closed windowCloseState
	if err := state.Load(a.windowClosePath(), &closed); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if !session.ValidID(closed.ID) || !session.ValidID(closed.SnapshotID) {
		return nil, fmt.Errorf("invalid closed-window checkpoint; saved snapshot preserved")
	}
	if saved == nil || closed.SnapshotID != saved.ID || closed.WarpID != warp {
		return nil, nil
	}
	return &closed, nil
}

// Called under capture.lock, before merging missing sessions. An empty layout
// must not age the last window's sessions out of the shutdown grace period.
func (a *App) guardWindowCapture(saved, live *session.Snapshot) (*windowCloseState, error) {
	if live.Layout == nil {
		return nil, fmt.Errorf("Warp layout unavailable; saved snapshot preserved")
	}
	if saved == nil || len(saved.Sessions) == 0 {
		return nil, nil
	}
	closed, err := a.pendingWindowClose(saved, live.WarpID)
	if err != nil {
		return nil, err
	}
	if len(live.Layout.Windows) == 0 {
		if closed == nil && saved.WarpID != live.WarpID {
			// A new Warp process may not have created its first window yet.
			// That is startup, not evidence of closing the last window.
			return nil, fmt.Errorf("Warp has no open windows yet; previous sessions preserved")
		}
		if closed == nil || closed.Reopened {
			closed = &windowCloseState{ID: uuid.NewString(), WarpID: live.WarpID, SnapshotID: saved.ID, Time: live.Taken}
			if err := state.Save(a.windowClosePath(), closed); err != nil {
				return nil, err
			}
		}
		return closed, fmt.Errorf("Warp has no open windows; saved sessions preserved until a window reopens")
	}
	if closed != nil {
		var done restoreState
		if err := state.Load(a.restoreStatePath(), &done); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if done.WarpID != live.WarpID || done.WindowCloseID != closed.ID {
			return closed, fmt.Errorf("closed-window sessions await restoration; use restore or let watch restore them")
		}
	}
	return closed, nil
}

// Restore only after a new window appears, never immediately after the user
// closes the last window. Native undo-close may already have resumed some or
// all agents, so only missing conversation IDs need runners.
func (a *App) restoreReopenedWindow(saved, live *session.Snapshot) (bool, error) {
	closed, err := a.pendingWindowClose(saved, live.WarpID)
	if err != nil || closed == nil {
		return false, err
	}
	if len(live.Layout.Windows) == 0 {
		return false, nil
	}
	if !closed.Reopened {
		closed.Reopened = true
		if err := state.Save(a.windowClosePath(), closed); err != nil {
			return false, err
		}
	}
	var done restoreState
	if err := state.Load(a.restoreStatePath(), &done); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if done.WarpID == live.WarpID && done.WindowCloseID == closed.ID {
		pending := done.Next < len(done.Entries)
		return pending, a.dispatchRestore(&done)
	}
	if len(live.Warnings) > 0 {
		return false, fmt.Errorf("window reopened with unidentified agents; saved sessions preserved: %v", live.Warnings)
	}
	missing := pendingSessions(saved, live)
	if len(missing) == 0 {
		// Record native restoration before Capture adopts its new pane IDs.
		// Clearing the checkpoint now would merge old pane IDs back in.
		done = restoreState{WarpID: live.WarpID, WindowCloseID: closed.ID, Time: live.Taken, Complete: true}
		for _, s := range saved.Sessions {
			if done.Progress == nil {
				done.Progress = map[string]recoveryProgress{}
			}
			done.Progress[s.Agent+":"+s.ID] = recoveryProgress{Verified: true}
		}
		return false, state.Save(a.restoreStatePath(), done)
	}
	// Closed tabs can remain alive in Warp's undo-close stack. Do not launch
	// their conversations again until those original processes have ended.
	for _, want := range missing {
		if want.PID > 0 && !want.Started.IsZero() && proc.StartTime(want.PID).Equal(want.Started) {
			return false, fmt.Errorf("waiting for closed-window agent processes to exit; saved sessions preserved")
		}
		for _, running := range live.Sessions {
			if running.Agent == want.Agent && running.ID == want.ID {
				return false, fmt.Errorf("waiting for closed-window agent processes to exit; saved sessions preserved")
			}
		}
	}
	if !a.Config.AutoResume {
		return false, fmt.Errorf("Warp window reopened; automatic resume is disabled. Run restore")
	}
	copy := *saved
	copy.Sessions = missing
	_, err = a.Restore(&copy, live.WarpID, false)
	return err == nil, err
}

func (a *App) clearWindowClose(closed *windowCloseState) error {
	if closed == nil {
		return nil
	}
	var current windowCloseState
	if err := state.Load(a.windowClosePath(), &current); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if current.ID != closed.ID {
		return nil
	}
	return os.Remove(a.windowClosePath())
}
