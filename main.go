package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentsdance/warp-agent-session-restore/internal/app"
	"github.com/agentsdance/warp-agent-session-restore/internal/proc"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "warp-agent-session-restore:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("warp-agent-session-restore", flag.ContinueOnError)
	flags.StringVar(&home, "home", home, "configuration home (for isolated testing)")
	showVersion := flags.Bool("version", false, "show the installed version")
	flags.Usage = usage
	if err = flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Println("warp-agent-session-restore", version)
		return nil
	}
	args = flags.Args()
	if len(args) == 0 || args[0] == "help" {
		usage()
		return nil
	}
	a, err := app.New(home)
	if err != nil {
		return err
	}
	sub := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "snapshot":
		current := sub.Bool("current", false, "accept current tabs as the baseline; archive the previous snapshot and pending recovery")
		if err = sub.Parse(args[1:]); err != nil {
			return err
		}
		if sub.NArg() != 0 {
			return fmt.Errorf("snapshot takes no positional arguments")
		}
		capture := a.Capture
		if *current {
			capture = a.CaptureCurrent
		}
		s, err := capture()
		if err != nil {
			return err
		}
		if *current {
			fmt.Println("Accepted current tabs. Previous snapshot and recovery state are preserved under", a.Config.StateDir+"/reconciliations")
		}
		fmt.Printf("Saved %d sessions to %s\n", len(s.Sessions), a.Config.SnapshotPath())
		for _, w := range s.Warnings {
			fmt.Fprintln(os.Stderr, w)
		}
		return nil
	case "status":
		return a.Status()
	case "watch":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return a.Watch(ctx)
	case "hook":
		if len(args) != 2 {
			return fmt.Errorf("usage: hook <claude|droid>")
		}
		return a.Hook(args[1], os.Stdin)
	case "install":
		noService := sub.Bool("no-service", false, "set up session tracking without registering the background service")
		if err = sub.Parse(args[1:]); err != nil {
			return err
		}
		return a.Install(*noService)
	case "uninstall":
		return a.Uninstall()
	case "restore":
		path := sub.String("snapshot", "", "snapshot file (default latest)")
		dry := sub.Bool("dry-run", false, "print Warp tab/launch configurations without launching")
		if err = sub.Parse(args[1:]); err != nil {
			return err
		}
		s, err := a.LoadSnapshot(*path)
		if err != nil {
			return err
		}
		ps, err := proc.List()
		if err != nil {
			return err
		}
		pathOut, err := a.Restore(s, proc.WarpID(ps), *dry)
		if err == nil && !*dry {
			fmt.Println("Opened Warp recovery configuration:", pathOut)
		}
		return err
	case "run-session":
		path := sub.String("snapshot", "", "immutable recovery snapshot")
		pane := sub.String("pane", "", "original pane UUID")
		batch := sub.String("batch", "", "restore batch UUID")
		if err = sub.Parse(args[1:]); err != nil {
			return err
		}
		return a.RunSession(*path, *pane, *batch)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Println(`warp-agent-session-restore — resume Codex, Claude Code, and Droid after restarting Warp

Usage: warp-agent-session-restore [--home DIR] COMMAND
       warp-restore [--home DIR] COMMAND

Use --version to show the installed version.

  install       Set up session tracking and automatic recovery in the background
  snapshot      Capture sessions (--current accepts current tabs after backing up recovery)
  status        Show saved sessions, live tabs and recovery progress
  restore       Open saved sessions in Warp (--dry-run, --snapshot FILE)
  watch         Run session recovery in this terminal
  uninstall     Stop background recovery and remove its setup; keep saved sessions

macOS / Warp Stable. Current Warp builds restore into the active window;
older builds use new windows. Native empty tabs may remain. No prompts are replayed.`)
}
