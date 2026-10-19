package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/config"
	"github.com/agentsdance/warp-agent-session-restore/internal/warpdb"
)

func TestOptionsAndShellDoNotReplayPromptOrExecuteValues(t *testing.T) {
	a := config.Default(t.TempDir()).AgentByName("codex")
	value := "name='x y'; $(touch /tmp/ras-MUST-NOT-EXIST) `false`"
	got, err := KeepOptions(*a, []string{"resume", "old-id", "--last", "--model", "test model", "-c", value, "--cd", "/old dir", "--worktree", "--no-alt-screen", "original prompt"})
	want := []string{"--model", "test model", "-c", value, "--no-alt-screen"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%q %v", got, err)
	}
	// Shell-quoted strings must survive a real shell, including empty args.
	argv := append(got, "", "a'b\nc")
	command := "printf '%s\\0' " + Shell(argv)
	output, err := exec.Command("/bin/sh", "-c", command).Output()
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := []byte{}
	for _, s := range argv {
		wantBytes = append(wantBytes, []byte(s)...)
		wantBytes = append(wantBytes, 0)
	}
	if !reflect.DeepEqual(output, wantBytes) {
		t.Fatalf("shell altered arguments: %q", output)
	}
}

func TestOpenTranscriptSkipsSubagentsAndRejectsAmbiguity(t *testing.T) {
	home := t.TempDir()
	a := config.Default(home).AgentByName("codex")
	makeFile := func(id string, source any) string {
		p := filepath.Join(home, ".codex/sessions/2026/10/19/rollout-date-"+id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "source": source}})
		if err := os.WriteFile(p, b, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	id := "11111111-1111-4111-8111-111111111111"
	p := makeFile(id, "cli")
	sub := makeFile("22222222-2222-4222-8222-222222222222", map[string]any{"subagent": "test"})
	got, err := FindOpenID([]string{p, sub}, a.OpenFileRegex)
	if err != nil || got != id {
		t.Fatalf("%s %v", got, err)
	}
	other := makeFile("33333333-3333-4333-8333-333333333333", "cli")
	if _, err = FindOpenID([]string{p, other}, a.OpenFileRegex); err == nil {
		t.Fatal("ambiguous transcripts accepted")
	}
}

func TestOpenTranscriptRecognizesDaemonCreatedTerminalSessions(t *testing.T) {
	home := t.TempDir()
	agent := config.Default(home).AgentByName("codex")
	id := "11111111-1111-4111-8111-111111111111"
	path := filepath.Join(home, ".codex/sessions/2026/10/19/rollout-date-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, originator string
		source           any
		want             bool
	}{
		{"terminal through daemon", "codex-tui", "vscode", true},
		{"editor", "codex_vscode", "vscode", false},
		{"missing originator", "", "vscode", false},
		{"other source", "codex-tui", "exec", false},
		{"subagent", "codex-tui", map[string]any{"subagent": "test"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{
				"id": id, "source": tc.source, "originator": tc.originator,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := FindOpenID([]string{path}, agent.OpenFileRegex)
			if tc.want && (err != nil || got != id) {
				t.Fatalf("terminal transcript rejected: id=%q err=%v", got, err)
			}
			if !tc.want && (err == nil || got != "") {
				t.Fatalf("non-terminal transcript accepted: id=%q err=%v", got, err)
			}
		})
	}
}

func TestClaudeMetadataRejectsReusedPID(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 19, 5, 6, 7, 0, time.UTC)
	data := map[string]any{"pid": 123, "sessionId": "11111111-1111-4111-8111-111111111111", "cwd": dir, "kind": "interactive", "entrypoint": "cli", "procStart": start.Format("Mon Jan  2 15:04:05 2006")}
	b, _ := json.Marshal(data)
	os.WriteFile(filepath.Join(dir, "sessions/123.json"), b, 0600)
	if id, cwd := ClaudeProcessSession(dir, 123, start); id == "" || cwd != dir {
		t.Fatal("valid metadata rejected")
	}
	if id, _ := ClaudeProcessSession(dir, 123, start.Add(time.Second)); id != "" {
		t.Fatal("reused PID accepted")
	}
}

func TestShutdownGraceAndWarpGeneration(t *testing.T) {
	now := time.Now()
	old := &Snapshot{WarpID: "old", Layout: &warpdb.Layout{}, Sessions: []Session{{Pane: "pane"}}}
	next := &Snapshot{WarpID: "old", Layout: &warpdb.Layout{}}
	Merge(old, next, now, 20*time.Second)
	if len(next.Sessions) != 1 || next.Layout != old.Layout {
		t.Fatal("shutdown destroyed last layout/session")
	}
	closed := &Snapshot{WarpID: "old"}
	Merge(next, closed, now.Add(21*time.Second), 20*time.Second)
	if len(closed.Sessions) != 0 {
		t.Fatal("deliberately closed session retained forever")
	}
	restarted := &Snapshot{WarpID: "new"}
	Merge(next, restarted, now, 20*time.Second)
	if len(restarted.Sessions) != 0 {
		t.Fatal("old pane leaked into a different Warp lifetime")
	}
}
