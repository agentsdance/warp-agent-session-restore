package session

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/agentsdance/warp-agent-session-restore/internal/registry"
)

// Claude allocates an ID before the first message, but cannot --resume that ID
// until it has written a transcript. Absence alone is not evidence of an empty
// conversation: require the original startup hook and no prompt/resume events.
// Read directories explicitly so permission failures cannot look like absence.
func ClaudeHistory(home string, s Session, entries []registry.Entry) (string, error) {
	if s.Agent != "claude" || !ValidID(s.ID) {
		return "", fmt.Errorf("invalid Claude session")
	}
	root := filepath.Join(agentHome(home, s), "projects")
	projects, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, p := range projects {
		if !p.IsDir() && p.Type()&os.ModeSymlink == 0 {
			continue
		}
		path := filepath.Join(root, p.Name(), s.ID+".jsonl")
		info, err := os.Stat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("Claude transcript is not a regular file: %s", path)
			}
			return "present", nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
	}
	if s.ClaudeHistory == "present" {
		return "", nil // A previously saved transcript has disappeared.
	}
	started := false
	for _, e := range entries {
		if e.Agent != "claude" || e.SessionID != s.ID {
			continue
		}
		if e.Event != "start" {
			continue
		}
		if e.Source != "startup" {
			return "", nil // Includes legacy UserPromptSubmit records.
		}
		if e.PID != s.PID || e.PaneUUID != s.Pane || e.CWD != s.CWD || e.Time.Before(s.Started) {
			continue
		}
		// Bind this evidence to Claude's storage root as well as the process.
		expected := filepath.Join(root, filepath.Base(filepath.Dir(e.Transcript)), s.ID+".jsonl")
		if e.Transcript != "" && filepath.Clean(e.Transcript) == expected {
			started = true
		}
	}
	if started {
		return "empty", nil
	}
	return "", nil
}

// ClaudeRestoreCommand rechecks at launch: a prompt may have been submitted
// after the last snapshot. Never substitute a new chat for missing history.
func ClaudeRestoreCommand(home string, s Session, entries []registry.Entry) ([]string, bool, error) {
	history, err := ClaudeHistory(home, s, entries)
	if err != nil {
		return nil, false, fmt.Errorf("cannot inspect Claude history: %w", err)
	}
	switch history {
	case "present":
		cmd, err := s.Command()
		return cmd, false, err
	case "empty":
		if _, err := s.Command(); err != nil {
			return nil, false, err
		}
		return append([]string{s.Bin, "--session-id", s.ID}, s.Args...), true, nil
	default:
		return nil, false, fmt.Errorf("Claude conversation %s has no transcript; cannot confirm it was an empty tab, so recovery has preserved the saved session", s.ID)
	}
}
