package main

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

func TestUpgradeSubcommandInstallsSnapshot(t *testing.T) {
	archive := testReleaseArchive(t, "serf_linux_amd64")
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)

	prefix := filepath.Join(t.TempDir(), ".local")
	var stdout, stderr bytes.Buffer
	handled, label, err := dispatchCLICommand([]string{
		"upgrade",
		"--repo-url", server.URL,
		"--prefix", prefix,
		"--goos", "linux",
		"--goarch", "amd64",
		"snapshot",
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("dispatchCLICommand: %v\nstderr:\n%s", err, stderr.String())
	}
	if !handled {
		t.Fatal("dispatchCLICommand handled=false, want true")
	}
	if label != "serf upgrade" {
		t.Fatalf("label=%q, want serf upgrade", label)
	}
	if want := "/releases/download/snapshot/serf_linux_amd64.tar.gz"; gotPath != want {
		t.Fatalf("download path = %q, want %q", gotPath, want)
	}

	out := stdout.String()
	for _, want := range []string{"upgraded serf to snapshot", "Restart serf-tui and serf-hub"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}

	installed := filepath.Join(prefix, "share", "serf", "bin", "serf")
	data, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("read installed serf binary: %v", err)
	}
	if !strings.Contains(string(data), "archive serf") {
		t.Fatalf("installed serf content = %q, want archive serf", string(data))
	}

	link := filepath.Join(prefix, "bin", "serf")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink %s: %v", link, err)
	}
	if target != installed {
		t.Fatalf("symlink target = %q, want %q", target, installed)
	}
}

func testReleaseArchive(t *testing.T, root string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, bin := range []string{"serf", "serf-hub", "serf-tui", "serf-doctor"} {
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

func TestRunUpgrade_TooManyArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runUpgrade([]string{"snapshot", "extra"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runUpgrade(too many args) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "at most one upgrade target") {
		t.Fatalf("error = %v, want 'at most one upgrade target'", err)
	}
}

func TestRunUpgrade_InvalidFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runUpgrade([]string{"--invalid-flag"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runUpgrade(invalid flag) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "not defined") || !strings.Contains(err.Error(), "-invalid-flag") {
		t.Fatalf("error = %v, want flag-parse error mentioning 'not defined' and '-invalid-flag'", err)
	}
	if got := stderr.String(); !strings.Contains(got, "Usage: serf upgrade") {
		t.Fatalf("stderr = %q, want usage banner 'Usage: serf upgrade'", got)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
}
