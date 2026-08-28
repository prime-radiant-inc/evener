package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubGitHeadFailSoft(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	calls := 0
	cfg := hubcore.WebConfig{ResolveGitHead: func(context.Context, string) (string, error) {
		calls++
		return "", errors.New("git unavailable")
	}}

	for _, tc := range []struct {
		name string
		cwd  string
	}{
		{name: "empty cwd", cwd: "  "},
		{name: "missing cwd", cwd: missing},
		{name: "git error", cwd: root},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hubGitHead(context.Background(), cfg, appwire.GitHeadParams{CWD: tc.cwd})
			if got.Head != "" {
				t.Fatalf("head=%q, want empty", got.Head)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("git seam calls=%d, want 1 (only the existing directory)", calls)
	}
}

func TestHubGitHeadNonGitDirectoryReturnsEmpty(t *testing.T) {
	got := hubGitHead(context.Background(), hubcore.WebConfig{}, appwire.GitHeadParams{CWD: t.TempDir()})
	if got.Head != "" {
		t.Fatalf("head=%q, want empty for a non-git directory", got.Head)
	}
}

func TestHubRPCGitHeadUsesCanonicalDirectory(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	var gotDir string
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past: hubcore.NewPastIndex(""),
		ResolveGitHead: func(_ context.Context, dir string) (string, error) {
			gotDir = dir
			return "main", nil
		},
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.GitHead(context.Background(), appwire.GitHeadParams{CWD: link})
	if err != nil {
		t.Fatalf("GitHead: %v", err)
	}
	if resp.Head != "main" {
		t.Fatalf("head=%q, want main", resp.Head)
	}
	if gotDir != canonicalRoot {
		t.Fatalf("git seam directory=%q, want %q", gotDir, canonicalRoot)
	}
}
