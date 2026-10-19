package app

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/agentsdance/warp-agent-session-restore/internal/config"
	"github.com/agentsdance/warp-agent-session-restore/internal/session"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
)

const label = "org.ai-infra.warp-agent-session-restore"

type installation struct {
	Binary   string `json:"binary"`
	Settings string `json:"settings"`
	Hook     string `json:"hook"`
	Plist    string `json:"plist"`
	Shell    string `json:"shell,omitempty"`
}

func MergeHooks(data []byte, command string, remove bool) ([]byte, error) {
	doc := map[string]any{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
	}
	if doc == nil {
		return nil, fmt.Errorf("Claude settings must be a JSON object")
	}
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		if _, exists := doc["hooks"]; exists {
			return nil, fmt.Errorf("Claude hooks must be an object")
		}
		hooks = map[string]any{}
		doc["hooks"] = hooks
	}
	for _, event := range []string{"SessionStart", "SessionEnd", "UserPromptSubmit"} {
		entries, ok := hooks[event].([]any)
		if !ok && hooks[event] != nil {
			return nil, fmt.Errorf("invalid %s hooks", event)
		}
		var kept []any
		for _, entry := range entries {
			m, ok := entry.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid hook entry")
			}
			children, ok := m["hooks"].([]any)
			if !ok {
				return nil, fmt.Errorf("invalid hook handlers")
			}
			var other []any
			for _, child := range children {
				h, ok := child.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("invalid hook handler")
				}
				if h["command"] != command {
					other = append(other, child)
				}
			}
			if len(other) > 0 {
				m["hooks"] = other
				kept = append(kept, m)
			}
		}
		if !remove {
			kept = append(kept, map[string]any{"matcher": "", "hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}}})
		}
		if len(kept) > 0 {
			hooks[event] = kept
		} else {
			delete(hooks, event)
		}
	}
	return json.MarshalIndent(doc, "", "  ")
}

func (a *App) Install(noService bool) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("installation currently supports macOS only")
	}
	bin := filepath.Join(a.Config.StateDir, "bin", "warp-agent-session-restore")
	settings := filepath.Join(a.Home, ".claude", "settings.json")
	if custom := os.Getenv("CLAUDE_CONFIG_DIR"); custom != "" {
		settings = filepath.Join(custom, "settings.json")
	}
	if target, err := filepath.EvalSymlinks(settings); err == nil {
		settings = target
	}
	hook := session.Shell([]string{bin, "--home", a.Home, "hook", "claude"})
	data, err := os.ReadFile(settings)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	merged, err := MergeHooks(data, hook, false)
	if err != nil {
		return fmt.Errorf("could not set up Claude Code session tracking in %s: %w", settings, err)
	}
	plist := filepath.Join(a.Home, "Library", "LaunchAgents", label+".plist")
	manifest := installation{Binary: bin, Settings: settings, Hook: hook, Plist: plist}
	// Validate all configuration before changing any existing user file.
	executable, err := os.ReadFile(a.Binary)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "Setting up Warp Agent Session Restore...")
	if err = state.Write(bin, executable, 0o700); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "  App installed:", bin)
	if len(data) > 0 {
		backup := settings + ".ras-backup-" + time.Now().Format("20060102T150405.000000000")
		if err = state.Write(backup, data, 0o600); err != nil {
			return err
		}
		fmt.Fprintln(a.Out, "  Claude Code settings backed up:", backup)
	}
	if err = state.Write(settings, append(merged, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "  Claude Code session tracking set up.")
	manifest.Shell, err = a.installShell()
	if err != nil {
		return err
	}
	if manifest.Shell != "" {
		fmt.Fprintln(a.Out, "  Codex helper set up for new Warp zsh tabs:", manifest.Shell)
	}
	a.Config.LocalCodex = manifest.Shell != ""
	if err = config.Save(a.Home, a.Config); err != nil {
		return err
	}
	if err = state.Save(filepath.Join(a.Config.StateDir, "installation.json"), manifest); err != nil {
		return err
	}
	if noService {
		a.printInstallSummary(manifest, false)
		return nil
	}
	if err = state.Write(plist, []byte(a.plist(bin)), 0o600); err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = exec.Command("/bin/launchctl", "bootout", domain+"/"+label).Run()
	if out, err := exec.Command("/bin/launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
		return fmt.Errorf("setup saved, but macOS could not start background recovery: %w: %s\nRetry setup: %s\nRun recovery in this terminal: %s", err, out,
			session.Shell([]string{bin, "--home", a.Home, "install"}),
			session.Shell([]string{bin, "--home", a.Home, "watch"}))
	}
	a.printInstallSummary(manifest, true)
	return nil
}

func (a *App) printInstallSummary(installed installation, service bool) {
	command := func(args ...string) string {
		return session.Shell(append([]string{installed.Binary, "--home", a.Home}, args...))
	}
	fmt.Fprintln(a.Out, "\nSetup complete.")
	if service {
		fmt.Fprintf(a.Out, "  Background service registered to start now and at login. Captures every %d seconds.\n", a.Config.PollSeconds)
		if a.Config.AutoResume {
			fmt.Fprintln(a.Out, "  Automatic restore is on. Saved sessions open in new windows when Warp restarts.")
		} else {
			fmt.Fprintln(a.Out, "  Automatic restore is off in your configuration. Open saved sessions with:")
			fmt.Fprintln(a.Out, "   ", command("restore"))
		}
	} else {
		fmt.Fprintln(a.Out, "  Background service setup skipped (--no-service). Start recovery in this terminal with:")
		fmt.Fprintln(a.Out, "   ", command("watch"))
	}
	fmt.Fprintln(a.Out, "\nNext: open a new Warp tab and start Codex or Claude Code.")
	if installed.Shell != "" {
		fmt.Fprintln(a.Out, "To enable the Codex helper in an existing zsh tab:")
		fmt.Fprintln(a.Out, "  source", session.Quote(filepath.Join(a.Config.StateDir, "shell.zsh")))
	}
	fmt.Fprintln(a.Out, "If macOS asks to access other apps' data, allow it so recovery can save Warp's layout.")
	fmt.Fprintln(a.Out, "\nCheck saved sessions:", command("status"))
	fmt.Fprintln(a.Out, "Remove setup:        ", command("uninstall"))
	fmt.Fprintln(a.Out, "Logs:                ", filepath.Join(a.Config.StateDir, "watch.log"))
}

func (a *App) plist(bin string) string {
	escape := func(s string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>--home</string><string>%s</string><string>watch</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>10</integer>
<key>ProcessType</key><string>Background</string>
<key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string></dict>
<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, label, escape(bin), escape(a.Home), escape(os.Getenv("PATH")), escape(filepath.Join(a.Config.StateDir, "watch.log")), escape(filepath.Join(a.Config.StateDir, "watch.log")))
}

func (a *App) Uninstall() error {
	var installed installation
	if err := state.Load(filepath.Join(a.Config.StateDir, "installation.json"), &installed); err != nil {
		return err
	}
	data, err := os.ReadFile(installed.Settings)
	if err != nil {
		return err
	}
	merged, err := MergeHooks(data, installed.Hook, true)
	if err != nil {
		return err
	}
	_ = exec.Command("/bin/launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+label).Run()
	if err = state.Write(installed.Settings, append(merged, '\n'), 0o600); err != nil {
		return err
	}
	if installed.Shell != "" {
		data, err := os.ReadFile(installed.Shell)
		if err != nil {
			return err
		}
		updated, err := shellBlock(string(data), "")
		if err != nil {
			return err
		}
		if err = state.Write(installed.Shell, []byte(updated), 0o600); err != nil {
			return err
		}
	}
	if err = os.Remove(installed.Plist); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Fprintln(a.Out, "Stopped service and removed managed hooks. Snapshots and installed binary preserved.")
	return nil
}
