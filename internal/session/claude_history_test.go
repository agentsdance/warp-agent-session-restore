package session

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/registry"
	"github.com/google/uuid"
)

func TestClaudeRecoveryDistinguishesEmptyTabsFromMissingHistory(t *testing.T) {
	for _, test := range []struct {
		name, history, source, file string
		noHook, wrongPID, wrongRoot bool
		wantFresh, wantError        bool
	}{
		{name: "legacy empty startup", wantFresh: true},
		{name: "captured empty startup", history: "empty", wantFresh: true},
		{name: "real transcript", file: "exact"},
		{name: "prompt after capture", history: "empty", file: "exact"},
		{name: "custom home transcript", file: "custom"},
		{name: "missing known transcript", history: "present", wantError: true},
		{name: "missing unknown transcript", noHook: true, wantError: true},
		{name: "submitted prompt without transcript", source: "", wantError: true, file: "prompt"},
		{name: "previously resumed", source: "resume", wantError: true},
		{name: "other process", wrongPID: true, wantError: true},
		{name: "other storage root", wrongRoot: true, wantError: true},
		{name: "unrelated conversation", file: "unrelated", wantFresh: true},
		{name: "unreadable storage", file: "blocked", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			s := Session{Agent: "claude", ID: uuid.NewString(), PID: 123, Pane: strings.Repeat("a", 32), CWD: home, Bin: "/bin/claude", Args: []string{"--model", "some model"}, Started: time.Now().Add(-time.Minute), ClaudeHistory: test.history}
			root := filepath.Join(home, ".claude", "projects")
			if test.file == "custom" {
				s.Env = map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(home, "custom root")}
				root = filepath.Join(s.Env["CLAUDE_CONFIG_DIR"], "projects")
			}
			project := filepath.Join(root, "project")
			if err := os.MkdirAll(project, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(project, s.ID+".jsonl")
			hook := registry.Entry{Agent: "claude", SessionID: s.ID, Event: "start", Source: "startup", PID: s.PID, PaneUUID: s.Pane, CWD: s.CWD, Time: time.Now(), Transcript: path}
			if test.source != "" {
				hook.Source = test.source
			}
			if test.wrongPID {
				hook.PID++
			}
			if test.wrongRoot {
				hook.Transcript = filepath.Join(home, "other", s.ID+".jsonl")
			}
			entries := []registry.Entry{hook}
			if test.noHook {
				entries = nil
			}
			switch test.file {
			case "exact", "custom":
				os.WriteFile(path, []byte(`{"type":"user","sessionId":"`+s.ID+`"}`+"\n"), 0600)
			case "unrelated":
				os.WriteFile(filepath.Join(project, uuid.NewString()+".jsonl"), []byte("{}\n"), 0600)
			case "prompt":
				prompt := hook
				prompt.Source = "" // v0.0.7 UserPromptSubmit representation.
				entries = append(entries, prompt)
			case "blocked":
				os.Remove(project)
				os.Remove(root)
				os.WriteFile(root, []byte("not a directory"), 0600)
			}
			cmd, fresh, err := ClaudeRestoreCommand(home, s, entries)
			if (err != nil) != test.wantError {
				t.Fatalf("command=%q fresh=%v err=%v", cmd, fresh, err)
			}
			if test.wantError {
				return
			}
			flag := "--resume"
			if test.wantFresh {
				flag = "--session-id"
			}
			want := []string{s.Bin, flag, s.ID, "--model", "some model"}
			if fresh != test.wantFresh || !reflect.DeepEqual(cmd, want) {
				t.Fatalf("got %q fresh=%v, want %q", cmd, fresh, want)
			}
		})
	}
}

func TestKnownClaudeHistoryCannotBecomeEmptyAfterTranscriptDisappears(t *testing.T) {
	s := Session{Agent: "claude", ID: uuid.NewString(), ClaudeHistory: "empty"}
	old := s
	old.ClaudeHistory = "present"
	sessions := []Session{s}
	CaptureNames(t.TempDir(), sessions, nil, &Snapshot{Sessions: []Session{old}})
	if sessions[0].ClaudeHistory != "present" {
		t.Fatal("lost evidence that this conversation previously had a transcript")
	}
}
