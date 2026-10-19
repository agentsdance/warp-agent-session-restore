# Warp Agent Session Restore

Resume Codex and Claude Code conversations after restarting Warp on macOS.
The service snapshots running agents and their Warp pane layout, then opens a
saved launch configuration when it detects a new Warp app process. It resumes
the recorded conversation IDs without replaying prompts.

## Install

Requirements: macOS, Warp Stable, Go 1.25+, and installed Codex / Claude Code CLIs.

```sh
git clone https://github.com/agentsdance/warp-agent-session-restore.git
cd warp-agent-session-restore
make install
~/.warp-agent-session-restore/bin/warp-agent-session-restore status
```

Installation creates a private binary and state directory at
`~/.warp-agent-session-restore`, adds Claude session hooks while preserving existing
settings, and starts a user LaunchAgent. Existing settings are backed up first.
It does not restart Warp or close any running agents.

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
ras="$HOME/.warp-agent-session-restore/bin/warp-agent-session-restore"
"$ras" snapshot             # capture immediately
"$ras" status               # saved names, counts and identification warnings
"$ras" restore --dry-run    # inspect generated Warp YAML
"$ras" restore              # open saved sessions
"$ras" watch                # foreground service; only one watcher is allowed
"$ras" uninstall            # stop service, remove managed hooks/shell block
```

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

```sh
make test
make build
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

References: [Warp launch configurations](https://docs.warp.dev/terminal/sessions/launch-configurations),
[Warp URI scheme](https://docs.warp.dev/terminal/more-features/uri-scheme), and
[Claude lifecycle hooks](https://code.claude.com/docs/en/hooks).
