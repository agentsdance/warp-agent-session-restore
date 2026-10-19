# Warp Agent Session Restore

[Website](https://warp-restore.ai-infra.org) · [Releases](https://github.com/agentsdance/warp-agent-session-restore/releases) · [Installer source](install.sh)

Resume Codex, Claude Code, and Factory Droid conversations after restarting Warp on macOS.
The service snapshots running agents and their Warp pane layout, then opens a
saved launch configuration when it detects a new Warp app process. It resumes
the recorded conversation IDs without replaying prompts.

## Install

Requirements: macOS, Warp Stable, and an installed Codex, Claude Code, or Droid CLI.

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

Setup installs the background app at `~/Applications/Warp Restore.app` and a
command helper in `~/.warp-agent-session-restore`, backs up and updates Claude
Code and Droid hook settings, and registers background recovery to start immediately
and at login. The installer reports each completed step, shows whether automatic
restore is enabled, and prints commands for checking saved sessions and removing
the setup.

After setup, open a new Warp tab and start Codex, Claude Code, or Droid. With automatic
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

In **System Settings → Privacy & Security → Files & Folders → Warp Restore**,
enable **Data shared by Warp and affiliated apps** so the background service can
read Warp's layout database. macOS 27 requires this manual step and does not show
an automatic permission prompt. On earlier macOS versions, allow the prompt if
one appears.

macOS 27 cannot enable this targeted permission for a bare command-line process
without a bundle ID ([Apple release notes](https://developer.apple.com/documentation/macos-release-notes/macos-27-release-notes)).
The installer creates a locally signed app bundle for the background helper;
no developer account or build tools are needed. After updating, macOS may ask
you to enable the permission again. Old entries named
`warp-agent-session-restore` can remain in Settings; use **Warp Restore**.

If access still fails after an update because macOS retained the previous app's
signature, reset only this app's permission with
`tccutil reset SystemPolicyAppDataDetailed org.ai-infra.warp-agent-session-restore`.
Then reopen System Settings and enable **Data shared by Warp and affiliated
apps** for **Warp Restore** again. Resetting the permission does not grant access.

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

If newer conversations are missing after a restart, check the recovery snapshot
in `~/.warp-agent-session-restore/recoveries` for identification warnings. The
watcher can save older local sessions while skipping newer shared-daemon
sessions. Their transcripts remain in Codex's storage; use
`codex --no-daemon resume --all` to find and reopen a missing conversation, or
`codex --no-daemon resume <session-id>` for an exact ID. Terminal transcripts
originally created through the shared daemon are recognized by their
`codex-tui` originator even when their saved source is `vscode`. The tool still
requires each pane's CLI process to hold its own transcript; it does not guess
from the daemon's files.

## Background service

Reinstallation waits up to 15 seconds for launchd to finish removing the old
service before registering its replacement. Earlier versions could report
`Bootstrap failed: 5: Input/output error` when the previous watcher was still
shutting down. If registration fails after setup, retry just the service as
your normal user:

```sh
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/org.ai-infra.warp-agent-session-restore.plist"
```

The installer prints this recovery command on failure. Its files and settings
remain installed, so another full installation is unnecessary for this retry.

The macOS LaunchAgent is named `org.ai-infra.warp-agent-session-restore`.
Its definition is installed at
`~/Library/LaunchAgents/org.ai-infra.warp-agent-session-restore.plist`.
It runs `~/Applications/Warp Restore.app/Contents/MacOS/warp-agent-session-restore watch`.
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
  Droid names come from the exact transcript’s `session_start` header.
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
- Droid uses lifecycle hooks tied to the interactive process and resumes with
  `droid resume <session-id>`. Installation adds Droid to older configurations
  and merges hooks into Factory’s effective user hook file (`hooks.json`, legacy
  `hooks/hooks.json`, or `settings.json`), preserving other hooks and settings.
  Start or resume Droid after installation to record its ID; the next submitted
  prompt also records it. Disabled or organization-restricted hooks cannot track
  Droid sessions. Headless `droid exec`, daemons, and utility commands are excluded.
  `FACTORY_HOME_OVERRIDE` is preserved and selects `<override>/.factory`.
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
they do not copy conversation content. Only `CODEX_HOME`, `CLAUDE_CONFIG_DIR`, and `FACTORY_HOME_OVERRIDE`
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
Tests exercise the restart transition, retry/duplicate behavior, all agents'
resume commands, shell argument boundaries, hook merge/uninstall preservation,
PID reuse, shutdown grace, and reading a live SQLite WAL layout.

To test reinstallation against a real launchd job with a delayed shutdown on a
logged-in Mac, run `WARP_RESTORE_TEST_LAUNCHD=1 go test -v ./internal/app -run
'^TestLaunchAgentReinstallWithDelayedExit$'`. This uses an isolated temporary
service and leaves the installed watcher running.

Live validation has covered saved names against native agent metadata, rename
capture and persistence using isolated sessions, and launching resume commands
in Warp with test agents. On 2026-10-19, a real Warp restart on macOS restored all
11 expected conversations (six Codex and five Claude Code), including a
daemon-created Codex terminal conversation resumed locally. Verification checked
the changed Warp process lifetime, each conversation's owning process and native
metadata, restore claims, absence of duplicate agents, and continued capture
across watcher polls. This validates local CLI restoration; sessions still using
the shared Codex daemon remain subject to the mapping limitation above.

Droid support was checked locally with Droid 0.223.0: a live conversation was
recorded, exited, and resumed through `run-session` in a new pane environment.
The conversation ID, title, directory, and options matched after rediscovery;
a duplicate launch was blocked. This Droid check did not restart the full Warp
application.

### Releases

Push a version tag such as `v0.0.1` to run the release workflow. It runs the
tests, builds macOS binaries for Apple Silicon and Intel, and publishes both
with `SHA256SUMS` on GitHub Releases. A published release is required for the
download installer; it never builds from source on the user's computer.

To install a particular release:

```sh
curl -fsSL https://warp-restore.ai-infra.org/install.sh | WARP_RESTORE_VERSION=v0.0.3 sh
```

References: [Warp launch configurations](https://docs.warp.dev/terminal/sessions/launch-configurations),
[Warp URI scheme](https://docs.warp.dev/terminal/more-features/uri-scheme), and
[Claude lifecycle hooks](https://code.claude.com/docs/en/hooks), and
[Factory Droid lifecycle hooks](https://docs.factory.com/harness/hooks).
