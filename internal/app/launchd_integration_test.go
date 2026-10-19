package app

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/state"
	"github.com/google/uuid"
)

// Opt-in because this test creates a real, isolated LaunchAgent in the user's
// GUI domain. It never unloads the installed recovery service.
func TestLaunchAgentReinstallWithDelayedExit(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("WARP_RESTORE_TEST_LAUNCHD") != "1" {
		t.Skip("set WARP_RESTORE_TEST_LAUNCHD=1 on a logged-in Mac")
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if out, err := launchctl("print", domain); err != nil {
		t.Fatalf("GUI domain unavailable: %v %s", err, out)
	}
	name := "org.ai-infra.warp-restore-test-" + uuid.NewString()
	dir := t.TempDir()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	escape := func(s string) string { var b bytes.Buffer; xml.EscapeText(&b, []byte(s)); return b.String() }
	plist := filepath.Join(dir, "agent.plist")
	data := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>-test.run=^TestLaunchAgentSlowExitFixture$</string></array>
<key>RunAtLoad</key><true/><key>ExitTimeOut</key><integer>10</integer>
<key>EnvironmentVariables</key><dict><key>WARP_RESTORE_LAUNCHD_FIXTURE_DIR</key><string>%s</string></dict>
<key>StandardOutPath</key><string>%s/output.log</string><key>StandardErrorPath</key><string>%s/error.log</string>
</dict></plist>`, name, escape(bin), escape(dir), escape(dir), escape(dir))
	if err := state.Write(plist, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { launchctl("bootout", domain+"/"+name) })
	waitPID := func(previous string) string {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			b, _ := os.ReadFile(filepath.Join(dir, "ready"))
			pid := strings.TrimSpace(string(b))
			if pid != "" && pid != previous {
				return pid
			}
			time.Sleep(100 * time.Millisecond)
		}
		log, _ := os.ReadFile(filepath.Join(dir, "error.log"))
		t.Fatalf("replacement did not start: %s", log)
		return ""
	}
	if err := reloadLaunchAgent(domain, name, plist, launchctl, time.Sleep); err != nil {
		t.Fatal(err)
	}
	previous := waitPID("")
	started := time.Now()
	attempts := 0
	run := func(args ...string) ([]byte, error) {
		if args[0] == "bootstrap" {
			attempts++
		}
		return launchctl(args...)
	}
	if err := reloadLaunchAgent(domain, name, plist, run, time.Sleep); err != nil {
		t.Fatal(err)
	}
	next := waitPID(previous)
	t.Logf("reinstalled delayed-exit job: PID %s -> %s; %d bootstrap attempts; %s", previous, next, attempts, time.Since(started))
	if time.Since(started) < 2*time.Second {
		t.Fatal("fixture did not exercise delayed teardown")
	}
}

func TestLaunchAgentSlowExitFixture(t *testing.T) {
	dir := os.Getenv("WARP_RESTORE_LAUNCHD_FIXTURE_DIR")
	if dir == "" {
		t.Skip("launchd fixture only")
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	defer signal.Stop(stop)
	if err := state.Write(filepath.Join(dir, "ready"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	<-stop
	time.Sleep(2 * time.Second)
}
