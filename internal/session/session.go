package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/config"
	"github.com/agentsdance/warp-agent-session-restore/internal/proc"
	"github.com/agentsdance/warp-agent-session-restore/internal/registry"
	"github.com/agentsdance/warp-agent-session-restore/internal/warpdb"
)

type Session struct {
	Agent           string            `json:"agent"`
	ID              string            `json:"session_id"`
	Pane            string            `json:"pane_uuid"`
	Name            string            `json:"name,omitempty"`
	NameSource      string            `json:"name_source,omitempty"`
	AgentName       string            `json:"agent_name,omitempty"`
	AgentNameSource string            `json:"agent_name_source,omitempty"`
	PaneTitle       string            `json:"pane_title,omitempty"`
	TabTitle        string            `json:"tab_title,omitempty"`
	CWD             string            `json:"cwd"`
	Bin             string            `json:"bin"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	PID             int               `json:"pid"`
	Started         time.Time         `json:"started"`
	MissingSince    time.Time         `json:"missing_since,omitempty"`
}

type Snapshot struct {
	Version  int            `json:"version"`
	ID       string         `json:"id"`
	WarpID   string         `json:"warp_id"`
	Taken    time.Time      `json:"taken"`
	Layout   *warpdb.Layout `json:"layout"`
	Sessions []Session      `json:"sessions"`
	Warnings []string       `json:"warnings,omitempty"`
}

var idRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func ValidID(id string) bool { return idRE.MatchString(id) }

func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func Shell(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}

// KeepOptions never replays the initial prompt or a subcommand. Original argv
// boundaries are retained, including whitespace, quotes and empty values.
func KeepOptions(a config.Agent, args []string) ([]string, error) {
	values := map[string]bool{}
	for _, v := range a.ValueFlags {
		values[v] = true
	}
	drop := map[string]config.ValueKind{}
	for _, d := range a.DropArgs {
		drop[d.Flag] = d.Value
	}
	var out []string
	for i := 0; i < len(args); i++ {
		v := args[i]
		if v == "--" {
			break
		}
		if !strings.HasPrefix(v, "-") {
			continue
		}
		key, _, inline := strings.Cut(v, "=")
		if kind, ok := drop[key]; ok {
			if !inline && (kind == config.ValueRequired || kind == config.ValueOptional && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-")) {
				i++
			}
			continue
		}
		out = append(out, v)
		if values[key] && !inline {
			if i+1 == len(args) {
				return nil, fmt.Errorf("missing value for %s", key)
			}
			i++
			out = append(out, args[i])
		}
	}
	return out, nil
}

func (s Session) Command() ([]string, error) {
	if !ValidID(s.ID) || s.Bin == "" || !filepath.IsAbs(s.CWD) {
		return nil, fmt.Errorf("invalid saved session")
	}
	var cmd []string
	switch s.Agent {
	case "codex", "droid":
		cmd = []string{s.Bin, "resume", s.ID}
	case "claude":
		cmd = []string{s.Bin, "--resume", s.ID}
	default:
		return nil, fmt.Errorf("unsupported agent %q", s.Agent)
	}
	return append(cmd, s.Args...), nil
}

type Candidate struct {
	Process proc.Process
	Agent   config.Agent
	Bin     string
	Args    []string
	Pane    string
}

func Candidates(cfg *config.Config, processes []proc.Process) []Candidate {
	var candidates []Candidate
	for _, p := range processes {
		likely := strings.Contains(p.Command, "claude") || filepath.Base(p.Command) == "node"
		for _, a := range cfg.Agents {
			for _, m := range a.Match {
				if filepath.Base(p.Command) == m {
					likely = true
				}
			}
		}
		if !likely || proc.Hydrate(&p) != nil {
			continue
		}
		pane := proc.NormalizePane(p.Env["WARP_TERMINAL_SESSION_UUID"])
		if pane == "" {
			continue
		}
		found := false
		for _, a := range cfg.Agents {
			for i := 0; i < len(p.Argv) && i < 2; i++ {
				base := filepath.Base(p.Argv[i])
				base = strings.TrimSuffix(base, ".js")
				match := false
				for _, m := range a.Match {
					if base == m {
						match = true
					}
				}
				if !match {
					continue
				}
				args := p.Argv[i+1:]
				if !Interactive(a, args) {
					break
				}
				bin := p.Argv[i]
				// Prefer stable shims over version-specific package paths.
				if stable, err := exec.LookPath(a.Name); err == nil {
					bin = stable
				}
				candidates = append(candidates, Candidate{p, a, bin, args, pane})
				found = true
				break
			}
			if found {
				break
			}
		}
	}
	// The node launcher and native child are one session. Prefer the child,
	// which owns the transcript, while excluding nested agents and tool servers.
	var out []Candidate
	for _, c := range candidates {
		wrapper := false
		for _, other := range candidates {
			if other.Process.PPID == c.Process.PID && other.Agent.Name == c.Agent.Name {
				wrapper = true
			}
		}
		if !wrapper {
			out = append(out, c)
		}
	}
	return out
}

// Interactive excludes utility commands and headless agents, including when
// global options precede the subcommand. Option values are never subcommands.
func Interactive(agent config.Agent, args []string) bool {
	kinds := map[string]config.ValueKind{}
	for _, flag := range agent.ValueFlags {
		kinds[flag] = config.ValueRequired
	}
	for _, flag := range agent.DropArgs {
		kinds[flag.Flag] = flag.Value
	}
	blocked := map[string]bool{"exec": true, "e": true, "app-server": true, "mcp-server": true, "mcp": true, "login": true, "logout": true, "features": true, "debug": true, "review": true, "completion": true, "doctor": true, "update": true, "auth": true, "agents": true, "attach": true, "plugin": true, "hooks": true}
	if agent.Name == "droid" {
		for _, command := range []string{"daemon", "search", "find", "computer", "help"} {
			blocked[command] = true
		}
	}
	firstPositional := true
	for i := 0; i < len(args); i++ {
		a, _, inline := strings.Cut(args[i], "=")
		if a == "--" {
			break
		}
		if a == "--help" || a == "-h" || a == "--version" || a == "-V" || agent.Name == "droid" && a == "-v" || agent.Name == "claude" && (a == "-p" || a == "--print" || a == "--bg" || a == "--background") {
			return false
		}
		if !strings.HasPrefix(a, "-") && firstPositional {
			if blocked[a] {
				return false
			}
			firstPositional = false
		}
		kind := kinds[a]
		if !inline && (kind == config.ValueRequired || kind == config.ValueOptional && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-")) {
			i++
		}
	}
	return true
}

func FindOpenID(paths []string, pattern string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	ids := map[string]bool{}
	for _, p := range paths {
		m := re.FindStringSubmatch(p)
		if len(m) < 2 || !ValidID(m[1]) {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 65536), 4<<20)
		var meta struct {
			Type    string `json:"type"`
			Payload struct {
				ID         string          `json:"id"`
				Source     json.RawMessage `json:"source"`
				Originator string          `json:"originator"`
			} `json:"payload"`
		}
		ok := sc.Scan() && json.Unmarshal(sc.Bytes(), &meta) == nil
		f.Close()
		if !ok || meta.Type != "session_meta" || meta.Payload.ID != m[1] {
			continue
		}
		var source string
		if json.Unmarshal(meta.Payload.Source, &source) != nil {
			continue
		}
		// The shared daemon records terminal sessions as "vscode" with a
		// codex-tui originator. Resuming locally retains that original metadata.
		// Require the TUI originator so editor and subagent sessions stay excluded.
		if source != "cli" && !(source == "vscode" && meta.Payload.Originator == "codex-tui") {
			continue
		}
		ids[m[1]] = true
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("found %d identifiable CLI transcripts (need exactly one)", len(ids))
	}
	for id := range ids {
		return id, nil
	}
	panic("unreachable")
}

// Recent Claude releases publish exact PID/session metadata. Validate the
// process birth time as well as PID, so stale files cannot survive PID reuse.
func ClaudeProcessSession(dir string, pid int, started time.Time) (string, string) {
	record := readClaudeProcess(dir, pid, started)
	return record.ID, record.CWD
}

func Discover(cfg *config.Config, processes []proc.Process) ([]Session, []string, error) {
	entries, err := registry.Load(cfg.RegistryPath())
	if err != nil {
		return nil, nil, err
	}
	live := registry.Current(entries)
	candidates := Candidates(cfg, processes)
	var pids []int
	for _, c := range candidates {
		if c.Agent.SessionSource == config.SourceOpenFile {
			pids = append(pids, c.Process.PID)
		}
	}
	files := proc.OpenFiles(pids)
	var sessions []Session
	var warnings []string
	for _, c := range candidates {
		id := ""
		cwd := ""
		started := proc.StartTime(c.Process.PID)
		if e, ok := live[c.Pane]; ok && e.Agent == c.Agent.Name && (e.PID == c.Process.PID || e.PID == c.Process.PPID) {
			if !e.Time.Before(started) {
				id, cwd = e.SessionID, e.CWD
			}
		}
		if c.Agent.Name == "claude" {
			dir := c.Process.Env["CLAUDE_CONFIG_DIR"]
			if dir == "" {
				home, _ := os.UserHomeDir()
				dir = filepath.Join(home, ".claude")
			}
			if exact, working := ClaudeProcessSession(dir, c.Process.PID, started); exact != "" {
				id, cwd = exact, working
			}
		}
		if c.Agent.SessionSource == config.SourceOpenFile {
			id, err = FindOpenID(files[c.Process.PID], c.Agent.OpenFileRegex)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s PID %d: %v", c.Agent.Name, c.Process.PID, err))
				continue
			}
		}
		if !ValidID(id) {
			warnings = append(warnings, fmt.Sprintf("%s PID %d: session hook has not recorded this process", c.Agent.Name, c.Process.PID))
			continue
		}
		if cwd == "" {
			cwd = proc.CWD(c.Process.PID)
		}
		args, err := KeepOptions(c.Agent, c.Args)
		if err != nil || !filepath.IsAbs(cwd) {
			warnings = append(warnings, fmt.Sprintf("%s PID %d: cannot safely capture arguments or directory", c.Agent.Name, c.Process.PID))
			continue
		}
		env := map[string]string{}
		for _, k := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR", "FACTORY_HOME_OVERRIDE"} {
			if v := c.Process.Env[k]; v != "" {
				env[k] = v
			}
		}
		sessions = append(sessions, Session{Agent: c.Agent.Name, ID: id, Pane: c.Pane, CWD: cwd, Bin: c.Bin, Args: args, Env: env, PID: c.Process.PID, Started: started})
	}
	counts := map[string]int{}
	for _, s := range sessions {
		counts[s.Pane]++
	}
	var unique []Session
	for _, s := range sessions {
		if counts[s.Pane] == 1 {
			unique = append(unique, s)
		} else {
			warnings = append(warnings, "multiple agents in pane "+s.Pane+"; skipped")
		}
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i].Started.After(unique[j].Started) })
	return unique, warnings, nil
}

// Merge gives disappearing processes a short grace period so a clean Warp
// shutdown cannot wipe the last snapshot while panes are being torn down.
func Merge(previous, next *Snapshot, now time.Time, grace time.Duration) {
	if previous == nil || previous.WarpID != next.WarpID {
		return
	}
	seen := map[string]bool{}
	for _, s := range next.Sessions {
		seen[s.Pane] = true
	}
	for _, s := range previous.Sessions {
		if seen[s.Pane] {
			continue
		}
		if s.MissingSince.IsZero() {
			s.MissingSince = now
		}
		if now.Sub(s.MissingSince) < grace {
			next.Sessions = append(next.Sessions, s)
		}
	}
	retainMissingLayout(previous, next)
}
