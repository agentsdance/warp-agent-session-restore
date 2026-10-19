package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
)

const shellBegin = "# >>> warp-agent-session-restore >>>"
const shellEnd = "# <<< warp-agent-session-restore <<<"

func shellBlock(text string, replacement string) (string, error) {
	i, j := strings.Index(text, shellBegin), strings.Index(text, shellEnd)
	if i < 0 && j < 0 {
		if replacement == "" {
			return text, nil
		}
		return strings.TrimRight(text, "\n") + "\n\n" + replacement + "\n", nil
	}
	if i < 0 || j < i {
		return "", fmt.Errorf("incomplete warp-agent-session-restore shell block")
	}
	end := j + len(shellEnd)
	if end < len(text) && text[end] == '\n' {
		end++
	}
	if replacement != "" {
		replacement += "\n"
	}
	return text[:i] + replacement + text[end:], nil
}

// New Codex versions share a daemon, which owns all transcript files. Running
// the TUI locally gives each pane its own exact transcript association. This
// changes no model, permission, approval or sandbox settings.
func (a *App) installShell() (string, error) {
	path := filepath.Join(a.Home, ".zshrc")
	if zdot := os.Getenv("ZDOTDIR"); zdot != "" {
		path = filepath.Join(zdot, ".zshrc")
	}
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	help, err := exec.Command("codex", "--help").Output()
	if err != nil || !strings.Contains(string(help), "--no-daemon") {
		return "", nil
	}
	scriptPath := filepath.Join(a.Config.StateDir, "shell.zsh")
	script := `# Only Warp's interactive zsh sessions need a dedicated Codex process.
if [[ -o interactive && -n ${WARP_TERMINAL_SESSION_UUID-} ]]; then
  if (( ! ${+functions[codex]} && ! ${+aliases[codex]} )); then
    codex() { command codex --no-daemon "$@"; }
  fi
fi
`
	block := shellBegin + "\n[[ -r " + session.Quote(scriptPath) + " ]] && source " + session.Quote(scriptPath) + "\n" + shellEnd
	updated, err := shellBlock(string(data), block)
	if err != nil {
		return "", err
	}
	if err = state.Write(scriptPath, []byte(script), 0o600); err != nil {
		return "", err
	}
	if string(data) != updated {
		if len(data) > 0 {
			if err = state.Write(path+".ras-backup-"+time.Now().Format("20060102T150405.000000000"), data, 0o600); err != nil {
				return "", err
			}
		}
		if err = state.Write(path, []byte(updated), 0o600); err != nil {
			return "", err
		}
	}
	return path, nil
}
