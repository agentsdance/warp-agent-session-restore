package app

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/proc"
	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
	"github.com/agentsdance/warp-agent-session-restore/internal/warpdb"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type launch struct {
	Name    string   `yaml:"name"`
	Windows []window `yaml:"windows"`
}
type window struct {
	Tabs   []tab   `yaml:"tabs"`
	Groups []group `yaml:"tab_groups,omitempty"`
}
type group struct {
	Name string `yaml:"name"`
}
type tab struct {
	Title  string `yaml:"title,omitempty"`
	Color  string `yaml:"color,omitempty"`
	Group  *int   `yaml:"group,omitempty"`
	Layout *node  `yaml:"layout"`
}
type node struct {
	CWD      *string   `yaml:"cwd,omitempty"`
	Focus    bool      `yaml:"is_focused,omitempty"`
	Split    string    `yaml:"split_direction,omitempty"`
	Panes    []*node   `yaml:"panes,omitempty"`
	Commands []command `yaml:"commands,omitempty"`
	PaneID   string    `yaml:"-"`
}
type command struct {
	Exec string `yaml:"exec"`
}

func LaunchYAML(s *session.Snapshot, binary, home, snapshot, batch string) ([]byte, error) {
	out, err := buildLaunch(s, binary, home, snapshot, batch)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(out)
}

func buildLaunch(s *session.Snapshot, binary, home, snapshot, batch string) (*launch, error) {
	saved := map[string]session.Session{}
	for _, v := range s.Sessions {
		saved[v.Pane] = v
	}
	used := map[string]bool{}
	leaf := func(p *warpdb.Pane) *node {
		cwd := p.CWD
		if !filepath.IsAbs(cwd) {
			cwd = ""
		}
		n := &node{CWD: &cwd, Focus: p.IsFocused, PaneID: p.UUID}
		if v, ok := saved[p.UUID]; ok {
			cwd = v.CWD
			used[p.UUID] = true
			n.Commands = []command{{session.Shell([]string{binary, "--home", home, "run-session", "--snapshot", snapshot, "--pane", p.UUID, "--batch", batch})}}
		}
		return n
	}
	var convert func(*warpdb.Node) *node
	convert = func(n *warpdb.Node) *node {
		if n == nil {
			return nil
		}
		if n.IsLeaf() {
			return leaf(n.Pane)
		}
		out := &node{Split: "vertical"}
		if n.Horizontal {
			out.Split = "horizontal"
		}
		for _, child := range n.Children {
			if c := convert(child); c != nil {
				out.Panes = append(out.Panes, c)
			}
		}
		if len(out.Panes) == 0 {
			return nil
		}
		if len(out.Panes) == 1 {
			return out.Panes[0]
		}
		return out
	}
	var hasSession func(*warpdb.Node) bool
	hasSession = func(n *warpdb.Node) bool {
		if n == nil {
			return false
		}
		if n.IsLeaf() {
			_, ok := saved[n.Pane.UUID]
			return ok
		}
		for _, c := range n.Children {
			if hasSession(c) {
				return true
			}
		}
		return false
	}
	out := launch{Name: "warp-agent-session-restore-" + batch}
	for _, w := range s.Layout.Windows {
		nw := window{}
		groups := map[string]int{}
		for _, t := range w.Tabs {
			if !hasSession(t.Root) {
				continue
			}
			nt := tab{Title: t.Title, Color: t.Color, Layout: convert(t.Root)}
			// Launch YAML supports tab titles, but not custom split-pane labels.
			// A single-pane agent tab can safely use its saved session name.
			if nt.Title == "" && t.Root.IsLeaf() {
				if v, ok := saved[t.Root.Pane.UUID]; ok {
					nt.Title, _ = v.DisplayName()
				}
			}
			if t.Group != "" {
				idx, ok := groups[t.Group]
				if !ok {
					idx = len(nw.Groups)
					groups[t.Group] = idx
					nw.Groups = append(nw.Groups, group{t.Group})
				}
				nt.Group = &idx
			}
			nw.Tabs = append(nw.Tabs, nt)
		}
		if len(nw.Tabs) > 0 {
			out.Windows = append(out.Windows, nw)
		}
	}
	for _, v := range s.Sessions {
		if !used[v.Pane] {
			if len(out.Windows) == 0 {
				out.Windows = append(out.Windows, window{})
			}
			title, _ := v.DisplayName()
			out.Windows[0].Tabs = append(out.Windows[0].Tabs, tab{Title: title, Layout: leaf(&warpdb.Pane{UUID: v.Pane, CWD: v.CWD})})
		}
	}
	if len(out.Windows) == 0 {
		return nil, fmt.Errorf("no sessions to restore")
	}
	return &out, nil
}

func (a *App) Restore(s *session.Snapshot, warp string, dry bool) (string, error) {
	copy := *s
	copy.Sessions = append([]session.Session(nil), s.Sessions...)
	if a.Config.AutoResumeLimit > 0 && len(copy.Sessions) > a.Config.AutoResumeLimit {
		copy.Sessions = copy.Sessions[:a.Config.AutoResumeLimit]
	}
	batch := uuid.NewString()
	snapPath := filepath.Join(a.Config.StateDir, "recoveries", batch+".json")
	plan, err := a.prepareLaunch(&copy, snapPath, batch)
	if err != nil {
		return "", err
	}
	if dry {
		for _, file := range plan.Files {
			fmt.Fprint(a.Out, string(file.Data))
		}
		return "", nil
	}
	if err = state.Save(snapPath, &copy); err != nil {
		return "", err
	}
	for _, file := range plan.Files {
		if err = state.Write(file.Path, file.Data, 0o600); err != nil {
			return "", err
		}
	}
	done := restoreState{WarpID: warp, Batch: batch, Time: time.Now(), Entries: plan.Entries}
	if err = state.Save(a.restoreStatePath(), done); err != nil {
		return "", err
	}
	return plan.Files[0].Path, a.dispatchRestore(&done)
}

func (a *App) RunSession(path, pane, batch string) error {
	if !session.ValidID(batch) || proc.NormalizePane(pane) == "" {
		return fmt.Errorf("invalid batch or pane")
	}
	s, err := a.LoadSnapshot(path)
	if err != nil {
		return err
	}
	var saved *session.Session
	for i := range s.Sessions {
		if s.Sessions[i].Pane == pane {
			saved = &s.Sessions[i]
			break
		}
	}
	if saved == nil {
		return fmt.Errorf("pane is not in snapshot")
	}
	// A pane acknowledgement is separate from successful agent startup. It
	// lets sequential tab-config dispatch wait for the active window to exist.
	if err = state.Write(a.openedPath(batch, pane), []byte(time.Now().Format(time.RFC3339)), 0o600); err != nil {
		return err
	}
	argv, err := saved.Command()
	if err != nil {
		return err
	}
	if saved.Agent == "codex" && a.Config.LocalCodex && !slices.Contains(argv, "--no-daemon") {
		argv = append(argv, "--no-daemon")
	}
	// Held throughout the agent's lifetime; another layout cannot start a copy.
	lock, err := state.Lock(filepath.Join(a.Config.StateDir, "locks", saved.Agent+"-"+saved.ID+".lock"), false)
	if err != nil {
		return fmt.Errorf("session already running or locked: %w", err)
	}
	defer lock.Close()
	marker := filepath.Join(a.Config.StateDir, "consumed", batch+"-"+pane)
	if _, err = os.Stat(marker); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	ps, err := proc.List()
	if err != nil {
		return err
	}
	live, _, err := session.Discover(a.Config, ps)
	if err != nil {
		return err
	}
	for _, v := range live {
		if v.Agent == saved.Agent && v.ID == saved.ID {
			return fmt.Errorf("session is already open in Warp")
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = saved.CWD
	cmd.Env = os.Environ()
	for k, v := range saved.Env {
		if k == "CODEX_HOME" || k == "CLAUDE_CONFIG_DIR" || k == "FACTORY_HOME_OVERRIDE" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	name, _ := saved.DisplayName()
	fmt.Fprintf(a.Out, "Resuming %s session %s (%q)\n", saved.Agent, saved.ID, name)
	// Ctrl-C is delivered to the entire foreground process group. The agent
	// handles it as an interruption; the supervisor must keep its lock alive.
	interrupts := make(chan os.Signal, 4)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGQUIT)
	defer signal.Stop(interrupts)
	if err = cmd.Start(); err != nil {
		return err
	}
	a.recordRecoveryStart(batch, saved, cmd.Process.Pid, proc.WarpID(ps))
	if err = state.Write(marker, []byte(time.Now().Format(time.RFC3339)), 0o600); err != nil {
		fmt.Fprintln(a.Err, "could not record resume:", err)
	}
	err = cmd.Wait()
	if err != nil {
		os.Remove(marker)
		return fmt.Errorf("resume failed (can be retried): %w", err)
	}
	return nil
}
