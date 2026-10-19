package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadInstaller(t *testing.T) {
	for _, dependency := range []string{"sh", "shasum", "awk", "mktemp"} {
		if _, err := exec.LookPath(dependency); err != nil {
			t.Skipf("installer test requires %s", dependency)
		}
	}
	tests := []struct {
		name, os, arch, version, checksum, wantError string
		missingRelease, failedDownload               bool
		customHome, existingLink                     bool
		existingAlias                                string
		repeatInstall                                bool
		installExit                                  string
		wantInstalled                                bool
	}{
		{name: "Apple Silicon latest", arch: "arm64", wantInstalled: true},
		{name: "Intel pinned version", arch: "x86_64", version: "v1.2.3", wantInstalled: true},
		{name: "custom settings home", customHome: true, wantInstalled: true},
		{name: "reinstall keeps both names", repeatInstall: true, wantInstalled: true},
		{name: "existing alias", existingAlias: "owned", wantInstalled: true},
		{name: "existing absolute alias", existingAlias: "absolute", wantInstalled: true},
		{name: "conflicting alias file", existingAlias: "file", wantError: "already exists"},
		{name: "conflicting alias link", existingAlias: "other", wantError: "already points to another command"},
		{name: "existing package link", existingLink: true, wantError: "is a symbolic link"},
		{name: "unsupported OS", os: "Linux", wantError: "supports macOS only"},
		{name: "unsupported architecture", arch: "other", wantError: "unsupported processor"},
		{name: "invalid version", version: "v1/../../other", wantError: "Invalid release version"},
		{name: "no published release", missingRelease: true, wantError: "Could not find a downloadable release"},
		{name: "download failure", failedDownload: true, wantError: "Could not download"},
		{name: "checksum mismatch", checksum: "wrong", wantError: "checksum did not match"},
		{name: "checksum missing", checksum: "missing", wantError: "missing a checksum"},
		{name: "setup failure", installExit: "7", wantInstalled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			mockBin := filepath.Join(root, "mock commands")
			downloads := filepath.Join(root, "download cache")
			for _, dir := range []string{mockBin, downloads} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, text string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(text), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			binary := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$INSTALL_TRACE\"\nexit \"${MOCK_INSTALL_EXIT:-0}\"\n"
			fixtureBinary := filepath.Join(root, "release binary")
			write(fixtureBinary, binary)
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(binary)))
			if tt.checksum == "wrong" {
				digest = strings.Repeat("0", 64)
			}
			checksums := fmt.Sprintf("%s  warp-agent-session-restore-darwin-arm64\n%s  warp-agent-session-restore-darwin-amd64\n", digest, digest)
			if tt.checksum == "missing" {
				checksums = digest + "  another-app\n"
			}
			fixtureChecksums := filepath.Join(root, "release checksums")
			write(fixtureChecksums, checksums)
			write(filepath.Join(mockBin, "uname"), `#!/bin/sh
case "$1" in
    -s) printf '%s\n' "$MOCK_OS" ;;
    -m) printf '%s\n' "$MOCK_ARCH" ;;
    *) exit 1 ;;
esac
`)
			write(filepath.Join(mockBin, "curl"), `#!/bin/sh
set -eu
output=
url=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --output) output=$2; shift 2 ;;
        --write-out|--proto|--connect-timeout|--retry) shift 2 ;;
        -*) shift ;;
        *) url=$1; shift ;;
    esac
done
printf '%s\n' "$url" >> "$CURL_TRACE"
case "$url" in
    */releases/latest)
        [ "$MOCK_MISSING_RELEASE" = false ] || exit 22
        printf '%s' 'https://github.com/agentsdance/warp-agent-session-restore/releases/tag/v1.2.3'
        ;;
    */SHA256SUMS) cp "$FIXTURE_CHECKSUMS" "$output" ;;
    */warp-agent-session-restore-darwin-*)
        [ "$MOCK_FAILED_DOWNLOAD" = false ] || exit 22
        cp "$FIXTURE_BINARY" "$output"
        ;;
    *) exit 22 ;;
esac
`)
			osName, arch := tt.os, tt.arch
			if osName == "" {
				osName = "Darwin"
			}
			if arch == "" {
				arch = "arm64"
			}
			trace := filepath.Join(root, "setup invoked")
			curlTrace := filepath.Join(root, "downloads requested")
			binDir := filepath.Join(root, "installed commands")
			installedCommand := filepath.Join(binDir, "warp-agent-session-restore")
			aliasPath := filepath.Join(binDir, "warp-restore")
			aliasTarget := "warp-agent-session-restore"
			if tt.existingLink {
				if err := os.Mkdir(binDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(fixtureBinary, installedCommand); err != nil {
					t.Fatal(err)
				}
			}
			if tt.existingAlias != "" {
				if err := os.Mkdir(binDir, 0o755); err != nil {
					t.Fatal(err)
				}
				switch tt.existingAlias {
				case "file":
					write(aliasPath, "unrelated command\n")
				case "other":
					aliasTarget = fixtureBinary
				case "absolute":
					aliasTarget = installedCommand
				}
				if tt.existingAlias != "file" {
					if err := os.Symlink(aliasTarget, aliasPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			cmd := exec.Command("sh", "install.sh", "--bin-dir", binDir, "--no-service")
			wantArguments := "install\n--no-service\n"
			if tt.customHome {
				settingsHome := filepath.Join(root, "settings home")
				cmd.Args = append(cmd.Args, "--home", settingsHome)
				wantArguments = "--home\n" + settingsHome + "\n" + wantArguments
			}
			cmd.Env = append(os.Environ(),
				"PATH="+mockBin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TMPDIR="+downloads,
				"MOCK_OS="+osName, "MOCK_ARCH="+arch,
				"WARP_RESTORE_VERSION="+tt.version,
				"MOCK_MISSING_RELEASE="+fmt.Sprint(tt.missingRelease),
				"MOCK_FAILED_DOWNLOAD="+fmt.Sprint(tt.failedDownload),
				"MOCK_INSTALL_EXIT="+tt.installExit,
				"FIXTURE_BINARY="+fixtureBinary, "FIXTURE_CHECKSUMS="+fixtureChecksums,
				"INSTALL_TRACE="+trace, "CURL_TRACE="+curlTrace)
			out, err := cmd.CombinedOutput()
			if tt.repeatInstall && err == nil {
				again := exec.Command(cmd.Path, cmd.Args[1:]...)
				again.Env = cmd.Env
				out, err = again.CombinedOutput()
			}
			wantFailure := tt.wantError != "" || tt.installExit != ""
			if (err != nil) != wantFailure {
				t.Fatalf("err=%v, want failure=%v; output:\n%s", err, wantFailure, out)
			}
			if tt.wantError != "" && !strings.Contains(string(out), tt.wantError) {
				t.Fatalf("missing guidance %q:\n%s", tt.wantError, out)
			}
			invoked, err := os.ReadFile(trace)
			if tt.wantInstalled {
				if err != nil || string(invoked) != wantArguments {
					t.Fatalf("setup arguments=%q, err=%v", invoked, err)
				}
				info, err := os.Stat(installedCommand)
				if err != nil || info.Mode().Perm() != 0o755 {
					t.Fatalf("command permissions: info=%v err=%v", info, err)
				}
				info, err = os.Stat(binDir)
				if err != nil || info.Mode().Perm() != 0o755 {
					t.Fatalf("command directory permissions: info=%v err=%v", info, err)
				}
				if target, err := os.Readlink(aliasPath); err != nil || target != aliasTarget {
					t.Fatalf("alias target=%q, err=%v", target, err)
				}
				if tt.installExit == "" {
					for _, executable := range []string{installedCommand, aliasPath} {
						invoke := exec.Command(executable, "status", "--example-argument", "value with spaces")
						invoke.Env = cmd.Env
						if out, err := invoke.CombinedOutput(); err != nil {
							t.Fatalf("%s failed: %v\n%s", executable, err, out)
						}
						args, err := os.ReadFile(trace)
						if err != nil || string(args) != "status\n--example-argument\nvalue with spaces\n" {
							t.Fatalf("%s arguments=%q, err=%v", executable, args, err)
						}
					}
				}
				requested, err := os.ReadFile(curlTrace)
				if err != nil {
					t.Fatal(err)
				}
				assetArch := "arm64"
				if arch == "x86_64" {
					assetArch = "amd64"
				}
				if !strings.Contains(string(requested), "/download/v1.2.3/warp-agent-session-restore-darwin-"+assetArch) {
					t.Fatalf("wrong release or architecture requested:\n%s", requested)
				}
				if tt.version != "" && strings.Contains(string(requested), "/releases/latest") {
					t.Fatal("pinned version unexpectedly requested the latest release")
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("setup ran before the download was validated: %q (err=%v)", invoked, err)
			}
			if tt.existingLink {
				if target, err := os.Readlink(installedCommand); err != nil || target != fixtureBinary {
					t.Fatalf("existing package link was changed: target=%q err=%v", target, err)
				}
			} else if !tt.wantInstalled {
				if _, err := os.Stat(installedCommand); !os.IsNotExist(err) {
					t.Fatalf("unverified command installed: %v", err)
				}
			}
			switch tt.existingAlias {
			case "file":
				if data, err := os.ReadFile(aliasPath); err != nil || string(data) != "unrelated command\n" {
					t.Fatalf("unrelated alias command was changed: %q (err=%v)", data, err)
				}
			case "other":
				if target, err := os.Readlink(aliasPath); err != nil || target != fixtureBinary {
					t.Fatalf("unrelated alias link was changed: %q (err=%v)", target, err)
				}
			default:
				if !tt.wantInstalled {
					if _, err := os.Lstat(aliasPath); !os.IsNotExist(err) {
						t.Fatalf("alias installed before validation: %v", err)
					}
				}
			}
			remaining, err := os.ReadDir(downloads)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("temporary downloads were not cleaned up: %v (err=%v)", remaining, err)
			}
		})
	}
}
