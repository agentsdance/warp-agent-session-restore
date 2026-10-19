package app

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/agentsdance/warp-agent-session-restore/internal/proc"
	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
)

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
// agent conversation started. Preserve the pre-restart snapshot until every
// expected ID is discovered in a live pane under the new Warp process.
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
	missing := pendingSessions(expected, next)
	if len(missing) > 0 {
		return fmt.Errorf("waiting for %d of %d saved sessions to start in live Warp panes; snapshot preserved (use restore to retry)", len(missing), len(expected.Sessions))
	}
	done.Complete = true
	return state.Save(a.restoreStatePath(), done)
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
