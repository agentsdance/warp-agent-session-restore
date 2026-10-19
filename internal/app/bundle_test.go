package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBundleHasVerifiedIdentityAndCanBeReinstalled(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires macOS code signing")
	}
	a, _ := fixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	var firstHash string
	for range 2 {
		bin, bundle, err := a.installBundle(data)
		if err != nil {
			t.Fatal(err)
		}
		if bin != filepath.Join(a.Home, "Applications", bundleName, "Contents", "MacOS", "warp-agent-session-restore") {
			t.Fatal(bin)
		}
		if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", bundle).CombinedOutput(); err != nil {
			t.Fatalf("signature: %v %s", err, out)
		}
		signature, err := exec.Command("/usr/bin/codesign", "-d", "--verbose=2", bundle).CombinedOutput()
		if err != nil {
			t.Fatalf("read signature: %v %s", err, signature)
		}
		var hash string
		for line := range strings.SplitSeq(string(signature), "\n") {
			if strings.HasPrefix(line, "CDHash=") {
				hash = line
			}
		}
		if hash == "" || (firstHash != "" && hash != firstHash) {
			t.Fatal("reinstall changed code identity for the same release")
		}
		firstHash = hash
		out, err := exec.Command("/usr/bin/plutil", "-extract", "CFBundleIdentifier", "raw", "-o", "-", filepath.Join(bundle, "Contents", "Info.plist")).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != label {
			t.Fatalf("bundle identity: %v %s", err, out)
		}
		plist := a.plist(bin)
		if !strings.Contains(plist, bin) {
			t.Fatal("service does not use the bundled executable")
		}
	}
}

func TestBundleInstallPreservesAnUnrelatedApp(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires macOS property list tools")
	}
	a, _ := fixture(t)
	bundle := filepath.Join(a.Home, "Applications", bundleName)
	if err := os.MkdirAll(filepath.Join(bundle, "Contents"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bundle, "Contents", "Info.plist")
	data := []byte(strings.Replace(bundleInfo, label, "org.example.unrelated", 1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.installBundle([]byte("not executable")); err == nil {
		t.Fatal("overwrote an unrelated app")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatal("changed unrelated application")
	}
}
