# Warp Agent Session Restore

[Website](https://warp-restore.ai-infra.org) · [Releases](https://github.com/agentsdance/warp-agent-session-restore/releases) · [Installer source](install.sh)

Resume Codex and Claude Code conversations after restarting Warp on macOS.
The service snapshots running agents and their Warp pane layout, then opens a
saved launch configuration when it detects a new Warp app process. It resumes
the recorded conversation IDs without replaying prompts.

## Install

Requirements: macOS, Warp Stable, and installed Codex / Claude Code CLIs.

Paste this into Terminal:

```sh
curl -fsSL https://warp-restore.ai-infra.org/install.sh | sh
```

The installer downloads the latest release for Apple Silicon or Intel and
verifies its checksum. No Git, Go, or build tools are needed. Run the same
command again to update.

To review the script before running it:

```sh
curl -fsSL https://warp-restore.ai-infra.org/install.sh -o install.sh
less install.sh
sh install.sh
```

The same [install.sh](install.sh) is available directly from GitHub:

```sh
curl -fsSL https://raw.githubusercontent.com/agentsdance/warp-agent-session-restore/master/install.sh | sh
```

The installer adds both `warp-agent-session-restore` and the shorter
`warp-restore` alias to `/usr/local/bin`. Both names run the same app and use the
same settings, saved sessions, and background service. You can use either from
any directory.

Run `warp-restore --version` or `warp-agent-session-restore --version` to check
the installed version.

Run the installer as your normal user; it asks for your macOS password only if
needed to write the commands to `/usr/local/bin`. Background recovery and
settings belong to your account. Rerunning the installer also adds the alias to
an existing installation.

To choose a different command directory, such as `~/.local/bin`, use:

```sh
curl -fsSL https://warp-restore.ai-infra.org/install.sh | sh -s -- --bin-dir "$HOME/.local/bin"
```

The chosen directory must be on your PATH to run the command by name.

Setup installs the app in `~/.warp-agent-session-restore`, backs up and updates
Claude Code settings, and registers background recovery to start immediately
and at login. The installer reports each completed step, shows whether automatic
restore is enabled, and prints commands for checking saved sessions and removing
the setup.

After setup, open a new Warp tab and start Codex or Claude Code. With automatic
restore enabled (the default), supported sessions are saved every five seconds
and reopened in new windows when Warp restarts.

Check your saved sessions with:

```sh
warp-restore status
```

If there is no snapshot yet, keep Warp open with an agent running and check
again after a few seconds. To remove background recovery and its setup:

```sh
warp-restore uninstall
```

You can also download the binary for your Mac from [Releases](https://github.com/agentsdance/warp-agent-session-restore/releases),
make it executable with `chmod +x`, and run it with `install`.

macOS may ask `warp-agent-session-restore` for permission to access other apps'
data. Allow that prompt so the background service can read Warp's layout
database. Until macOS resolves the prompt, background capture can wait even
though the LaunchAgent is running; foreground capture from Warp may still work.

For Codex versions supporting `--no-daemon`, installation adds a marked block to
`.zshrc` (or `$ZDOTDIR/.zshrc`). In new Warp zsh shells, `codex` runs with
`--no-daemon` so each terminal owns its transcript. Existing aliases/functions
are respected; if you have one, add `--no-daemon` to it yourself. Existing open
shells can activate the helper with:

```sh
source ~/.warp-agent-session-restore/shell.zsh
```

This changes where Codex runs, not its model, approval policy, or sandbox.
For bash/fish or an explicit absolute-path invocation, start Codex with
`codex --no-daemon` yourself. Already-running shared-daemon sessions cannot be
matched reliably to their panes and are reported as skipped. Start or resume
them with `--no-daemon` to make them capturable.

## Background service

The macOS LaunchAgent is named `org.ai-infra.warp-agent-session-restore`.
Its definition is installed at
`~/Library/LaunchAgents/org.ai-infra.warp-agent-session-restore.plist`.
It runs `~/.warp-agent-session-restore/bin/warp-agent-session-restore watch`.
`launchd` starts it at login and restarts it if it exits.

The watcher captures supported sessions every five seconds, preserves the
snapshot while Warp is closed, and detects a restarted Warp by its process ID
and start time. It then opens a launch configuration in new Warp windows and
starts `codex resume <session-id>` or `claude --resume <session-id>`. Persistent
restore markers and per-session locks guard against duplicate launches.

## Commands

```sh
warp-restore snapshot             # capture immediately
warp-restore status               # saved names, counts and identification warnings
warp-restore restore --dry-run    # inspect generated Warp YAML
warp-restore restore              # open saved sessions
warp-restore watch                # foreground service; only one watcher is allowed
warp-restore uninstall            # stop service, remove managed hooks/shell block
```

Every command also works with `warp-agent-session-restore`.

`restore --snapshot /absolute/path/to/snapshot.json` restores a historical
snapshot. Failed launches can be retried with `restore`. Successful sessions are
claimed once per restore batch, and a per-session process lock prevents two
restoration commands from running the same conversation concurrently.

## Behavior and limits

- Each captured session stores its name against its exact conversation ID.
  Codex names come from `session_index.jsonl`; Claude names come from validated
  process metadata, with transcript `custom-title` records as a fallback.
  Names refresh every poll, so `/rename` changes are captured without restarting
  the agent. Automatically derived Claude names are marked separately from user
  names. Sessions without a name receive an agent/short-ID display label.
  Warp tab titles and custom pane/sidebar labels are also saved separately.
  `status` lists the saved names and their sources; the full data is in
  `~/.warp-agent-session-restore/snapshot.json` and retained snapshot history.
- Restoration resumes the same conversation ID. Saved names label unnamed
  single-pane and fallback tabs; existing custom tab titles take precedence.
  Warp's launch YAML cannot set custom split-pane/sidebar labels: these labels
  are backed up, but cannot currently be reapplied by this tool.
- Snapshots are captured every 5 seconds by default. Sessions that disappear
  remain in the snapshot for a 20-second shutdown grace period. A deliberately
  closed session may be restored if Warp restarts within that period. A session
  created and lost between polls cannot be recovered from this tool's snapshot.
- Claude uses exact PID/session metadata when available, with process birth
  time checks. Lifecycle hooks provide a fallback for versions without it.
  New hook events, including the next submitted prompt, populate that fallback.
- Codex identification reads the transcript held by the CLI process, validates
  its session metadata, and excludes subagent transcripts. Ambiguous sessions
  are skipped and reported; working-directory similarity is never used to guess.
- Only interactive agent sessions in Warp are supported. Multiple independent
  agents associated with one pane are skipped. Remote/SSH/tmux multiplexing and
  background/shared-server agents are not supported.
- Restored sessions open in **new Warp windows**. Warp's native restoration may
  also restore its previous windows; this tool does not close those windows.
  Tabs containing agents retain their split structure and ordinary shell panes.
  Exact pane proportions and active-window/tab selection are not restored.
- Warp launch configurations are a legacy but supported format, used here for
  multi-window layouts. Native Warp database schema changes can break capture;
  capture errors preserve the last usable snapshot and appear in `watch.log`.
- `open` accepting a launch URI means it was dispatched, not that each agent
  successfully resumed. Agent startup errors remain visible in the new panes.
  The tool does not automatically submit a new message or continue an interrupted
  action; each agent controls how its conversation resumes.
- Uninstall preserves snapshots and the binary. Existing shell functions last
  until that shell exits (or `unfunction codex` in zsh).

Configuration is `~/.warp-agent-session-restore/config.json`. Set `auto_resume` to
`false` to require manual restoration after a restart, `auto_resume_limit` to cap launches (0 means unlimited),
or adjust `poll_seconds`, `exit_grace_seconds`, and `keep_snapshots`.
`local_codex` is enabled at installation when `--no-daemon` is available.
Snapshots store session IDs, names, directories and CLI options in owner-only files;
they do not copy conversation content. Only `CODEX_HOME` / `CLAUDE_CONFIG_DIR`
environment overrides are retained.

Logs: `~/.warp-agent-session-restore/watch.log`.

## Development

Building from source requires Go 1.25+:

```sh
git clone https://github.com/agentsdance/warp-agent-session-restore.git
cd warp-agent-session-restore
make test
make build
make install
```

`--home DIR` uses a separate configuration/state root for testing. It does not
change the operating-system user or pretend to be a different Warp instance.
Tests exercise the restart transition, retry/duplicate behavior, both agents'
resume commands, shell argument boundaries, hook merge/uninstall preservation,
PID reuse, shutdown grace, and reading a live SQLite WAL layout.

Live validation has covered saved names against native agent metadata, rename
capture and persistence using isolated sessions, and launching resume commands
in Warp with test agents. A full Warp restart with real conversations has not
yet been validated end to end.

### Releases

Push a version tag such as `v0.0.1` to run the release workflow. It runs the
tests, builds macOS binaries for Apple Silicon and Intel, and publishes both
with `SHA256SUMS` on GitHub Releases. A published release is required for the
download installer; it never builds from source on the user's computer.

To install a particular release:

```sh
curl -fsSL https://warp-restore.ai-infra.org/install.sh | WARP_RESTORE_VERSION=v0.0.1 sh
```

References: [Warp launch configurations](https://docs.warp.dev/terminal/sessions/launch-configurations),
[Warp URI scheme](https://docs.warp.dev/terminal/more-features/uri-scheme), and
[Claude lifecycle hooks](https://code.claude.com/docs/en/hooks).
