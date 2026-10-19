package app

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type launchctlExit int

func (e launchctlExit) Error() string { return fmt.Sprintf("exit status %d", e) }
func (e launchctlExit) ExitCode() int { return int(e) }

func TestLaunchAgentReloadWaitsForTeardown(t *testing.T) {
	for _, tc := range []struct {
		name                                      string
		bootoutCode, transientFailures, finalCode int
		wantBootstraps, wantPauses                int
		wantError                                 string
	}{
		{name: "first install", bootoutCode: 3, wantBootstraps: 1},
		{name: "immediate reinstall", wantBootstraps: 1},
		{name: "shutdown exceeds old retry window", transientFailures: 6, wantBootstraps: 7, wantPauses: 6},
		{name: "teardown never finishes", transientFailures: 100, wantBootstraps: 16, wantPauses: 15, wantError: "register service"},
		{name: "bootout denied", bootoutCode: 1, wantError: "unload previous service"},
		{name: "bootstrap denied", finalCode: 13, wantBootstraps: 1, wantError: "register service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bootouts, bootstraps, pauses := 0, 0, 0
			run := func(args ...string) ([]byte, error) {
				code := 0
				switch args[0] {
				case "bootout":
					bootouts++
					if !reflect.DeepEqual(args, []string{"bootout", "gui/501/test-agent"}) {
						t.Fatal(args)
					}
					code = tc.bootoutCode
				case "bootstrap":
					bootstraps++
					if !reflect.DeepEqual(args, []string{"bootstrap", "gui/501", "/agent.plist"}) {
						t.Fatal(args)
					}
					code = tc.finalCode
					if bootstraps <= tc.transientFailures {
						code = 5
					}
				default:
					t.Fatal(args)
				}
				if code != 0 {
					return []byte("launchd diagnostic\nTry re-running the command as root for richer errors."), launchctlExit(code)
				}
				return nil, nil
			}
			err := reloadLaunchAgent("gui/501", "test-agent", "/agent.plist", run, func(d time.Duration) {
				if d != time.Second {
					t.Fatal(d)
				}
				pauses++
			})
			if bootouts != 1 || bootstraps != tc.wantBootstraps || pauses != tc.wantPauses {
				t.Fatalf("bootout=%d bootstrap=%d pauses=%d", bootouts, bootstraps, pauses)
			}
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) || !strings.Contains(err.Error(), "launchd diagnostic") || strings.Contains(err.Error(), "as root") {
				t.Fatalf("wrong diagnostic: %v", err)
			}
		})
	}
}
