package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
)

type droidHooks struct {
	Root       string `json:"root"`
	Path       string `json:"path"`
	Command    string `json:"command"`
	Standalone bool   `json:"standalone"`
}

// Factory reads standalone hooks first, then legacy standalone hooks, and
// falls back to settings.json. Edit the effective source so we do not mask
// existing user hooks by creating a new, higher-priority file.
func droidHookFile(root, command string) (droidHooks, error) {
	for _, relative := range []string{"hooks.json", "hooks/hooks.json", "settings.json"} {
		path := filepath.Join(root, relative)
		_, err := os.Stat(path)
		if err != nil && !os.IsNotExist(err) {
			return droidHooks{}, err
		}
		if err == nil || relative == "settings.json" {
			if target, err := filepath.EvalSymlinks(path); err == nil {
				path = target
			}
			return droidHooks{Root: root, Path: path, Command: command, Standalone: relative != "settings.json"}, nil
		}
	}
	panic("unreachable")
}

func mergeDroidHooks(data []byte, command string, standalone, remove bool) ([]byte, error) {
	if !standalone {
		return MergeHooks(data, command, remove)
	}
	var hooks map[string]any
	if err := json.Unmarshal(data, &hooks); err != nil {
		return nil, err
	}
	if hooks == nil {
		return nil, fmt.Errorf("Droid hooks must be a JSON object")
	}
	wrapped, err := json.Marshal(map[string]any{"hooks": hooks})
	if err != nil {
		return nil, err
	}
	merged, err := MergeHooks(wrapped, command, remove)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Hooks json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(merged, &doc); err != nil {
		return nil, err
	}
	return doc.Hooks, nil
}

func (a *App) prepareDroidHooks(bin string) (droidHooks, []byte, []byte, error) {
	home := a.Home
	if custom := os.Getenv("FACTORY_HOME_OVERRIDE"); custom != "" {
		home = custom
	}
	command := session.Shell([]string{bin, "--home", a.Home, "hook", "droid"})
	hooks, err := droidHookFile(filepath.Join(home, ".factory"), command)
	if err != nil {
		return hooks, nil, nil, err
	}
	data, err := os.ReadFile(hooks.Path)
	if err != nil && !os.IsNotExist(err) {
		return hooks, nil, nil, err
	}
	merged, err := mergeDroidHooks(data, command, hooks.Standalone, false)
	if err != nil {
		return hooks, nil, nil, fmt.Errorf("could not set up Droid session tracking in %s: %w", hooks.Path, err)
	}
	return hooks, data, merged, nil
}

func removeDroidHooks(installed droidHooks) error {
	// Droid's hook editor can migrate settings/legacy hooks to hooks.json after
	// installation. Remove our exact command from all possible sources.
	files := map[string]bool{installed.Path: installed.Standalone}
	for _, relative := range []string{"hooks.json", "hooks/hooks.json", "settings.json"} {
		path := filepath.Join(installed.Root, relative)
		if target, err := filepath.EvalSymlinks(path); err == nil {
			path = target
		}
		files[path] = relative != "settings.json"
	}
	type update struct {
		path string
		data []byte
	}
	var updates []update
	for path, standalone := range files {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		// Decode before searching so JSON escapes in paths cannot hide a hook.
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			return err
		}
		var contains func(any) bool
		contains = func(v any) bool {
			switch value := v.(type) {
			case map[string]any:
				if value["command"] == installed.Command {
					return true
				}
				for _, child := range value {
					if contains(child) {
						return true
					}
				}
			case []any:
				for _, child := range value {
					if contains(child) {
						return true
					}
				}
			}
			return false
		}
		if !contains(doc) {
			continue
		}
		merged, err := mergeDroidHooks(data, installed.Command, standalone, true)
		if err != nil {
			return err
		}
		updates = append(updates, update{path, merged})
	}
	for _, u := range updates {
		if err := state.Write(u.path, append(u.data, '\n'), 0o600); err != nil {
			return err
		}
	}
	return nil
}
