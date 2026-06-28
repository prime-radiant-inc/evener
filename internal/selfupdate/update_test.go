package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	archive := releaseArchive(t, "serf_linux_amd64")
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	if want := "/releases/download/snapshot/serf_linux_amd64.tar.gz"; gotPath != want {
		t.Fatalf("download path = %q, want %q", gotPath, want)
	}
	if result.Channel != "snapshot" {
		t.Fatalf("Channel = %q, want snapshot", result.Channel)
	}
	if result.Release != "snapshot" {
		t.Fatalf("Release = %q, want snapshot", result.Release)
	}
	const wantRestart = "Restart serf-tui and serf-hub to use the upgraded binaries."
	if result.RestartMessage != wantRestart {
		t.Fatalf("RestartMessage = %q, want %q", result.RestartMessage, wantRestart)
	}

	binDir := filepath.Join(prefix, "bin")
	shareBinDir := filepath.Join(prefix, "share", "serf", "bin")
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
	archive := releaseArchive(t, "serf_linux_amd64")
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	if want := "/releases/latest/download/serf_linux_amd64.tar.gz"; gotPath != want {
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
		RepoURL:        "https://example.invalid/serf",
	})
	if err == nil {
		t.Fatal("Upgrade succeeded on unsupported platform")
	}
	if !strings.Contains(err.Error(), "unsupported platform darwin-amd64") {
		t.Fatalf("error = %q, want unsupported platform", err.Error())
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
