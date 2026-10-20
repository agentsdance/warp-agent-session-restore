package app

import (
	"fmt"
	"path/filepath"

	"github.com/agentsdance/warp-agent-session-restore/internal/session"
)

// Warp identifies CLI agents by the basename of the shell's first command.
// Private, batch-local launchers preserve that identity while exec'ing our
// supervisor. They are never added to PATH and never replace the user's agent.
// https://github.com/warpdotdev/warp/blob/master/app/src/terminal/cli_agent.rs
func agentRunnerPath(snapshot, batch, agent string) (string, error) {
	if !session.ValidID(batch) || !filepath.IsAbs(snapshot) {
		return "", fmt.Errorf("invalid recovery path or batch")
	}
	switch agent {
	case "claude", "codex", "droid":
		return filepath.Join(filepath.Dir(snapshot), "runners", batch, agent), nil
	default:
		return "", fmt.Errorf("unsupported agent %q", agent)
	}
}

func agentRunners(s *session.Snapshot, binary, home, snapshot, batch string) ([]launchFile, error) {
	seen := map[string]bool{}
	var files []launchFile
	for _, saved := range s.Sessions {
		path, err := agentRunnerPath(snapshot, batch, saved.Agent)
		if err != nil {
			return nil, err
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		script := "#!/bin/sh\nexec " + session.Shell([]string{binary, "--home", home, "run-session"}) + " \"$@\"\n"
		files = append(files, launchFile{path, []byte(script)})
	}
	return files, nil
}
