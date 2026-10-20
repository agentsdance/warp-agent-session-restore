package app

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/proc"
	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
	"gopkg.in/yaml.v3"
)

type restoreEntry struct {
	URI  string `json:"uri"`
	Pane string `json:"pane,omitempty"`
}
type launchFile struct {
	Path string
	Data []byte
}
type launchPlan struct {
	Files   []launchFile
	Runners []launchFile
	Entries []restoreEntry
}

// Auto deliberately uses a conservative floor: this is the first Warp build
// verified with tab_config URIs. Older/unknown builds retain legacy behavior.
func supportsTabConfigs(version string) bool {
	parts := strings.Split(strings.TrimSpace(version), ".")
	floor := []int{0, 2026, 10, 14, 8, 27, 1}
	if len(parts) != len(floor) {
		return false
	}
	values := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return false
		}
		values[i] = n
	}
	for i, n := range values {
		if n != floor[i] {
			return n > floor[i]
		}
	}
	return true
}

func (a *App) useTabConfigs() bool {
	if a.Config.RestoreMode == "tabs" {
		return true
	}
	if a.Config.RestoreMode != "auto" {
		return false
	}
	ps, err := proc.List()
	if err != nil {
		return false
	}
	for _, p := range ps {
		if !strings.Contains(p.Command, "/Warp.app/Contents/MacOS/") {
			continue
		}
		contents := filepath.Dir(filepath.Dir(p.Command))
		b, err := exec.Command("/usr/bin/plutil", "-extract", "CFBundleShortVersionString", "raw", "-o", "-", filepath.Join(contents, "Info.plist")).Output()
		return err == nil && supportsTabConfigs(string(b))
	}
	return false
}

func (a *App) prepareLaunch(s *session.Snapshot, snapshot, batch string) (*launchPlan, error) {
	out, err := buildLaunch(s, a.Binary, a.Home, snapshot, batch)
	if err != nil {
		return nil, err
	}
	plan := &launchPlan{}
	plan.Runners, err = agentRunners(s, a.Binary, a.Home, snapshot, batch)
	if err != nil {
		return nil, err
	}
	if !a.useTabConfigs() {
		data, err := yaml.Marshal(out)
		if err != nil {
			return nil, err
		}
		plan.Files = append(plan.Files, launchFile{filepath.Join(a.Config.LaunchConfigDir, out.Name+".yaml"), data})
		plan.Entries = append(plan.Entries, restoreEntry{URI: "warp://launch/" + url.PathEscape(out.Name)})
		return plan, nil
	}
	for wi, w := range out.Windows {
		for ti, t := range w.Tabs {
			name := fmt.Sprintf("%s-%d-%d", out.Name, wi, ti)
			data := tabTOML(name, t)
			uri := "warp://tab_config/" + url.PathEscape(name)
			if wi > 0 && ti == 0 {
				uri += "?new_window=true"
			}
			// Every generated tab has at least one saved agent runner.
			pane := ""
			var find func(*node)
			find = func(n *node) {
				if n == nil {
					return
				}
				if len(n.Commands) > 0 {
					pane = n.PaneID
					return
				}
				for _, c := range n.Panes {
					if pane == "" {
						find(c)
					}
				}
			}
			find(t.Layout)
			if pane == "" {
				return nil, fmt.Errorf("tab has no recovery pane")
			}
			plan.Files = append(plan.Files, launchFile{filepath.Join(a.Config.TabConfigDir, name+".toml"), data})
			plan.Entries = append(plan.Entries, restoreEntry{URI: uri, Pane: pane})
		}
	}
	return plan, nil
}

// JSON string/array escaping is also valid TOML basic-string syntax. Unlike
// Go's %q it never emits TOML-invalid \x escapes for control characters.
func tomlValue(v any) string { b, _ := json.Marshal(v); return string(b) }
func tabTOML(name string, t tab) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "name = %s\n", tomlValue(name))
	if t.Title != "" {
		fmt.Fprintf(&b, "title = %s\n", tomlValue(t.Title))
	}
	if t.Color != "" {
		fmt.Fprintf(&b, "color = %s\n", tomlValue(t.Color))
	}
	next := 0
	var emit func(*node, string)
	emit = func(n *node, id string) {
		fmt.Fprintf(&b, "\n[[panes]]\nid = %s\n", tomlValue(id))
		if n.CWD != nil {
			fmt.Fprintln(&b, `type = "terminal"`)
			if *n.CWD != "" {
				fmt.Fprintf(&b, "directory = %s\n", tomlValue(*n.CWD))
			}
			if n.Focus {
				fmt.Fprintln(&b, "is_focused = true")
			}
			commands := []string{}
			for _, c := range n.Commands {
				commands = append(commands, c.Exec)
			}
			if len(commands) > 0 {
				fmt.Fprintf(&b, "commands = %s\n", tomlValue(commands))
			}
			return
		}
		ids := []string{}
		for range n.Panes {
			next++
			ids = append(ids, fmt.Sprintf("pane_%d", next))
		}
		fmt.Fprintf(&b, "split = %s\nchildren = %s\n", tomlValue(n.Split), tomlValue(ids))
		for i, c := range n.Panes {
			emit(c, ids[i])
		}
	}
	emit(t.Layout, "root")
	return []byte(b.String())
}

func (a *App) openedPath(batch, pane string) string {
	return filepath.Join(a.Config.StateDir, "opened", batch+"-"+pane)
}

// Persist each dispatch claim before opening the URI. A controller crash cannot
// reopen already dispatched tabs. If delivery is uncertain, keep the recovery
// snapshot and wait for acknowledgement instead of making duplicate windows.
func (a *App) dispatchRestore(done *restoreState) error {
	for i, entry := range done.Entries {
		if i >= done.Next {
			done.Next = i + 1
			if err := state.Save(a.restoreStatePath(), done); err != nil {
				return err
			}
			if err := a.launch(entry.URI); err != nil {
				done.Next = i
				if saveErr := state.Save(a.restoreStatePath(), done); saveErr != nil {
					return fmt.Errorf("launch: %v; save retry progress: %w", err, saveErr)
				}
				return err
			}
		}
		if entry.Pane == "" {
			continue
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := os.Stat(a.openedPath(done.Batch, entry.Pane)); err == nil {
				break
			} else if !os.IsNotExist(err) {
				return err
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("waiting for Warp recovery tab %d; saved sessions preserved (use restore to retry)", i+1)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return nil
}
