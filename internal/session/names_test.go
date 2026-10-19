package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/warpdb"
)

const nameID = "11111111-1111-4111-8111-111111111111"
const otherNameID = "22222222-2222-4222-8222-222222222222"

func TestDroidTitleTracksExactSessionAndFactoryHome(t *testing.T) {
	home, custom := t.TempDir(), t.TempDir()
	path := filepath.Join(custom, ".factory", "sessions", "encoded-project", nameID+".jsonl")
	header := func(id, name string, manual bool) string {
		b, _ := json.Marshal(map[string]any{"type": "session_start", "id": id, "title": name, "isSessionTitleManuallySet": manual})
		return string(b) + "\n"
	}
	sessions := []Session{{Agent: "droid", ID: nameID, Env: map[string]string{"FACTORY_HOME_OVERRIDE": custom}}}
	nameFile(t, path, header(nameID, "Original", false))
	CaptureNames(home, sessions, nil, nil)
	if sessions[0].Name != "Original" || sessions[0].NameSource != "droid_derived" {
		t.Fatalf("%+v", sessions[0])
	}
	nameFile(t, path, header(nameID, "Renamed 会话", true))
	CaptureNames(home, sessions, nil, nil)
	if sessions[0].Name != "Renamed 会话" || sessions[0].NameSource != "droid_user" {
		t.Fatalf("%+v", sessions[0])
	}
	previous := &Snapshot{Sessions: append([]Session(nil), sessions...)}
	for _, invalid := range []string{header(otherNameID, "Wrong", true), `{"type":"session_start"`, header(nameID, "Ambiguous", true)} {
		nameFile(t, path, invalid)
		if invalid == header(nameID, "Ambiguous", true) {
			nameFile(t, filepath.Join(custom, ".factory", "sessions", "second-project", nameID+".jsonl"), invalid)
		}
		if _, ok := droidName(filepath.Join(custom, ".factory"), sessions[0]); ok {
			t.Fatal("accepted invalid or ambiguous header")
		}
		CaptureNames(home, sessions, nil, previous)
		if sessions[0].Name != "Renamed 会话" {
			t.Fatal("lost last known name")
		}
	}
	os.RemoveAll(filepath.Join(custom, ".factory", "sessions", "second-project"))
	nameFile(t, path, header(nameID, "", true))
	CaptureNames(home, sessions, nil, previous)
	if sessions[0].AgentName != "" {
		t.Fatal("explicit empty title did not clear name")
	}
	sessions[0] = Session{Agent: "droid", ID: nameID}
	CaptureNames(home, sessions, nil, previous)
	if sessions[0].NameSource != "fallback" {
		t.Fatal("title leaked across storage roots")
	}
}

func nameFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func indexLine(id, name, updated string) string {
	b, _ := json.Marshal(map[string]string{"id": id, "thread_name": name, "updated_at": updated})
	return string(b) + "\n"
}

func TestCaptureNamesRefreshByIDAndStorageRoot(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "session_index.jsonl")
	newName := "修复 'quotes' $(touch /tmp/should-not-run) 🚀"
	data := indexLine(nameID, "old", "2026-10-19T01:00:00Z") + indexLine(otherNameID, "different session", "2026-10-19T01:00:00Z")
	nameFile(t, path, data)
	sessions := []Session{{Agent: "codex", ID: nameID, CWD: home}, {Agent: "codex", ID: otherNameID, CWD: home}}
	CaptureNames(home, sessions, nil, nil)
	if sessions[0].Name != "old" || sessions[1].Name != "different session" {
		t.Fatal("names confused within one directory")
	}
	previous := &Snapshot{Sessions: append([]Session(nil), sessions...)}
	data += indexLine(nameID, newName, "2026-10-19T02:00:00Z") + indexLine(nameID, "stale", "2026-10-19T01:30:00Z") + `{"id":"torn`
	nameFile(t, path, data)
	CaptureNames(home, sessions, nil, previous)
	if sessions[0].AgentName != newName || sessions[0].NameSource != "codex_session_index" {
		t.Fatalf("rename not refreshed: %+v", sessions[0])
	}
	previous.Sessions = append([]Session(nil), sessions...)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	sessions[0] = Session{Agent: "codex", ID: nameID}
	CaptureNames(home, sessions, nil, previous)
	if sessions[0].Name != newName {
		t.Fatal("temporary absence discarded saved name")
	}
	nameFile(t, path, indexLine(nameID, "", "2026-10-19T03:00:00Z"))
	CaptureNames(home, sessions, nil, previous)
	if sessions[0].AgentName != "" || sessions[0].NameSource != "fallback" {
		t.Fatal("cleared name retained")
	}
	sessions[0] = Session{Agent: "codex", ID: nameID, Env: map[string]string{"CODEX_HOME": filepath.Join(home, "other")}}
	CaptureNames(home, sessions, nil, previous)
	if sessions[0].NameSource != "fallback" {
		t.Fatal("name leaked across storage roots")
	}
	nameFile(t, filepath.Join(home, "other", "session_index.jsonl"), indexLine(nameID, "custom root", "2026-10-19T03:00:00Z"))
	CaptureNames(home, sessions, nil, previous)
	if sessions[0].Name != "custom root" {
		t.Fatal("CODEX_HOME ignored")
	}
}

func TestClaudeRuntimeAndTranscriptNames(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "custom-claude")
	started := time.Date(2026, 10, 19, 1, 2, 3, 0, time.UTC)
	r := claudeProcess{PID: 1234, ID: nameID, CWD: home, ProcStart: started.Format("Mon Jan  2 15:04:05 2006"), Kind: "interactive", Entrypoint: "cli", Name: "用户改名", NameSource: "user"}
	b, _ := json.Marshal(r)
	runtimePath := filepath.Join(dir, "sessions", "1234.json")
	nameFile(t, runtimePath, string(b))
	sessions := []Session{{Agent: "claude", ID: nameID, PID: 1234, Started: started, Env: map[string]string{"CLAUDE_CONFIG_DIR": dir}}}
	CaptureNames(home, sessions, nil, nil)
	if sessions[0].Name != r.Name || sessions[0].NameSource != "claude_user" {
		t.Fatal("runtime name not captured")
	}
	r.Name, r.NameSource = "Derived title", "derived"
	b, _ = json.Marshal(r)
	nameFile(t, runtimePath, string(b))
	CaptureNames(home, sessions, nil, nil)
	if sessions[0].NameSource != "claude_derived" {
		t.Fatal("derived title claimed as user rename")
	}
	// A runtime record from a different conversation must never supply the name.
	r.ID = otherNameID
	b, _ = json.Marshal(r)
	nameFile(t, runtimePath, string(b))
	transcript := filepath.Join(dir, "projects", "project", nameID+".jsonl")
	data := `{"type":"custom-title","sessionId":"` + nameID + `","customTitle":"old"}` + "\n"
	data += strings.Repeat("x", 2<<20) + "\n" // large messages must not hide later rename records
	data += `{"type":"custom-title","sessionId":"` + nameID + `","customTitle":"latest"}` + "\n"
	data += `{"type":"custom-title","sessionId":"` + otherNameID + `","customTitle":"wrong session"}` + "\n"
	nameFile(t, transcript, data)
	sessions[0].AgentName, sessions[0].AgentNameSource = "", ""
	CaptureNames(home, sessions, nil, nil)
	if sessions[0].Name != "latest" {
		t.Fatalf("transcript rename lost: %+v", sessions[0])
	}
	// Birth-time validation also applies to names, not just session discovery.
	r.ID = nameID
	r.ProcStart = started.Add(-time.Hour).Format("Mon Jan  2 15:04:05 2006")
	b, _ = json.Marshal(r)
	nameFile(t, runtimePath, string(b))
	CaptureNames(home, sessions, nil, nil)
	if sessions[0].Name != "latest" {
		t.Fatal("stale PID name accepted")
	}
}

func TestCaptureNamesKeepsSeparateWarpLabelsAndClearsThem(t *testing.T) {
	home := t.TempDir()
	nameFile(t, filepath.Join(home, ".codex", "session_index.jsonl"), indexLine(nameID, "Agent name", "2026-10-19T01:00:00Z"))
	p := &warpdb.Pane{UUID: "aaaa", Title: "Pane name"}
	l := &warpdb.Layout{Windows: []warpdb.Window{{Tabs: []warpdb.Tab{{Title: "Tab name", Root: &warpdb.Node{Pane: p}}}}}}
	sessions := []Session{{Agent: "codex", ID: nameID, Pane: p.UUID}}
	CaptureNames(home, sessions, l, nil)
	if s := sessions[0]; s.Name != "Pane name" || s.NameSource != "warp_pane" || s.TabTitle != "Tab name" || s.AgentName != "Agent name" {
		t.Fatalf("lost independent names: %+v", s)
	}
	previous := &Snapshot{Sessions: append([]Session(nil), sessions...)}
	p.Title, l.Windows[0].Tabs[0].Title = "", ""
	CaptureNames(home, sessions, l, previous)
	if s := sessions[0]; s.Name != "Agent name" || s.PaneTitle != "" || s.TabTitle != "" {
		t.Fatalf("cleared Warp labels retained: %+v", s)
	}
	var legacy Session
	if err := json.Unmarshal([]byte(`{"agent":"codex","session_id":"`+nameID+`"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if name, source := legacy.DisplayName(); name != "codex 11111111" || source != "fallback" {
		t.Fatal("legacy snapshot has no display label")
	}
}
