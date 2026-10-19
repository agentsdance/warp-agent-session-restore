package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentsdance/warp-agent-session-restore/internal/config"
	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
	"github.com/agentsdance/warp-agent-session-restore/internal/warpdb"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

func fixture(t *testing.T) (*App, *session.Snapshot) {
	t.Helper()
	home := t.TempDir()
	a := &App{Home: home, Config: config.Default(home), Binary: "/path with 'quotes'/warp-agent-session-restore", Out: io.Discard, Err: io.Discard}
	pane := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s := &session.Snapshot{Version: 1, ID: uuid.NewString(), WarpID: "before", Layout: &warpdb.Layout{Windows: []warpdb.Window{{Tabs: []warpdb.Tab{{Title: "Agent tab", Group: "Work", Root: &warpdb.Node{Horizontal: true, Children: []*warpdb.Node{{Pane: &warpdb.Pane{UUID: pane, CWD: home, IsFocused: true}}, {Pane: &warpdb.Pane{UUID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", CWD: home}}}}}}}}}, Sessions: []session.Session{{Agent: "codex", ID: uuid.NewString(), Pane: pane, CWD: home, Bin: "/bin/echo", Args: []string{"--model", "model with spaces"}}}}
	return a, s
}

func TestRestartLaunchesOnceAcrossWatcherRestarts(t *testing.T) {
	a, s := fixture(t)
	calls := 0
	a.launch = func(uri string) error {
		calls++
		if !strings.HasPrefix(uri, "warp://launch/") {
			t.Fatal(uri)
		}
		stem := strings.TrimPrefix(uri, "warp://launch/")
		b, err := os.ReadFile(filepath.Join(a.Config.LaunchConfigDir, stem+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var config launch
		if err = yaml.Unmarshal(b, &config); err != nil {
			t.Fatal(err)
		}
		if config.Name != stem {
			t.Fatal("Warp URI must match the YAML name")
		}
		return nil
	}
	if restored, err := a.autoRestore(s, "before"); err != nil || restored {
		t.Fatal("restored into original running Warp")
	}
	if restored, err := a.autoRestore(s, ""); err != nil || restored {
		t.Fatal("restored while Warp closed")
	}
	if restored, err := a.autoRestore(s, "after"); err != nil || !restored {
		t.Fatalf("%v %v", restored, err)
	}
	// A different watcher instance must read the persisted completion marker.
	restarted := *a
	if restored, err := restarted.autoRestore(s, "after"); err != nil || restored || calls != 1 {
		t.Fatalf("duplicate restore: calls=%d err=%v", calls, err)
	}
	if restored, err := restarted.autoRestore(s, "another-restart"); err != nil || !restored || calls != 2 {
		t.Fatal("next restart was suppressed")
	}
}

func TestLaunchFailureCanRetryAndDryRunWritesNothing(t *testing.T) {
	a, s := fixture(t)
	a.launch = func(string) error { return errors.New("launch failed") }
	if _, err := a.autoRestore(s, "after"); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(a.restoreStatePath()); !os.IsNotExist(err) {
		t.Fatal("failed launch marked complete")
	}
	a.launch = func(string) error { return nil }
	if ok, err := a.autoRestore(s, "after"); err != nil || !ok {
		t.Fatalf("retry: %v", err)
	}
	b, s2 := fixture(t)
	var out bytes.Buffer
	b.Out = &out
	b.launch = func(string) error { t.Fatal("dry run launched Warp"); return nil }
	if _, err := b.Restore(s2, "after", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.Config.StateDir); !os.IsNotExist(err) {
		t.Fatal("dry run wrote state")
	}
	var layout launch
	if err := yaml.Unmarshal(out.Bytes(), &layout); err != nil {
		t.Fatal(err)
	}
	if len(layout.Windows) != 1 || len(layout.Windows[0].Tabs[0].Layout.Panes) != 2 {
		t.Fatal("split layout lost")
	}
	cmd := layout.Windows[0].Tabs[0].Layout.Panes[0].Commands[0].Exec
	if !strings.Contains(cmd, "'\"'\"'") || strings.Contains(cmd, s2.Sessions[0].ID) {
		t.Fatal("unsafe command or agent data embedded into launch config")
	}
}

func TestHooksMergeIsIdempotentAndPreservesOtherSettings(t *testing.T) {
	original := []byte(`{"permissions":{"defaultMode":"default"},"hooks":{"SessionStart":[{"matcher":"resume","hooks":[{"type":"command","command":"existing-hook"}]}],"Stop":[{"hooks":[{"type":"command","command":"stop-hook"}]}]}}`)
	one, err := MergeHooks(original, "managed-hook", false)
	if err != nil {
		t.Fatal(err)
	}
	two, err := MergeHooks(one, "managed-hook", false)
	if err != nil || !bytes.Equal(one, two) {
		t.Fatal("install not idempotent")
	}
	restored, err := MergeHooks(two, "managed-hook", true)
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	json.Unmarshal(original, &before)
	json.Unmarshal(restored, &after)
	b, _ := json.Marshal(before)
	c, _ := json.Marshal(after)
	if !bytes.Equal(b, c) {
		t.Fatalf("changed unrelated settings: %s", restored)
	}
}

func TestSavedNamesInStatusAndRestoredTabs(t *testing.T) {
	a, s := fixture(t)
	s.Sessions[0].AgentName = "修复 'quotes' $(false) 🚀"
	s.Sessions[0].AgentNameSource = "codex_session_index"
	s.Sessions[0].Name, s.Sessions[0].NameSource = s.Sessions[0].DisplayName()
	if err := state.Save(a.Config.SnapshotPath(), s); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.Out = &out
	if err := a.Status(); err != nil {
		t.Fatal(err)
	}
	var status struct {
		SessionNames []struct {
			ID   string `json:"session_id"`
			Name string `json:"name"`
		} `json:"session_names"`
	}
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.SessionNames) != 1 || status.SessionNames[0].Name != s.Sessions[0].Name || status.SessionNames[0].ID != s.Sessions[0].ID {
		t.Fatal("status omitted exact session names")
	}
	for _, mode := range []string{"custom", "single", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			_, snap := fixture(t)
			snap.Sessions[0] = s.Sessions[0]
			want := "Agent tab"
			if mode == "single" {
				snap.Layout.Windows[0].Tabs[0].Title = ""
				snap.Layout.Windows[0].Tabs[0].Root = &warpdb.Node{Pane: &warpdb.Pane{UUID: snap.Sessions[0].Pane}}
				want = s.Sessions[0].Name
			}
			if mode == "fallback" {
				snap.Layout.Windows = nil
				want = s.Sessions[0].Name
			}
			data, err := LaunchYAML(snap, a.Binary, a.Home, "/snapshot.json", uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
			var launch launch
			if err = yaml.Unmarshal(data, &launch); err != nil {
				t.Fatal(err)
			}
			if launch.Windows[0].Tabs[0].Title != want {
				t.Fatal("wrong restored title")
			}
		})
	}
}

func TestRunnerUsesExactArgvAndCWDAndDoesNotRunTwice(t *testing.T) {
	a, s := fixture(t)
	script := filepath.Join(a.Home, "fake agent.sh")
	record := filepath.Join(a.Home, "called")
	// A local fake agent exercises process launch without sending a model prompt.
	code := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" >> " + session.Quote(record) + "\n"
	if err := os.WriteFile(script, []byte(code), 0700); err != nil {
		t.Fatal(err)
	}
	s.Sessions[0].Bin = script
	s.Sessions[0].Args = []string{"--model", "a b'$(false)"}
	path := filepath.Join(a.Home, "snapshot.json")
	if err := state.Save(path, s); err != nil {
		t.Fatal(err)
	}
	batch := uuid.NewString()
	pane := s.Sessions[0].Pane
	if err := a.RunSession(path, pane, batch); err != nil {
		t.Fatal(err)
	}
	if err := a.RunSession(path, pane, batch); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	realHome, err := filepath.EvalSymlinks(a.Home)
	if err != nil {
		t.Fatal(err)
	}
	want := realHome + "\nresume\n" + s.Sessions[0].ID + "\n--model\na b'$(false)\n"
	if string(b) != want {
		t.Fatalf("wrong arguments or repeated launch: %q", b)
	}
	// Claude has a different resume syntax, but uses the same exact argv path.
	s.Sessions[0].Agent = "claude"
	s.Sessions[0].ID = uuid.NewString()
	state.Save(path, s)
	if err := a.RunSession(path, pane, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	want += realHome + "\n--resume\n" + s.Sessions[0].ID + "\n--model\na b'$(false)\n"
	if string(b) != want {
		t.Fatalf("Claude resume arguments: %q", b)
	}
	// Droid resumes an exact ID and preserves runtime settings as argv values.
	s.Sessions[0].Agent = "droid"
	s.Sessions[0].ID = uuid.NewString()
	s.Sessions[0].Args = []string{"--settings", "/settings with spaces.json", "--auto", "low"}
	if err := state.Save(path, s); err != nil {
		t.Fatal(err)
	}
	batch = uuid.NewString()
	for range 2 {
		if err := a.RunSession(path, pane, batch); err != nil {
			t.Fatal(err)
		}
	}
	b, err = os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	want += realHome + "\nresume\n" + s.Sessions[0].ID + "\n--settings\n/settings with spaces.json\n--auto\nlow\n"
	if string(b) != want {
		t.Fatalf("Droid resume arguments or duplicate: %q", b)
	}
}

func TestRunnerFailureReleasesClaim(t *testing.T) {
	a, s := fixture(t)
	s.Sessions[0].Bin = "/usr/bin/false"
	path := filepath.Join(a.Home, "snapshot.json")
	state.Save(path, s)
	batch := uuid.NewString()
	if err := a.RunSession(path, s.Sessions[0].Pane, batch); err == nil {
		t.Fatal("expected process failure")
	}
	marker := filepath.Join(a.Config.StateDir, "consumed", batch+"-"+s.Sessions[0].Pane)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("failed resume consumed the session")
	}
}

func TestShellBlockPreservesUserContent(t *testing.T) {
	original := "# user\nexport TEST='x y'\n"
	block := shellBegin + "\nsource '/test'\n" + shellEnd
	one, err := shellBlock(original, block)
	if err != nil {
		t.Fatal(err)
	}
	two, err := shellBlock(one, block)
	if err != nil || one != two {
		t.Fatal("duplicate block")
	}
	removed, err := shellBlock(two, "")
	if err != nil || strings.TrimSpace(removed) != strings.TrimSpace(original) {
		t.Fatal("user content changed")
	}
	if _, err = shellBlock(shellBegin, block); err == nil {
		t.Fatal("accepted incomplete block")
	}
}
