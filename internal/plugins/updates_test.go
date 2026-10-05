package plugins

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn runs git in dir with a fixed identity, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// installURLPlugin installs widget@acme from a local plugin repo through a
// marketplace whose catalog names it by a url source with sourceExtra (for
// example `,"sha":"..."`) appended. It returns the plugin repo and manager.
func installURLPlugin(t *testing.T, sourceExtra string) (pluginRepo string, m *Manager) {
	t.Helper()
	if !gitAvailable() {
		t.Skip("git not available")
	}
	pluginRepo = filepath.Join(t.TempDir(), "pluginrepo")
	writePlugin(t, pluginRepo, "widget", nil)
	makeGitRepo(t, pluginRepo, "extra.txt", "v1")
	if strings.Contains(sourceExtra, "SHA") {
		sourceExtra = strings.ReplaceAll(sourceExtra, "SHA", gitIn(t, pluginRepo, "rev-parse", "HEAD"))
	}
	mktRepo := filepath.Join(t.TempDir(), "mkt")
	if err := os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":{"source":"url","url":"`+pluginRepo+`"`+sourceExtra+`}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, mktRepo, "README.md", "x")
	m = NewManager(t.TempDir())
	m.Stderr = &bytes.Buffer{}
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(context.Background(), "widget", "acme"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	return pluginRepo, m
}

func advanceRepo(t *testing.T, repo string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "extra.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "commit", "-aqm", "v2")
}

// updateAvailable runs a check and reports what List says about plugin.
func updateAvailable(t *testing.T, m *Manager, plugin string) bool {
	t.Helper()
	if err := m.CheckUpdates(context.Background()); err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	return listedUpdateAvailable(t, m, plugin)
}

func listedUpdateAvailable(t *testing.T, m *Manager, plugin string) bool {
	t.Helper()
	items, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, it := range items {
		if it.Plugin == plugin {
			return it.UpdateAvailable
		}
	}
	t.Fatalf("%s not listed", plugin)
	return false
}

func TestCheckUpdates_FlagsAGitPluginWhoseRemoteMovedUntilItIsUpgraded(t *testing.T) {
	pluginRepo, m := installURLPlugin(t, "")
	if listedUpdateAvailable(t, m, "widget") {
		t.Fatal("unchecked plugin listed as having an update")
	}
	if updateAvailable(t, m, "widget") {
		t.Fatal("plugin at its remote head listed as having an update")
	}
	advanceRepo(t, pluginRepo)
	if !updateAvailable(t, m, "widget") {
		t.Fatal("plugin behind its remote head not listed as having an update")
	}
	// The remote moves again after the check, so the upgrade lands past the
	// checked head; the check's answer must not outlive the upgrade.
	gitIn(t, pluginRepo, "commit", "--allow-empty", "-qm", "v3")
	if _, err := m.Upgrade(context.Background(), "widget", "acme"); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if listedUpdateAvailable(t, m, "widget") {
		t.Fatal("upgraded plugin still listed as having an update")
	}
}

func TestCheckUpdates_ShaPinnedSourceAsksNoRemote(t *testing.T) {
	pluginRepo, m := installURLPlugin(t, `,"sha":"SHA"`)
	advanceRepo(t, pluginRepo)
	// A check that reached the remote would now fail and warn.
	if err := os.Rename(pluginRepo, pluginRepo+".gone"); err != nil {
		t.Fatal(err)
	}
	if updateAvailable(t, m, "widget") {
		t.Fatal("sha-pinned plugin listed as having an update")
	}
	if warned := m.Stderr.(*bytes.Buffer).String(); warned != "" {
		t.Fatalf("pinned check reached the remote: %s", warned)
	}
}

func TestCheckUpdates_UnreachableRemoteWarnsAndFlagsNothing(t *testing.T) {
	pluginRepo, m := installURLPlugin(t, "")
	advanceRepo(t, pluginRepo)
	if !updateAvailable(t, m, "widget") {
		t.Fatal("plugin behind its remote head not listed as having an update")
	}
	if err := os.Rename(pluginRepo, pluginRepo+".gone"); err != nil {
		t.Fatal(err)
	}
	if updateAvailable(t, m, "widget") {
		t.Fatal("plugin with an unreachable remote listed as having an update")
	}
	if warned := m.Stderr.(*bytes.Buffer).String(); !strings.Contains(warned, "widget@acme") {
		t.Fatalf("no warning names the plugin: %q", warned)
	}
}

func TestCheckUpdates_RelativeSourcePluginIsNeverFlagged(t *testing.T) {
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(context.Background(), "widget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}
	gitIn(t, mktRepo, "commit", "--allow-empty", "-qm", "moved")
	if updateAvailable(t, m, "widget") {
		t.Fatal("relative-source plugin listed as having an update")
	}
}

func TestGitRemoteHead_ResolvesTheCommitACheckoutOfRefLandsOn(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	first := makeGitRepo(t, repo, "f.txt", "1")
	gitIn(t, repo, "tag", "-a", "v1", "-m", "release")
	gitIn(t, repo, "branch", "feature")
	gitIn(t, repo, "commit", "--allow-empty", "-qm", "later")
	head := gitIn(t, repo, "rev-parse", "HEAD")

	for _, tc := range []struct{ ref, want string }{
		{"", head},
		{"main", head},
		{"feature", first},
		{"v1", first},
	} {
		got, err := gitRemoteHead(context.Background(), repo, tc.ref)
		if err != nil {
			t.Fatalf("gitRemoteHead(%q): %v", tc.ref, err)
		}
		if got != tc.want {
			t.Errorf("gitRemoteHead(%q) = %s, want %s", tc.ref, got, tc.want)
		}
	}
	if _, err := gitRemoteHead(context.Background(), repo, "nope"); err == nil {
		t.Error("gitRemoteHead of a missing ref succeeded")
	}
}
