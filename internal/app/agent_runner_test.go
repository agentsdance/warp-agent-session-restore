package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
	"github.com/google/uuid"
)

func TestAgentLaunchersKeepIdentityAndForwardExactArguments(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "droid"} {
		t.Run(agent, func(t *testing.T) {
			a, s := fixture(t)
			a.Home = filepath.Join(a.Home, "spaces ' $HOME `false`\n")
			a.Config.StateDir = filepath.Join(a.Home, "state")
			s.Sessions[0].Agent = agent
			a.Binary = filepath.Join(a.Home, "actual helper")
			if err := state.Write(a.Binary, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\"\nexit 23\n"), 0700); err != nil {
				t.Fatal(err)
			}
			snapshot := filepath.Join(a.Config.StateDir, "snapshot.json")
			batch := uuid.NewString()
			plan, err := a.prepareLaunch(s, snapshot, batch)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Runners) != 1 || filepath.Base(plan.Runners[0].Path) != agent {
				t.Fatalf("wrong agent launcher: %+v", plan.Runners)
			}
			runner := plan.Runners[0]
			if err := state.Write(runner.Path, runner.Data, 0700); err != nil {
				t.Fatal(err)
			}
			layout, err := buildLaunch(s, a.Binary, a.Home, snapshot, batch)
			if err != nil {
				t.Fatal(err)
			}
			command := layout.Windows[0].Tabs[0].Layout.Panes[0].Commands[0].Exec
			if !strings.HasPrefix(command, session.Quote(runner.Path)+" ") {
				t.Fatalf("first command loses agent identity: %s", command)
			}
			out, err := exec.Command("/bin/sh", "-c", command).Output()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 23 {
				t.Fatalf("launcher lost exit status: %v", err)
			}
			got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			want := []string{"--home", a.Home, "run-session", "--snapshot", snapshot, "--pane", s.Sessions[0].Pane, "--batch", batch}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("arguments changed:\n%q\n%q", got, want)
			}
		})
	}
}

func TestRestoreWritesPrivateAgentLaunchersBeforeDispatch(t *testing.T) {
	a, s := fixture(t)
	a.launch = func(string) error {
		var done restoreState
		if err := state.Load(a.restoreStatePath(), &done); err != nil {
			return err
		}
		path, err := agentRunnerPath(filepath.Join(a.Config.StateDir, "recoveries", done.Batch+".json"), done.Batch, "codex")
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm() != 0700 {
			t.Fatalf("runner permissions: %v", info.Mode())
		}
		data, err := os.ReadFile(path)
		if err == nil && !bytes.Contains(data, []byte(session.Quote(a.Binary))) {
			t.Fatal("launcher does not exec supervisor")
		}
		return err
	}
	if _, err := a.Restore(s, "next", false); err != nil {
		t.Fatal(err)
	}
}
