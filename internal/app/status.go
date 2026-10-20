package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
)

type sessionName struct {
	Agent     string `json:"agent"`
	ID        string `json:"session_id"`
	Name      string `json:"name"`
	Source    string `json:"name_source"`
	AgentName string `json:"agent_name,omitempty"`
	PaneTitle string `json:"pane_title,omitempty"`
	TabTitle  string `json:"tab_title,omitempty"`
}

type liveStatus struct {
	Checked       time.Time      `json:"checked"`
	Windows       int            `json:"windows"`
	Tabs          int            `json:"tabs"`
	Panes         int            `json:"panes"`
	AgentSessions int            `json:"agent_sessions"`
	Sessions      map[string]int `json:"sessions"`
	Warnings      []string       `json:"warnings,omitempty"`
}

type recoveryStatus struct {
	Pending  bool   `json:"pending"`
	Verified int    `json:"verified"`
	Closed   int    `json:"closed"`
	Message  string `json:"message,omitempty"`
}

type statusResult struct {
	Snapshot     string          `json:"snapshot"`
	Taken        time.Time       `json:"taken"`
	SavedTotal   int             `json:"saved_total"`
	Sessions     map[string]int  `json:"sessions"` // Kept for compatibility: saved counts.
	Warnings     []string        `json:"warnings,omitempty"`
	AutoResume   bool            `json:"auto_resume"`
	SessionNames []sessionName   `json:"session_names"`
	Live         *liveStatus     `json:"live,omitempty"`
	LiveError    string          `json:"live_error,omitempty"`
	Recovery     *recoveryStatus `json:"recovery,omitempty"`
}

func describeStatus(saved, live *session.Snapshot, liveErr error) statusResult {
	r := statusResult{Sessions: map[string]int{}, SessionNames: []sessionName{}}
	if saved != nil {
		r.Taken, r.Warnings, r.SavedTotal = saved.Taken, saved.Warnings, len(saved.Sessions)
		for _, s := range saved.Sessions {
			r.Sessions[s.Agent]++
			name, source := s.DisplayName()
			r.SessionNames = append(r.SessionNames, sessionName{s.Agent, s.ID, name, source, s.AgentName, s.PaneTitle, s.TabTitle})
		}
	}
	if liveErr != nil {
		r.LiveError = liveErr.Error()
		return r
	}
	if live != nil {
		r.Live = &liveStatus{Checked: live.Taken, AgentSessions: len(live.Sessions), Sessions: map[string]int{}, Warnings: live.Warnings}
		for _, s := range live.Sessions {
			r.Live.Sessions[s.Agent]++
		}
		if live.Layout != nil {
			r.Live.Windows, r.Live.Panes = len(live.Layout.Windows), len(live.Layout.Panes())
			for _, w := range live.Layout.Windows {
				r.Live.Tabs += len(w.Tabs)
			}
		}
	}
	return r
}

func (a *App) Status() error {
	s, err := a.LoadSnapshot("")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	live, liveErr := a.observe(s)
	r := describeStatus(s, live, liveErr)
	r.Snapshot, r.AutoResume = a.Config.SnapshotPath(), a.Config.AutoResume
	var done restoreState
	if state.Load(a.restoreStatePath(), &done) == nil && live != nil && done.WarpID == live.WarpID {
		r.Recovery = &recoveryStatus{Pending: !done.Complete && s != nil && s.WarpID != live.WarpID}
		for _, p := range done.Progress {
			if p.Verified {
				r.Recovery.Verified++
			} else if p.Closed {
				r.Recovery.Closed++
			}
		}
		if r.Recovery.Pending {
			r.Recovery.Message = "Saved counts are from before recovery. Use restore to retry, or snapshot --current to archive the old state and accept current tabs."
		}
	}
	if live != nil {
		closed, closeErr := a.pendingWindowClose(s, live.WarpID)
		if closeErr != nil {
			return closeErr
		}
		if closed != nil {
			if r.Recovery == nil {
				r.Recovery = &recoveryStatus{}
			}
			r.Recovery.Pending = true
			if len(live.Layout.Windows) == 0 {
				r.Recovery.Message = "Last Warp window is closed. Saved sessions are preserved and will restore when a window opens."
			} else {
				r.Recovery.Message = "Warp window reopened; saved sessions await verification. Use restore to retry, or snapshot --current to accept current tabs."
			}
		}
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Out, string(b))
	return err
}
