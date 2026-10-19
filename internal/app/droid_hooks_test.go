package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/agentsdance/warp-agent-session-restore/internal/config"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
)

func TestInstallAddsDroidToExistingConfiguration(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS installer")
	}
	for _, key := range []string{"CLAUDE_CONFIG_DIR", "FACTORY_HOME_OVERRIDE", "ZDOTDIR"} {
		t.Setenv(key, "")
	}
	a, _ := fixture(t)
	var err error
	a.Binary, err = os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var old []config.Agent
	for _, agent := range a.Config.Agents {
		if agent.Name != "droid" {
			old = append(old, agent)
		}
	}
	a.Config.Agents = old
	a.Config.AgentByName("codex").ValueFlags = append(a.Config.AgentByName("codex").ValueFlags, "--custom-option")
	if err := a.Install(true); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(a.Home)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AgentByName("droid") == nil {
		t.Fatal("upgrade did not enable Droid")
	}
	if !reflect.DeepEqual(loaded.AgentByName("codex"), a.Config.AgentByName("codex")) {
		t.Fatal("upgrade replaced existing agent configuration")
	}
	var manifest installation
	if err := state.Load(filepath.Join(a.Config.StateDir, "installation.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Droid == nil {
		t.Fatal("uninstall metadata missing")
	}
	if err := removeDroidHooks(*manifest.Droid); err != nil {
		t.Fatal(err)
	}
}

func TestDroidHooksPreserveEffectiveSourceAndUserConfiguration(t *testing.T) {
	for _, source := range []string{"settings.json", "hooks/hooks.json", "hooks.json"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			command := "'/a path/restore' hook droid"
			original := map[string]any{"SessionStart": []any{map[string]any{"matcher": "resume", "hooks": []any{map[string]any{"type": "command", "command": "keep-me"}}}}}
			standalone := source != "settings.json"
			var doc any = original
			if !standalone {
				doc = map[string]any{"hooks": original, "trustedFolders": []string{"/project"}, "hooksDisabled": true}
			}
			data, _ := json.Marshal(doc)
			path := filepath.Join(root, source)
			if err := state.Write(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			// Lower-priority configuration must never be hidden or overwritten.
			if standalone {
				if err := state.Write(filepath.Join(root, "settings.json"), []byte(`{"trustedFolders":["/other"]}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			path, _ = filepath.EvalSymlinks(path)
			selected, err := droidHookFile(root, command)
			if err != nil || selected.Path != path || selected.Standalone != standalone {
				t.Fatalf("wrong source: %+v %v", selected, err)
			}
			merged, err := mergeDroidHooks(data, command, standalone, false)
			if err != nil {
				t.Fatal(err)
			}
			again, err := mergeDroidHooks(merged, command, standalone, false)
			if err != nil || string(merged) != string(again) {
				t.Fatal("install is not idempotent", err)
			}
			if err := state.Write(path, again, 0600); err != nil {
				t.Fatal(err)
			}
			if err := removeDroidHooks(selected); err != nil {
				t.Fatal(err)
			}
			cleaned, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			json.Unmarshal(cleaned, &got)
			json.Unmarshal(data, &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("user settings changed: %s", cleaned)
			}
		})
	}
}

func TestDroidHooksRemoveAfterFactoryMigration(t *testing.T) {
	root := t.TempDir()
	command := "managed hook"
	installed := droidHooks{Root: root, Path: filepath.Join(root, "settings.json"), Command: command}
	data, err := mergeDroidHooks([]byte(`{"SessionEnd":[{"hooks":[{"command":"keep"}]}]}`), command, true, false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "hooks.json")
	if err := state.Write(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeDroidHooks(installed); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	var doc map[string]any
	json.Unmarshal(data, &doc)
	if len(doc) != 1 || doc["SessionEnd"] == nil {
		t.Fatalf("wrong remaining hooks: %s", data)
	}
}

func TestDroidHookPreparationUsesOverrideAndRejectsMalformedConfiguration(t *testing.T) {
	a, _ := fixture(t)
	override := t.TempDir()
	t.Setenv("FACTORY_HOME_OVERRIDE", override)
	path := filepath.Join(override, ".factory", "hooks.json")
	if err := state.Write(path, []byte(`{"SessionStart":"invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.prepareDroidHooks("/helper"); err == nil {
		t.Fatal("accepted invalid hooks")
	}
	if err := state.Write(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	path, _ = filepath.EvalSymlinks(path)
	got, _, _, err := a.prepareDroidHooks("/helper")
	if err != nil || got.Path != path {
		t.Fatalf("override ignored: %+v %v", got, err)
	}
}
