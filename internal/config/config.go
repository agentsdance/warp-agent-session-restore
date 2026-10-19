// Package config holds the tool's settings and the per-agent definitions that
// describe how to find a session ID and how to rebuild a resume command.
package config

import (
	"encoding/json"
	"fmt"
	"github.com/agentsdance/warp-agent-session-restore/internal/state"
	"os"
	"path/filepath"
)

// ValueKind says whether a flag consumes the argument that follows it.
type ValueKind string

const (
	// ValueNone is a boolean flag.
	ValueNone ValueKind = "none"
	// ValueRequired always consumes the next argument.
	ValueRequired ValueKind = "required"
	// ValueOptional consumes the next argument only if it is not itself a flag.
	ValueOptional ValueKind = "optional"
)

// DropArg names a flag that must not be carried into the resume command,
// because the resume command supplies its own version of it.
type DropArg struct {
	Flag  string    `json:"flag"`
	Value ValueKind `json:"value"`
}

// SessionSource is how we learn the session ID of a running agent.
type SessionSource string

const (
	// SourceRegistry reads the session ID recorded by the agent's SessionStart hook.
	SourceRegistry SessionSource = "registry"
	// SourceOpenFile derives the session ID from a session file the process holds open.
	SourceOpenFile SessionSource = "open_file"
)

// Agent describes one CLI agent we can snapshot and resume.
type Agent struct {
	Name string `json:"name"`
	// Match are process basenames that identify this agent. A wrapper such as
	// "node /opt/homebrew/bin/codex" matches on its second argument.
	Match []string `json:"match"`

	SessionSource SessionSource `json:"session_source"`
	// OpenFileRegex extracts the session ID from an open file path. Group 1 is the ID.
	// Only used when SessionSource is open_file.
	OpenFileRegex string `json:"open_file_regex,omitempty"`
	// TranscriptGlob locates session transcripts for the cwd fallback, with %s
	// replaced by the encoded project directory. Only used by the registry source.
	TranscriptGlob string `json:"transcript_glob,omitempty"`

	// ResumeTemplate builds the resume command from Bin, SessionID and Args.
	ResumeTemplate string `json:"resume_template"`

	// ValueFlags consume the next argument and are kept as-is.
	ValueFlags []string `json:"value_flags,omitempty"`
	// DropArgs are removed from the original command line.
	DropArgs []DropArg `json:"drop_args,omitempty"`
	// DropPositionals removes bare arguments, which for these CLIs are prompts
	// or subcommands rather than options worth preserving.
	DropPositionals bool `json:"drop_positionals"`
}

// Config is the whole tool configuration.
type Config struct {
	// StateDir holds the snapshot, the registry and the consumed-pane marker.
	StateDir string `json:"state_dir"`
	// WarpDB is Warp's sqlite database.
	WarpDB string `json:"warp_db"`
	// LaunchConfigDir is where generated Warp launch configurations are written.
	LaunchConfigDir string `json:"launch_config_dir"`
	// AutoResume runs the resume command directly instead of pre-filling the prompt.
	AutoResume bool `json:"auto_resume"`
	// AutoResumeLimit caps how many panes auto-resume, newest first. Zero means no cap.
	AutoResumeLimit int `json:"auto_resume_limit"`
	// KeepSnapshots is how many historical snapshots to retain.
	KeepSnapshots    int  `json:"keep_snapshots"`
	PollSeconds      int  `json:"poll_seconds"`
	ExitGraceSeconds int  `json:"exit_grace_seconds"`
	LocalCodex       bool `json:"local_codex"`

	Agents []Agent `json:"agents"`
}

// DefaultWarpDB is where Warp keeps its sqlite database on macOS.
func DefaultWarpDB(home string) string {
	return filepath.Join(home,
		"Library", "Group Containers", "2BBY89MBSN.dev.warp",
		"Library", "Application Support", "dev.warp.Warp-Stable", "warp.sqlite")
}

// Default returns the built-in configuration.
func Default(home string) *Config {
	return &Config{
		StateDir:         filepath.Join(home, ".warp-agent-session-restore"),
		WarpDB:           DefaultWarpDB(home),
		LaunchConfigDir:  filepath.Join(home, ".warp", "launch_configurations"),
		AutoResume:       true,
		AutoResumeLimit:  0,
		KeepSnapshots:    20,
		PollSeconds:      5,
		ExitGraceSeconds: 20,
		Agents: []Agent{
			{
				Name:           "claude",
				Match:          []string{"claude"},
				SessionSource:  SourceRegistry,
				TranscriptGlob: filepath.Join(home, ".claude", "projects", "%s", "*.jsonl"),
				ResumeTemplate: "{{.Bin}} --resume {{.SessionID}}{{if .Args}} {{.Args}}{{end}}",
				ValueFlags: []string{
					"--model", "-m", "--fallback-model", "--effort",
					"--append-system-prompt", "--system-prompt",
					"--permission-mode", "--permission-prompt-tool",
					"--settings", "--setting-sources", "--add-dir",
					"--allowed-tools", "--allowedTools",
					"--disallowed-tools", "--disallowedTools",
					"--mcp-config", "--agents", "--plugin-dir", "--agent", "--name",
					"--output-format", "--input-format",
				},
				DropArgs: []DropArg{
					{Flag: "--resume", Value: ValueOptional},
					{Flag: "-r", Value: ValueOptional},
					{Flag: "--continue", Value: ValueNone},
					{Flag: "-c", Value: ValueNone},
					{Flag: "--fork-session", Value: ValueNone},
					{Flag: "--session-id", Value: ValueRequired},
					{Flag: "-p", Value: ValueNone},
					{Flag: "--print", Value: ValueNone},
					{Flag: "--worktree", Value: ValueOptional},
					{Flag: "-w", Value: ValueOptional},
					{Flag: "--tmux", Value: ValueNone},
				},
				DropPositionals: true,
			},
			{
				Name:          "codex",
				Match:         []string{"codex"},
				SessionSource: SourceOpenFile,
				// ~/.codex/sessions/2026/08/13/rollout-2026-08-13T16-15-59-<uuid>.jsonl
				OpenFileRegex:  `/\.codex/sessions/.*/rollout-[^/]*-([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$`,
				ResumeTemplate: "{{.Bin}} resume {{.SessionID}}{{if .Args}} {{.Args}}{{end}}",
				ValueFlags: []string{
					"-c", "--config", "-m", "--model", "-p", "--profile",
					"-s", "--sandbox", "-a", "--ask-for-approval",
					"--enable", "--disable", "--add-dir", "--local-provider",
				},
				DropArgs: []DropArg{
					{Flag: "--last", Value: ValueNone},
					{Flag: "--resume", Value: ValueOptional},
					{Flag: "--all", Value: ValueNone},
					{Flag: "--worktree", Value: ValueNone},
					{Flag: "-C", Value: ValueRequired},
					{Flag: "--cd", Value: ValueRequired},
					{Flag: "-i", Value: ValueRequired},
					{Flag: "--image", Value: ValueRequired},
				},
				DropPositionals: true,
			},
		},
	}
}

// Path is the configuration file location.
func Path(home string) string {
	return filepath.Join(home, ".warp-agent-session-restore", "config.json")
}

// Load reads the configuration, writing the default file if none exists.
func Load(home string) (*Config, error) {
	def := Default(home)
	p := Path(home)

	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		if err := Save(home, def); err != nil {
			return nil, err
		}
		return def, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", p, err)
	}

	// Start from the default so a partial file still gets sane agent definitions.
	cfg := def
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", p, err)
	}
	if len(cfg.Agents) == 0 {
		cfg.Agents = Default(home).Agents
	}
	if cfg.PollSeconds < 1 || cfg.ExitGraceSeconds < 0 || cfg.KeepSnapshots < 1 || cfg.AutoResumeLimit < 0 {
		return nil, fmt.Errorf("invalid polling, retention, grace, or resume limit")
	}
	return cfg, nil
}

// Save writes the configuration file.
func Save(home string, cfg *Config) error {
	p := Path(home)
	return state.Save(p, cfg)
}

// AgentByName looks up an agent definition.
func (c *Config) AgentByName(name string) *Agent {
	for i := range c.Agents {
		if c.Agents[i].Name == name {
			return &c.Agents[i]
		}
	}
	return nil
}

// SnapshotPath is the current snapshot file.
func (c *Config) SnapshotPath() string { return filepath.Join(c.StateDir, "snapshot.json") }

// RegistryPath is the session registry written by agent hooks.
func (c *Config) RegistryPath() string { return filepath.Join(c.StateDir, "registry.jsonl") }

// ConsumedPath records panes that have already been resumed once.
func (c *Config) ConsumedPath() string { return filepath.Join(c.StateDir, "consumed.json") }

// HistoryDir holds rotated snapshots.
func (c *Config) HistoryDir() string { return filepath.Join(c.StateDir, "snapshots") }
