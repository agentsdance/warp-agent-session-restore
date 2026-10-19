package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/config"
	"github.com/agentsdance/warp-agent-session-restore/internal/proc"
	"github.com/agentsdance/warp-agent-session-restore/internal/registry"
	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
	"github.com/agentsdance/warp-agent-session-restore/internal/warpdb"
	"github.com/google/uuid"
)

type App struct {
	Home     string
	Config   *config.Config
	Binary   string
	Out, Err io.Writer
	launch   func(string) error
}

func New(home string) (*App, error) {
	c, err := config.Load(home)
	if err != nil {
		return nil, err
	}
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &App{Home: home, Config: c, Binary: bin, Out: os.Stdout, Err: os.Stderr, launch: openURI}, nil
}

func (a *App) LoadSnapshot(path string) (*session.Snapshot, error) {
	if path == "" {
		path = a.Config.SnapshotPath()
	}
	var s session.Snapshot
	if err := state.Load(path, &s); err != nil {
		return nil, err
	}
	if s.Version != 1 || s.Layout == nil || !session.ValidID(s.ID) {
		return nil, fmt.Errorf("invalid snapshot %s", path)
	}
	return &s, nil
}

func (a *App) Capture() (*session.Snapshot, error) {
	return a.capture(false)
}

// CaptureCurrent explicitly accepts today's open sessions as the new baseline.
// Older recovery data is archived before replacing the saved snapshot.
func (a *App) CaptureCurrent() (*session.Snapshot, error) {
	return a.capture(true)
}

func (a *App) capture(acceptCurrent bool) (*session.Snapshot, error) {
	lock, err := state.Lock(filepath.Join(a.Config.StateDir, "capture.lock"), true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	ps, err := proc.List()
	if err != nil {
		return nil, err
	}
	warp := proc.WarpID(ps)
	if warp == "" {
		return nil, fmt.Errorf("Warp is not running; saved snapshot preserved")
	}
	previous, err := a.LoadSnapshot("")
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if !acceptCurrent && previous != nil && previous.WarpID != warp && len(previous.Sessions) > 0 {
		var done restoreState
		if state.Load(a.restoreStatePath(), &done) != nil || done.WarpID != warp {
			return nil, fmt.Errorf("previous Warp sessions await restoration; use restore or let watch restore them")
		}
	}
	s, err := a.observe(previous)
	if err != nil {
		return nil, err
	}
	if s.WarpID != warp {
		return nil, fmt.Errorf("Warp changed during capture; saved snapshot preserved")
	}
	if acceptCurrent {
		if err := validateCurrent(s); err != nil {
			return nil, err
		}
		if err := a.archiveRecovery(previous); err != nil {
			return nil, err
		}
	} else {
		if err := a.verifyRecovery(previous, s); err != nil {
			return nil, err
		}
		session.Merge(previous, s, s.Taken, time.Duration(a.Config.ExitGraceSeconds)*time.Second)
	}
	panes := map[string]bool{}
	for _, p := range s.Layout.Panes() {
		panes[p.UUID] = true
	}
	for _, v := range s.Sessions {
		if !panes[v.Pane] {
			s.Warnings = append(s.Warnings, "session pane absent from Warp layout; will restore in a new tab")
		}
	}
	if previous != nil && equivalent(previous, s) {
		return previous, nil
	}
	if previous != nil {
		if err := state.Save(filepath.Join(a.Config.HistoryDir(), previous.ID+".json"), previous); err != nil {
			return nil, err
		}
	}
	if err := state.Save(a.Config.SnapshotPath(), s); err != nil {
		return nil, err
	}
	a.pruneHistory()
	return s, nil
}

// observe is read-only: it never adopts a snapshot or changes recovery progress.
func (a *App) observe(previous *session.Snapshot) (*session.Snapshot, error) {
	ps, err := proc.List()
	if err != nil {
		return nil, err
	}
	warp := proc.WarpID(ps)
	if warp == "" {
		return nil, fmt.Errorf("Warp is not running")
	}
	layout, err := warpdb.Read(a.Config.WarpDB)
	if err != nil {
		return nil, err
	}
	sessions, warnings, err := session.Discover(a.Config, ps)
	if err != nil {
		return nil, err
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	session.CaptureNames(userHome, sessions, layout, previous)
	current, err := proc.List()
	if err != nil {
		return nil, err
	}
	if proc.WarpID(current) != warp {
		return nil, fmt.Errorf("Warp changed during capture; saved snapshot preserved")
	}
	s := &session.Snapshot{Version: 1, ID: uuid.NewString(), WarpID: warp, Taken: time.Now(), Layout: layout, Sessions: sessions, Warnings: warnings}
	// Processes left behind by a dead Warp lifetime must not satisfy recovery.
	s.Sessions = sessionsInWarp(s.Sessions, current, warp)
	return s, nil
}

func equivalent(x, y *session.Snapshot) bool {
	a, b := *x, *y
	a.ID, b.ID = "", ""
	a.Taken, b.Taken = time.Time{}, time.Time{}
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

func (a *App) pruneHistory() {
	files, _ := filepath.Glob(filepath.Join(a.Config.HistoryDir(), "*.json"))
	sort.Slice(files, func(i, j int) bool {
		x, e1 := os.Stat(files[i])
		y, e2 := os.Stat(files[j])
		return e1 == nil && (e2 != nil || x.ModTime().After(y.ModTime()))
	})
	for i := a.Config.KeepSnapshots; i < len(files); i++ {
		os.Remove(files[i])
	}
}

func (a *App) Hook(agent string, input io.Reader) error {
	if a.Config.AgentByName(agent) == nil {
		return fmt.Errorf("unknown agent %s", agent)
	}
	pane := proc.NormalizePane(os.Getenv("WARP_TERMINAL_SESSION_UUID"))
	if pane == "" {
		return nil
	}
	var payload struct {
		ID         string `json:"session_id"`
		CWD        string `json:"cwd"`
		Event      string `json:"hook_event_name"`
		Source     string `json:"source"`
		Reason     string `json:"reason"`
		Transcript string `json:"transcript_path"`
		Parent     string `json:"parent_session_id"`
	}
	if err := json.NewDecoder(io.LimitReader(input, 1<<20)).Decode(&payload); err != nil {
		return err
	}
	if payload.Parent != "" {
		return nil
	}
	if !session.ValidID(payload.ID) || !filepath.IsAbs(payload.CWD) {
		return fmt.Errorf("hook requires session UUID and absolute cwd")
	}
	event := "start"
	if payload.Event == "SessionEnd" {
		event = "end"
	} else if payload.Event != "SessionStart" && payload.Event != "UserPromptSubmit" {
		return nil
	}
	ps, err := proc.List()
	if err != nil {
		return err
	}
	agentPID, ignored := hookProcess(*a.Config.AgentByName(agent), ps, os.Getppid(), proc.Hydrate)
	if ignored {
		return nil
	}
	if agentPID == 0 {
		return fmt.Errorf("hook has no %s ancestor", agent)
	}
	return registry.Append(a.Config.RegistryPath(), registry.Entry{Time: time.Now(), Event: event, Agent: agent, SessionID: payload.ID, CWD: payload.CWD, PaneUUID: pane, Source: payload.Source + payload.Reason, PID: agentPID, Transcript: payload.Transcript})
}

type restoreState struct {
	WarpID   string                      `json:"warp_id"`
	Batch    string                      `json:"batch"`
	Time     time.Time                   `json:"time"`
	Entries  []restoreEntry              `json:"entries,omitempty"`
	Next     int                         `json:"next,omitempty"`
	Complete bool                        `json:"complete,omitempty"`
	Progress map[string]recoveryProgress `json:"progress,omitempty"`
}

func (a *App) restoreStatePath() string {
	return filepath.Join(a.Config.StateDir, "restore-state.json")
}

// Watch persists captures and restores once per new Warp app lifetime. Failed
// captures do not replace a usable snapshot. It survives both Warp and login.
func (a *App) Watch(ctx context.Context) error {
	lock, err := state.Lock(filepath.Join(a.Config.StateDir, "watch.lock"), false)
	if err != nil {
		return fmt.Errorf("watcher already running or lock unavailable: %w", err)
	}
	defer lock.Close()
	ticker := time.NewTicker(time.Duration(a.Config.PollSeconds) * time.Second)
	defer ticker.Stop()
	lastMessage := ""
	for {
		err = a.tick(ctx)
		if err != nil && err.Error() != lastMessage {
			fmt.Fprintln(a.Err, time.Now().Format(time.RFC3339), err)
			lastMessage = err.Error()
		} else if err == nil {
			lastMessage = ""
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (a *App) tick(ctx context.Context) error {
	ps, err := proc.List()
	if err != nil {
		return err
	}
	warp := proc.WarpID(ps)
	if warp == "" {
		return nil
	}
	s, err := a.LoadSnapshot("")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if s != nil && s.WarpID != warp && len(s.Sessions) > 0 {
		restored, err := a.autoRestore(s, warp)
		if err != nil {
			return err
		}
		if restored {
			fmt.Fprintln(a.Out, time.Now().Format(time.RFC3339), "opened saved agent sessions")
			// Give Warp time to create its panes before the next capture.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(15 * time.Second):
			}
		}
	}
	_, err = a.Capture()
	return err
}

func (a *App) autoRestore(s *session.Snapshot, warp string) (bool, error) {
	if s == nil || warp == "" || s.WarpID == warp || len(s.Sessions) == 0 {
		return false, nil
	}
	var done restoreState
	if err := state.Load(a.restoreStatePath(), &done); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if done.WarpID == warp {
		pending := done.Next < len(done.Entries)
		return pending, a.dispatchRestore(&done)
	}
	if !a.Config.AutoResume {
		return false, fmt.Errorf("Warp restarted; automatic resume is disabled. Run restore")
	}
	_, err := a.Restore(s, warp, false)
	return err == nil, err
}

func openURI(uri string) error { return exec.Command("/usr/bin/open", uri).Run() }
