package app

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/agentsdance/warp-agent-session-restore/internal/config"
	"github.com/agentsdance/warp-agent-session-restore/internal/proc"
	"github.com/agentsdance/warp-agent-session-restore/internal/session"
)

// hookProcess follows the hook shell to the actual agent. Droid's TUI uses a
// direct stream-jsonrpc child for session events; bind those to the TUI, never
// to an arbitrary ancestor or a standalone headless job.
func hookProcess(agent config.Agent, ps []proc.Process, pid int, hydrate func(*proc.Process) error) (owner int, ignored bool) {
	byPID := map[int]proc.Process{}
	for _, p := range ps {
		byPID[p.PID] = p
	}
	argsFor := func(p proc.Process) ([]string, bool) {
		for i, arg := range p.Argv {
			if i > 1 {
				break
			}
			if slices.Contains(agent.Match, strings.TrimSuffix(filepath.Base(arg), ".js")) {
				return p.Argv[i+1:], true
			}
		}
		return nil, false
	}
	for depth := 0; depth < 12 && pid > 1; depth++ {
		p, ok := byPID[pid]
		if !ok {
			break
		}
		if hydrate(&p) == nil {
			if args, match := argsFor(p); match {
				if session.Interactive(agent, args) {
					return p.PID, false
				}
				if agent.Name == "droid" && slices.Equal(args, []string{"exec", "--input-format", "stream-jsonrpc", "--output-format", "stream-jsonrpc"}) {
					parent, ok := byPID[p.PPID]
					if ok && hydrate(&parent) == nil {
						parentArgs, match := argsFor(parent)
						if match && session.Interactive(agent, parentArgs) {
							return parent.PID, false
						}
					}
				}
				return 0, true
			}
		}
		pid = p.PPID
	}
	return 0, false
}
