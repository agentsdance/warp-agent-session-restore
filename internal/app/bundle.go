package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/agentsdance/warp-agent-session-restore/internal/state"
)

const bundleName = "Warp Restore.app"

const bundleInfo = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>org.ai-infra.warp-agent-session-restore</string>
<key>CFBundleName</key><string>Warp Restore</string>
<key>CFBundleDisplayName</key><string>Warp Restore</string>
<key>CFBundleExecutable</key><string>warp-agent-session-restore</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
<key>CFBundleVersion</key><string>1</string>
<key>LSUIElement</key><true/>
<key>NSAppDataUsageDescription</key><string>Read Warp's window, tab, and split-pane layout to save and restore your agent sessions after a restart.</string>
</dict></plist>
`

// installBundle gives the background process a real bundle identity. macOS 27
// cannot grant per-app container access to a bare command-line process, even
// when that executable has a code-signing identifier.
func (a *App) installBundle(executable []byte) (string, string, error) {
	applications := filepath.Join(a.Home, "Applications")
	bundle := filepath.Join(applications, bundleName)
	if info, err := os.Lstat(bundle); err == nil {
		if !info.IsDir() {
			return "", "", fmt.Errorf("%s already exists and is not an application directory", bundle)
		}
		out, err := exec.Command("/usr/bin/plutil", "-extract", "CFBundleIdentifier", "raw", "-o", "-", filepath.Join(bundle, "Contents", "Info.plist")).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != label {
			return "", "", fmt.Errorf("%s belongs to another application; move it before installing Warp Restore", bundle)
		}
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	if err := os.MkdirAll(applications, 0o755); err != nil {
		return "", "", err
	}
	staging, err := os.MkdirTemp(applications, ".warp-restore-")
	if err != nil {
		return "", "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(staging)
		}
	}()
	staged := filepath.Join(staging, bundleName)
	binRelative := filepath.Join("Contents", "MacOS", "warp-agent-session-restore")
	if err := state.Write(filepath.Join(staged, binRelative), executable, 0o700); err != nil {
		return "", "", err
	}
	if err := state.Write(filepath.Join(staged, "Contents", "Info.plist"), []byte(bundleInfo), 0o600); err != nil {
		return "", "", err
	}
	// No developer account or private key is needed on the user's Mac. The
	// signature seals Info.plist into the helper's application identity.
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--identifier", label, "--timestamp=none", staged).CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("could not sign the background app: %w: %s", err, out)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", staged).CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("background app signature verification failed: %w: %s", err, out)
	}
	previous := filepath.Join(staging, "previous.app")
	hadPrevious := false
	if err := os.Rename(bundle, previous); err == nil {
		hadPrevious = true
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	if err := os.Rename(staged, bundle); err != nil {
		if hadPrevious {
			if rollback := os.Rename(previous, bundle); rollback != nil {
				cleanup = false
				return "", "", fmt.Errorf("install failed: %v; restore failed: %v; previous app preserved at %s", err, rollback, previous)
			}
		}
		return "", "", err
	}
	return filepath.Join(bundle, binRelative), bundle, nil
}
