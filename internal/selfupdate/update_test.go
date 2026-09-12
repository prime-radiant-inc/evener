package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTargetTracksCurrentChannel(t *testing.T) {
	tests := []struct {
		name           string
		requested      string
		currentChannel string
		wantRelease    string
		wantChannel    string
	}{
		{
			name:           "snapshot build tracks snapshot",
			currentChannel: "snapshot",
			wantRelease:    "snapshot",
			wantChannel:    "snapshot",
		},
		{
			name:           "release build tracks latest release",
			currentChannel: "release",
			wantRelease:    "latest",
			wantChannel:    "release",
		},
		{
			name:           "dev build defaults to latest release",
			currentChannel: "dev",
			wantRelease:    "latest",
			wantChannel:    "release",
		},
		{
			name:           "explicit snapshot overrides release",
			requested:      "snapshot",
			currentChannel: "release",
			wantRelease:    "snapshot",
			wantChannel:    "snapshot",
		},
		{
			name:           "explicit release overrides snapshot",
			requested:      "release",
			currentChannel: "snapshot",
			wantRelease:    "latest",
			wantChannel:    "release",
		},
		{
			name:           "explicit version uses that release",
			requested:      "v1.2.3",
			currentChannel: "snapshot",
			wantRelease:    "v1.2.3",
			wantChannel:    "release",
		},
		{
			name:           "explicit current behaves like empty",
			requested:      "current",
			currentChannel: "release",
			wantRelease:    "latest",
			wantChannel:    "release",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveTarget(tc.requested, tc.currentChannel)
			if err != nil {
				t.Fatalf("ResolveTarget: %v", err)
			}
			if got.Release != tc.wantRelease {
				t.Fatalf("Release = %q, want %q", got.Release, tc.wantRelease)
			}
			if got.Channel != tc.wantChannel {
				t.Fatalf("Channel = %q, want %q", got.Channel, tc.wantChannel)
			}
		})
	}
}

func TestUpgradeInstallsReleaseArchive(t *testing.T) {
	archive := releaseArchive(t, "evener_linux_amd64")
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			sum := sha256.Sum256(archive)
			_, _ = fmt.Fprintf(w, "%x  evener_linux_amd64.tar.gz\n", sum)
			return
		}
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)

	prefix := filepath.Join(t.TempDir(), ".local")
	result, err := Upgrade(t.Context(), Options{
		Requested:      "",
		CurrentChannel: "snapshot",
		Prefix:         prefix,
		GOOS:           "linux",
		GOARCH:         "amd64",
		RepoURL:        server.URL,
	})
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if want := "/releases/download/snapshot/evener_linux_amd64.tar.gz"; gotPath != want {
		t.Fatalf("download path = %q, want %q", gotPath, want)
	}
	if result.Channel != "snapshot" {
		t.Fatalf("Channel = %q, want snapshot", result.Channel)
	}
	if result.Release != "snapshot" {
		t.Fatalf("Release = %q, want snapshot", result.Release)
	}
	const wantRestart = "Restart evener to use the upgraded binary."
	if result.RestartMessage != wantRestart {
		t.Fatalf("RestartMessage = %q, want %q", result.RestartMessage, wantRestart)
	}

	binDir := filepath.Join(prefix, "bin")
	shareBinDir := filepath.Join(prefix, "share", "evener", "bin")
	for _, bin := range installBinaries {
		installed := filepath.Join(shareBinDir, bin)
		info, err := os.Stat(installed)
		if err != nil {
			t.Fatalf("installed binary %s: %v", installed, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("installed binary %s is not executable: mode %s", installed, info.Mode())
		}
		data, err := os.ReadFile(installed)
		if err != nil {
			t.Fatalf("read installed binary %s: %v", installed, err)
		}
		if !strings.Contains(string(data), "archive "+bin) {
			t.Fatalf("installed binary %s has unexpected content %q", bin, string(data))
		}

		link := filepath.Join(binDir, bin)
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("readlink %s: %v", link, err)
		}
		if target != installed {
			t.Fatalf("symlink %s -> %s, want %s", link, target, installed)
		}
	}
}

func TestUpgradeReleaseChannelUsesLatestDownloadURL(t *testing.T) {
	archive := releaseArchive(t, "evener_linux_amd64")
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			sum := sha256.Sum256(archive)
			_, _ = fmt.Fprintf(w, "%x  evener_linux_amd64.tar.gz\n", sum)
			return
		}
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)

	prefix := filepath.Join(t.TempDir(), ".local")
	result, err := Upgrade(t.Context(), Options{
		Requested:      "",
		CurrentChannel: "release",
		Prefix:         prefix,
		GOOS:           "linux",
		GOARCH:         "amd64",
		RepoURL:        server.URL,
	})
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}

	if want := "/releases/latest/download/evener_linux_amd64.tar.gz"; gotPath != want {
		t.Fatalf("download path = %q, want %q", gotPath, want)
	}
	if result.Channel != "release" {
		t.Fatalf("Channel = %q, want release", result.Channel)
	}
	if result.Release != "latest" {
		t.Fatalf("Release = %q, want latest", result.Release)
	}
}

func TestUpgradeRejectsUnsupportedPlatform(t *testing.T) {
	_, err := Upgrade(t.Context(), Options{
		Requested:      "snapshot",
		CurrentChannel: "snapshot",
		Prefix:         t.TempDir(),
		GOOS:           "darwin",
		GOARCH:         "amd64",
		RepoURL:        "https://example.invalid/evener",
	})
	if err == nil {
		t.Fatal("Upgrade succeeded on unsupported platform")
	}
	if !strings.Contains(err.Error(), "unsupported platform darwin-amd64") {
		t.Fatalf("error = %q, want unsupported platform", err.Error())
	}
}

func TestStageExecutableLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("binary body"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	tmp, err := stageExecutable(t.Context(), src, dir, "evener")
	if err != nil {
		t.Fatalf("stageExecutable: %v", err)
	}

	body, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read staged: %v", err)
	}
	if string(body) != "binary body" {
		t.Fatalf("staged = %q, want %q", string(body), "binary body")
	}
	info, err := os.Stat(tmp)
	if err != nil {
		t.Fatalf("stat staged: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("staged mode = %v, want 0755", info.Mode().Perm())
	}
	_ = os.Remove(tmp)
	leftovers, err := filepath.Glob(filepath.Join(dir, "*.stage"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

// Concurrent stages (a hub self-update and an `evener upgrade` in another
// process) must not share one temp path and interleave writes: each stages
// a complete file of its own under a unique name.
func TestStageExecutableConcurrentStagesNeverMix(t *testing.T) {
	dir := t.TempDir()
	first := strings.Repeat("A", 1<<20)
	second := strings.Repeat("B", 1<<20)
	srcA := filepath.Join(dir, "srcA")
	srcB := filepath.Join(dir, "srcB")
	if err := os.WriteFile(srcA, []byte(first), 0o644); err != nil {
		t.Fatalf("write srcA: %v", err)
	}
	if err := os.WriteFile(srcB, []byte(second), 0o644); err != nil {
		t.Fatalf("write srcB: %v", err)
	}
	tmps := make([]string, 2)
	for j, src := range []string{srcA, srcB} {
		var err error
		tmps[j], err = stageExecutable(t.Context(), src, dir, "evener")
		if err != nil {
			t.Fatalf("stageExecutable: %v", err)
		}
	}
	if tmps[0] == tmps[1] {
		t.Fatalf("both stages share temp path %q", tmps[0])
	}
	for j, want := range []string{first, second} {
		body, err := os.ReadFile(tmps[j])
		if err != nil {
			t.Fatalf("read staged: %v", err)
		}
		if string(body) != want {
			t.Fatalf("staged %d has unexpected content (len %d)", j, len(body))
		}
		_ = os.Remove(tmps[j])
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, "*.stage"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

func releaseArchive(t *testing.T, root string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, bin := range installBinaries {
		body := fmt.Sprintf("#!/bin/sh\necho archive %s\n", bin)
		header := &tar.Header{
			Name: filepath.ToSlash(filepath.Join(root, bin)),
			Mode: 0o755,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}
