package app

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func launchctl(args ...string) ([]byte, error) {
	return exec.Command("/bin/launchctl", args...).CombinedOutput()
}

func exitCode(err error) int {
	var status interface{ ExitCode() int }
	if errors.As(err, &status) {
		return status.ExitCode()
	}
	return -1
}

// bootout returns before launchd has finished removing a running job. Keep
// retrying bootstrap's transient EIO for up to 15 seconds, without repeatedly
// unloading the service or masking permission/configuration errors.
func reloadLaunchAgent(domain, name, plist string, run func(...string) ([]byte, error), pause func(time.Duration)) error {
	output, err := run("bootout", domain+"/"+name)
	if err != nil && exitCode(err) != 3 { // ESRCH: nothing was installed yet.
		return fmt.Errorf("unload previous service: %w: %s", err, launchctlDiagnostic(output))
	}
	const retries = 15
	for attempt := 0; attempt <= retries; attempt++ {
		output, err = run("bootstrap", domain, plist)
		if err == nil {
			return nil
		}
		if exitCode(err) != 5 || attempt == retries {
			return fmt.Errorf("register service: %w: %s", err, launchctlDiagnostic(output))
		}
		pause(time.Second)
	}
	panic("unreachable")
}

func launchctlDiagnostic(output []byte) string {
	// This is a per-user LaunchAgent. launchctl's generic suggestion to rerun
	// as root does not fix a teardown race and can lead to root-owned settings.
	return strings.TrimSpace(strings.ReplaceAll(string(output), "Try re-running the command as root for richer errors.", ""))
}
