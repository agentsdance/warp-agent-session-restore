// Package registry stores the session IDs reported by agent lifecycle hooks.
// Hooks tie Claude Code and Droid conversation IDs to their actual Warp process.
package registry

import (
	"bufio"
	"encoding/json"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Entry is one recorded lifecycle event.
type Entry struct {
	Time      time.Time `json:"time"`
	Event     string    `json:"event"` // "start" or "end"
	Agent     string    `json:"agent"`
	SessionID string    `json:"session_id"`
	CWD       string    `json:"cwd"`
	PaneUUID  string    `json:"pane_uuid"`
	// Source is the agent's own reason for the event, such as startup or resume.
	Source     string `json:"source,omitempty"`
	PID        int    `json:"pid,omitempty"`
	Transcript string `json:"transcript,omitempty"`
}

// Append adds an entry to the registry file.
func Append(path string, e Entry) error {
	lock, err := state.Lock(path+".lock", true)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// Load reads every entry, skipping malformed lines.
func Load(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Current returns the newest live session per pane UUID. A pane whose most
// recent event is an end is omitted, and so is an entry without a pane UUID.
// Sessions restart on /clear and on compaction, so latest-wins is the rule.
func Current(entries []Entry) map[string]Entry {
	latest := map[string]Entry{}
	for _, e := range entries {
		if e.PaneUUID == "" {
			continue
		}
		prev, ok := latest[e.PaneUUID]
		if ok && prev.Time.After(e.Time) {
			continue
		}
		// An old session's delayed end must not erase its replacement.
		if ok && e.Event == "end" && e.SessionID != prev.SessionID {
			continue
		}
		latest[e.PaneUUID] = e
	}
	for pane, e := range latest {
		if e.Event == "end" {
			delete(latest, pane)
		}
	}
	return latest
}

// ByCWD groups live sessions by working directory. Used only for the fallback
// path, where a pane UUID is unavailable.
func ByCWD(latest map[string]Entry) map[string][]Entry {
	out := map[string][]Entry{}
	for _, e := range latest {
		out[e.CWD] = append(out[e.CWD], e)
	}
	return out
}

// Prune rewrites the registry keeping only entries newer than the cutoff, so
// the file does not grow without bound.
func Prune(path string, keepSince time.Time) error {
	lock, err := state.Lock(path+".lock", true)
	if err != nil {
		return err
	}
	defer lock.Close()
	entries, err := Load(path)
	if err != nil || len(entries) == 0 {
		return err
	}
	var kept []Entry
	for _, e := range entries {
		if e.Time.After(keepSince) {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(entries) {
		return nil
	}

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range kept {
		b, err := json.Marshal(e)
		if err != nil {
			continue
		}
		w.Write(append(b, '\n'))
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
