package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/proc"
	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
)

type recoveryProgress struct {
	Verified     bool      `json:"verified,omitempty"`
	Closed       bool      `json:"closed,omitempty"`
	MissingSince time.Time `json:"missing_since,omitempty"`
}

// Receipt connects a dispatched old pane to the pane Warp actually created.
// Starting a child is not proof that the requested conversation loaded.
type recoveryReceipt struct {
	WarpID string `json:"warp_id"`
	Agent  string `json:"agent"`
	ID     string `json:"session_id"`
	Pane   string `json:"pane_uuid"`
	PID    int    `json:"pid"`
}

func (a *App) receiptPath(batch, pane string) string {
	return filepath.Join(a.Config.StateDir, "started", batch+"-"+pane+".json")
}

func (a *App) recordRecoveryStart(batch string, s *session.Session, pid int, warp string) {
	pane := proc.NormalizePane(os.Getenv("WARP_TERMINAL_SESSION_UUID"))
	if pane == "" || warp == "" {
		return
	}
	err := state.Save(a.receiptPath(batch, s.Pane), recoveryReceipt{warp, s.Agent, s.ID, pane, pid})
	if err != nil {
		fmt.Fprintln(a.Err, "could not record recovery pane:", err)
	}
}

func (a *App) archiveRecovery(previous *session.Snapshot) error {
	if previous == nil {
		return nil
	}
	dir := filepath.Join(a.Config.StateDir, "reconciliations", time.Now().Format("20060102T150405.000000000"))
	if err := state.Save(filepath.Join(dir, "snapshot.json"), previous); err != nil {
		return err
	}
	b, err := os.ReadFile(a.restoreStatePath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return state.Write(filepath.Join(dir, "restore-state.json"), b, 0600)
}

func validateCurrent(s *session.Snapshot) error {
	if len(s.Warnings) > 0 {
		return fmt.Errorf("cannot accept current sessions while identification warnings remain: %v", s.Warnings)
	}
	if s.Layout == nil || len(s.Layout.Panes()) == 0 {
		return fmt.Errorf("cannot accept current sessions without a live Warp layout")
	}
	panes := map[string]bool{}
	for _, p := range s.Layout.Panes() {
		panes[p.UUID] = true
	}
	for _, v := range s.Sessions {
		if !panes[v.Pane] {
			return fmt.Errorf("cannot accept current sessions: agent pane absent from Warp layout")
		}
	}
	return nil
}

func sessionsInWarp(sessions []session.Session, ps []proc.Process, warp string) []session.Session {
	pidText, _, _ := strings.Cut(warp, "-")
	pid, _ := strconv.Atoi(pidText)
	descendants := map[int]bool{}
	for _, p := range proc.Descendants(ps, pid) {
		descendants[p] = true
	}
	out := make([]session.Session, 0, len(sessions))
	for _, s := range sessions {
		if descendants[s.PID] {
			out = append(out, s)
		}
	}
	return out
}

// A dispatched URI (or a consumed runner claim) is not proof that the exact
// agent conversation started. Remember verified IDs across polls so closing an
// already recovered conversation cannot undo its verification. An unverified
// attempt can be retired only after its new pane and child process disappear
// for a grace period while other live sessions remain.
func (a *App) verifyRecovery(previous, next *session.Snapshot) error {
	if previous == nil || len(previous.Sessions) == 0 || previous.WarpID == next.WarpID {
		return nil
	}
	var done restoreState
	if err := state.Load(a.restoreStatePath(), &done); err != nil {
		return err
	}
	if done.WarpID != next.WarpID {
		return fmt.Errorf("previous Warp sessions await restoration")
	}
	if done.Complete {
		return nil
	}
	expected, err := a.LoadSnapshot(filepath.Join(a.Config.StateDir, "recoveries", done.Batch+".json"))
	if err != nil {
		return fmt.Errorf("read pending recovery: %w", err)
	}
	ps, err := proc.List()
	if err != nil {
		return err
	}
	alive := map[int]bool{}
	for _, p := range ps {
		alive[p.PID] = true
	}
	receipts := map[string]recoveryReceipt{}
	for _, s := range expected.Sessions {
		var receipt recoveryReceipt
		if err := state.Load(a.receiptPath(done.Batch, s.Pane), &receipt); err == nil {
			receipts[s.Agent+":"+s.ID] = receipt
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read recovery receipt: %w", err)
		}
	}
	before, _ := json.Marshal(done)
	missing := advanceRecovery(&done, expected, next, receipts, alive, time.Duration(a.Config.ExitGraceSeconds)*time.Second)
	done.Complete = len(missing) == 0
	after, _ := json.Marshal(done)
	if !bytes.Equal(before, after) {
		if err := state.Save(a.restoreStatePath(), done); err != nil {
			return err
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("waiting for %d of %d saved sessions; snapshot preserved (use restore to retry, or snapshot --current to accept current tabs)", len(missing), len(expected.Sessions))
	}
	return nil
}

func advanceRecovery(done *restoreState, expected, next *session.Snapshot, receipts map[string]recoveryReceipt, alive map[int]bool, grace time.Duration) []session.Session {
	if done.Progress == nil {
		done.Progress = map[string]recoveryProgress{}
	}
	missingNow := map[string]bool{}
	for _, s := range pendingSessions(expected, next) {
		missingNow[s.Agent+":"+s.ID] = true
	}
	panes := map[string]bool{}
	if next.Layout != nil {
		for _, p := range next.Layout.Panes() {
			panes[p.UUID] = true
		}
	}
	// An empty layout/capture can be a shutdown or transient DB view. It is not
	// evidence that the user deliberately closed all recovery tabs.
	canRetire := len(panes) > 0 && len(next.Sessions) > 0
	var pending []session.Session
	for _, s := range expected.Sessions {
		key := s.Agent + ":" + s.ID
		p := done.Progress[key]
		if !missingNow[key] {
			p.Verified = true
			p.MissingSince = time.Time{}
		}
		if !p.Verified && !p.Closed {
			r, ok := receipts[key]
			closed := canRetire && ok && r.WarpID == next.WarpID && r.Agent == s.Agent && r.ID == s.ID && proc.NormalizePane(r.Pane) != "" && r.PID > 0 && !panes[r.Pane] && !alive[r.PID]
			if closed {
				if p.MissingSince.IsZero() {
					p.MissingSince = next.Taken
				}
				p.Closed = next.Taken.Sub(p.MissingSince) >= grace
			} else {
				p.MissingSince = time.Time{}
			}
		}
		done.Progress[key] = p
		if !p.Verified && !p.Closed {
			pending = append(pending, s)
		}
	}
	return pending
}

func pendingSessions(expected, next *session.Snapshot) []session.Session {
	panes := map[string]bool{}
	if next.Layout != nil {
		for _, p := range next.Layout.Panes() {
			panes[p.UUID] = true
		}
	}
	live := map[string]bool{}
	for _, s := range next.Sessions {
		if panes[s.Pane] {
			live[s.Agent+":"+s.ID] = true
		}
	}
	missing := []session.Session{}
	for _, s := range expected.Sessions {
		if !live[s.Agent+":"+s.ID] {
			missing = append(missing, s)
		}
	}
	return missing
}
