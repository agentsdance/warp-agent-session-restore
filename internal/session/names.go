package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/warpdb"
)

type savedName struct {
	name, source string
	updated      time.Time
}

type claudeProcess struct {
	PID        int    `json:"pid"`
	ID         string `json:"sessionId"`
	CWD        string `json:"cwd"`
	ProcStart  string `json:"procStart"`
	Kind       string `json:"kind"`
	Entrypoint string `json:"entrypoint"`
	Name       string `json:"name"`
	NameSource string `json:"nameSource"`
}

func readClaudeProcess(dir string, pid int, started time.Time) claudeProcess {
	b, err := os.ReadFile(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"))
	var r claudeProcess
	if err != nil || json.Unmarshal(b, &r) != nil || r.PID != pid || !ValidID(r.ID) || r.Kind != "interactive" || r.Entrypoint != "cli" {
		return claudeProcess{}
	}
	birth, err := time.ParseInLocation("Mon Jan  2 15:04:05 2006", r.ProcStart, time.UTC)
	if err != nil || started.IsZero() || birth.Unix() != started.Unix() {
		return claudeProcess{}
	}
	return r
}

// Read only complete, bounded JSONL records. A torn append or a large message
// must not hide later title metadata or cause conversation content to be saved.
func metadataLines(path string, visit func([]byte)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var line []byte
	tooLarge := false
	for {
		part, err := r.ReadSlice('\n')
		if !tooLarge {
			if len(line)+len(part) > 1<<20 {
				tooLarge = true
				line = nil
			} else {
				line = append(line, part...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return
		}
		if !tooLarge {
			visit(line)
		}
		line, tooLarge = line[:0], false
	}
}

func codexNames(dir string) map[string]savedName {
	names := map[string]savedName{}
	metadataLines(filepath.Join(dir, "session_index.jsonl"), func(b []byte) {
		var r struct {
			ID      string    `json:"id"`
			Name    *string   `json:"thread_name"`
			Updated time.Time `json:"updated_at"`
		}
		if json.Unmarshal(b, &r) != nil || !ValidID(r.ID) || r.Name == nil {
			return
		}
		if old, ok := names[r.ID]; ok && r.Updated.Before(old.updated) {
			return
		}
		names[r.ID] = savedName{*r.Name, "codex_session_index", r.Updated}
	})
	return names
}

func claudeName(dir string, s Session) (savedName, bool) {
	if !ValidID(s.ID) {
		return savedName{}, false
	}
	r := readClaudeProcess(dir, s.PID, s.Started)
	if r.ID == s.ID && r.Name != "" {
		source := "claude_runtime"
		if r.NameSource != "" {
			source = "claude_" + r.NameSource
		}
		return savedName{name: r.Name, source: source}, true
	}
	// Older Claude versions have no runtime names. Only an exact session UUID
	// is accepted, never another conversation from the same project directory.
	files, _ := filepath.Glob(filepath.Join(dir, "projects", "*", s.ID+".jsonl"))
	if len(files) != 1 {
		return savedName{}, false
	}
	var name savedName
	found := false
	metadataLines(files[0], func(b []byte) {
		var r struct {
			Type string  `json:"type"`
			ID   string  `json:"sessionId"`
			Name *string `json:"customTitle"`
		}
		if json.Unmarshal(b, &r) == nil && r.Type == "custom-title" && r.ID == s.ID && r.Name != nil {
			name, found = savedName{name: *r.Name, source: "claude_user"}, true
		}
	})
	return name, found
}

func agentHome(home string, s Session) string {
	if s.Agent == "droid" {
		if override := s.Env["FACTORY_HOME_OVERRIDE"]; override != "" {
			home = override
		}
		return filepath.Join(home, ".factory")
	}
	key, fallback := "CODEX_HOME", ".codex"
	if s.Agent == "claude" {
		key, fallback = "CLAUDE_CONFIG_DIR", ".claude"
	}
	if dir := s.Env[key]; dir != "" {
		return dir
	}
	return filepath.Join(home, fallback)
}

// CaptureNames refreshes names on every poll, including /rename without a new
// process. Keep the last known agent name if its metadata is temporarily absent.
// Explicit empty names clear it. Identity includes the agent's storage root.
func CaptureNames(home string, sessions []Session, layout *warpdb.Layout, previous *Snapshot) {
	type identity struct{ agent, dir, id string }
	key := func(s Session) identity { return identity{s.Agent, agentHome(home, s), s.ID} }
	old := map[identity]Session{}
	if previous != nil {
		for _, s := range previous.Sessions {
			old[key(s)] = s
		}
	}
	type labels struct{ pane, tab string }
	warp := map[string]labels{}
	if layout != nil {
		for _, w := range layout.Windows {
			for _, t := range w.Tabs {
				var walk func(*warpdb.Node)
				walk = func(n *warpdb.Node) {
					if n == nil {
						return
					}
					if n.IsLeaf() {
						warp[n.Pane.UUID] = labels{n.Pane.Title, t.Title}
						return
					}
					for _, child := range n.Children {
						walk(child)
					}
				}
				walk(t.Root)
			}
		}
	}
	indexes := map[string]map[string]savedName{}
	for i := range sessions {
		s := &sessions[i]
		if p, ok := old[key(*s)]; ok {
			s.AgentName, s.AgentNameSource = p.AgentName, p.AgentNameSource
		}
		dir := agentHome(home, *s)
		var name savedName
		var found bool
		switch s.Agent {
		case "codex":
			if _, ok := indexes[dir]; !ok {
				indexes[dir] = codexNames(dir)
			}
			name, found = indexes[dir][s.ID]
		case "claude":
			name, found = claudeName(dir, *s)
		case "droid":
			name, found = droidName(dir, *s)
		}
		if found {
			s.AgentName, s.AgentNameSource = name.name, name.source
		}
		if titles, ok := warp[s.Pane]; ok {
			s.PaneTitle, s.TabTitle = titles.pane, titles.tab
		} else if p, ok := old[key(*s)]; ok && p.Pane == s.Pane {
			s.PaneTitle, s.TabTitle = p.PaneTitle, p.TabTitle
		}
		s.Name, s.NameSource = s.DisplayName()
	}
}

// Droid keeps its current title in the transcript header. Match both filename
// and header ID; never select another conversation by project or recency.
func droidName(dir string, s Session) (savedName, bool) {
	if !ValidID(s.ID) {
		return savedName{}, false
	}
	files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", s.ID+".jsonl"))
	if len(files) != 1 {
		return savedName{}, false
	}
	f, err := os.Open(files[0])
	if err != nil {
		return savedName{}, false
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	line, err := r.ReadSlice('\n')
	if err != nil {
		return savedName{}, false
	}
	var header struct {
		Type   string  `json:"type"`
		ID     string  `json:"id"`
		Title  *string `json:"title"`
		Manual bool    `json:"isSessionTitleManuallySet"`
	}
	if json.Unmarshal(line, &header) != nil || header.Type != "session_start" || header.ID != s.ID || header.Title == nil {
		return savedName{}, false
	}
	source := "droid_derived"
	if header.Manual {
		source = "droid_user"
	}
	return savedName{name: *header.Title, source: source}, true
}

// DisplayName also provides a useful name for older snapshots without titles.
func (s Session) DisplayName() (string, string) {
	if s.PaneTitle != "" {
		return s.PaneTitle, "warp_pane"
	}
	if s.AgentName != "" {
		return s.AgentName, s.AgentNameSource
	}
	if s.TabTitle != "" {
		return s.TabTitle, "warp_tab"
	}
	id := s.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return s.Agent + " " + id, "fallback"
}
