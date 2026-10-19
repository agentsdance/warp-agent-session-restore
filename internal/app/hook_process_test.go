package app

import (
	"testing"

	"github.com/agentsdance/warp-agent-session-restore/internal/config"
	"github.com/agentsdance/warp-agent-session-restore/internal/proc"
)

func TestDroidHookBindsOnlyToItsInteractiveOwner(t *testing.T) {
	agent := *config.Default(t.TempDir()).AgentByName("droid")
	bridge := []string{"droid", "exec", "--input-format", "stream-jsonrpc", "--output-format", "stream-jsonrpc"}
	for _, tc := range []struct {
		name          string
		child, parent []string
		direct        bool
		want          int
	}{
		{"interactive", []string{"droid", "resume", "id"}, nil, true, 30},
		{"TUI bridge", bridge, []string{"droid", "--settings", "file", "resume", "id"}, true, 20},
		{"standalone bridge", bridge, []string{"zsh"}, true, 0},
		{"headless child", []string{"droid", "exec", "prompt"}, []string{"droid"}, true, 0},
		{"headless owner", bridge, []string{"droid", "--settings", "file", "exec", "prompt"}, true, 0},
		{"indirect bridge", bridge, []string{"droid"}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parentPID := 20
			if !tc.direct {
				parentPID = 25
			}
			ps := []proc.Process{{PID: 40, PPID: 30, Argv: []string{"sh"}}, {PID: 30, PPID: parentPID, Argv: tc.child}, {PID: 25, PPID: 20, Argv: []string{"sh"}}, {PID: 20, PPID: 10, Argv: tc.parent}}
			got, _ := hookProcess(agent, ps, 40, func(*proc.Process) error { return nil })
			if got != tc.want {
				t.Fatalf("owner=%d, want %d", got, tc.want)
			}
		})
	}
}
