package plugins

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// urlPluginFixture is widget@acme installed from a local plugin repo through a
// local marketplace repo whose catalog names it by a url source.
type urlPluginFixture struct {
	pluginRepo, mktRepo string
	m                   *Manager
}

// installURLPlugin builds the fixture. sourceExtra returns extra JSON members
// for the catalog's url source (for example `,"sha":"..."`), given the plugin
// repo and its head commit.
func installURLPlugin(t *testing.T, sourceExtra func(repo, head string) string) urlPluginFixture {
	t.Helper()
	if !gitAvailable() {
		t.Skip("git not available")
	}
	f := urlPluginFixture{
		pluginRepo: filepath.Join(t.TempDir(), "pluginrepo"),
		mktRepo:    filepath.Join(t.TempDir(), "mkt"),
		m:          NewManager(t.TempDir()),
	}
	writePlugin(t, f.pluginRepo, "widget", nil)
	head := makeGitRepo(t, f.pluginRepo, "extra.txt", "v1")
	writeURLCatalog(t, f.mktRepo, f.pluginRepo, sourceExtra(f.pluginRepo, head))
	makeGitRepo(t, f.mktRepo, "README.md", "x")
	f.m.Stderr = &bytes.Buffer{}
	if _, err := f.m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: f.mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := f.m.Install(context.Background(), "widget", "acme"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	return f
}

func unpinned(string, string) string { return "" }

func writeURLCatalog(t *testing.T, mktRepo, pluginRepo, sourceExtra string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":{"source":"url","url":"`+pluginRepo+`"`+sourceExtra+`}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// advanceRepo adds an empty commit to repo and returns the new head.
func advanceRepo(t *testing.T, repo string) string {
	t.Helper()
	gitIn(t, repo, "commit", "--allow-empty", "-qm", "advance")
	return gitIn(t, repo, "rev-parse", "HEAD")
}

func (f urlPluginFixture) warnings() string { return f.m.Stderr.(*bytes.Buffer).String() }

// checkThenList runs a check and reports what List says about widget.
func checkThenList(t *testing.T, m *Manager) bool {
	t.Helper()
	if err := m.CheckUpdates(context.Background()); err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	return listedUpdateAvailable(t, m)
}

func listedUpdateAvailable(t *testing.T, m *Manager) bool {
	t.Helper()
	items, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, it := range items {
		if it.Plugin == "widget" {
			return it.UpdateAvailable
		}
	}
	t.Fatal("widget not listed")
	return false
}

func TestCheckUpdates_FlagsAGitPluginWhoseRemoteMovedUntilItIsUpgraded(t *testing.T) {
	f := installURLPlugin(t, unpinned)
	if listedUpdateAvailable(t, f.m) {
		t.Fatal("unchecked plugin listed as having an update")
	}
	if checkThenList(t, f.m) {
		t.Fatal("plugin at its remote head listed as having an update")
	}
	advanceRepo(t, f.pluginRepo)
	if !checkThenList(t, f.m) {
		t.Fatal("plugin behind its remote head not listed as having an update")
	}
	// The remote moves again after the check, so the upgrade lands past the
	// checked head; the check's answer must not outlive the upgrade.
	advanceRepo(t, f.pluginRepo)
	if _, err := f.m.Upgrade(context.Background(), "widget", "acme"); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if listedUpdateAvailable(t, f.m) {
		t.Fatal("upgraded plugin still listed as having an update")
	}
}

func TestCheckUpdates_AnswerGoesStaleWhenTheInstallChangesAnyOtherWay(t *testing.T) {
	f := installURLPlugin(t, unpinned)
	advanceRepo(t, f.pluginRepo)
	if !checkThenList(t, f.m) {
		t.Fatal("plugin behind its remote head not listed as having an update")
	}
	advanceRepo(t, f.pluginRepo)
	// A reinstall (or a CLI upgrade, another Manager) changes the installed
	// commit without this Manager's Upgrade.
	if err := f.m.Remove(context.Background(), "widget", "acme"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := f.m.Install(context.Background(), "widget", "acme"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if listedUpdateAvailable(t, f.m) {
		t.Fatal("reinstalled plugin listed with the update checked before the reinstall")
	}
}

func TestCheckUpdates_CancelledCheckKeepsThePreviousAnswer(t *testing.T) {
	f := installURLPlugin(t, unpinned)
	advanceRepo(t, f.pluginRepo)
	if !checkThenList(t, f.m) {
		t.Fatal("plugin behind its remote head not listed as having an update")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.m.CheckUpdates(ctx); err == nil {
		t.Fatal("cancelled CheckUpdates reported success")
	}
	if !listedUpdateAvailable(t, f.m) {
		t.Fatal("cancelled check discarded the previous answer")
	}
}

func TestCheckUpdates_ShaPinnedSourceAsksNoRemote(t *testing.T) {
	for name, pin := range map[string]func(string, string) string{
		"full":        func(_, head string) string { return `,"sha":"` + head + `"` },
		"abbreviated": func(_, head string) string { return `,"sha":"` + strings.ToUpper(head[:7]) + `"` },
	} {
		t.Run(name, func(t *testing.T) {
			f := installURLPlugin(t, pin)
			advanceRepo(t, f.pluginRepo)
			// A check that reached the remote would now fail and warn.
			if err := os.Rename(f.pluginRepo, f.pluginRepo+".gone"); err != nil {
				t.Fatal(err)
			}
			if checkThenList(t, f.m) {
				t.Fatal("plugin at its pinned sha listed as having an update")
			}
			if w := f.warnings(); w != "" {
				t.Fatalf("pinned check reached the remote: %s", w)
			}
		})
	}
}

func TestCheckUpdates_FlagsAPinTheCatalogMoved(t *testing.T) {
	f := installURLPlugin(t, func(_, head string) string { return `,"sha":"` + head + `"` })
	next := advanceRepo(t, f.pluginRepo)
	writeURLCatalog(t, f.mktRepo, f.pluginRepo, `,"sha":"`+next+`"`)
	gitIn(t, f.mktRepo, "commit", "-aqm", "pin next")
	if err := f.m.RefreshMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RefreshMarketplace: %v", err)
	}
	if !checkThenList(t, f.m) {
		t.Fatal("plugin whose catalog pin moved not listed as having an update")
	}
}

func TestCheckUpdates_FollowsTheCatalogRef(t *testing.T) {
	f := installURLPlugin(t, func(repo, _ string) string {
		gitIn(t, repo, "branch", "stable")
		return `,"ref":"stable"`
	})
	advanceRepo(t, f.pluginRepo)
	if checkThenList(t, f.m) {
		t.Fatal("plugin flagged for a commit on a branch its catalog ref doesn't follow")
	}
	gitIn(t, f.pluginRepo, "branch", "-f", "stable", "main")
	if !checkThenList(t, f.m) {
		t.Fatal("plugin whose ref moved not listed as having an update")
	}
}

func TestCheckUpdates_UnreachableRemoteWarnsAndFlagsNothing(t *testing.T) {
	f := installURLPlugin(t, unpinned)
	advanceRepo(t, f.pluginRepo)
	if !checkThenList(t, f.m) {
		t.Fatal("plugin behind its remote head not listed as having an update")
	}
	if err := os.Rename(f.pluginRepo, f.pluginRepo+".gone"); err != nil {
		t.Fatal(err)
	}
	if checkThenList(t, f.m) {
		t.Fatal("plugin with an unreachable remote listed as having an update")
	}
	if w := f.warnings(); !strings.Contains(w, "widget@acme") {
		t.Fatalf("no warning names the plugin: %q", w)
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
	advanceRepo(t, mktRepo)
	if checkThenList(t, m) {
		t.Fatal("relative-source plugin listed as having an update")
	}
}

func TestGitRemoteHead_ResolvesTheCommitACheckoutOfRefLandsOn(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	first := makeGitRepo(t, repo, "f.txt", "1")
	gitIn(t, repo, "tag", "-a", "v1", "-m", "release")
	gitIn(t, repo, "branch", "feature")
	head := advanceRepo(t, repo)

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

// Run under -race this also guards the two kinds of warning against sharing
// a slice, but only when the remote check happens to start before the broken
// catalog is read, which map order makes an occasional event.
func TestCheckUpdates_WarnsAboutEveryRemoteAndCatalogItCannotRead(t *testing.T) {
	f := installURLPlugin(t, unpinned)
	name := "other"
	mktRepo := makeMarketplaceRepoWithPlugin(t, name, "gadget")
	ref, err := f.m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := f.m.Install(context.Background(), "gadget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ref.InstallLocation, ".claude-plugin", "marketplace.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.pluginRepo, f.pluginRepo+".gone"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.CheckUpdates(context.Background()); err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	w := f.warnings()
	if !strings.Contains(w, "checking widget@acme for updates") || !strings.Contains(w, "reading marketplace.json for "+name) {
		t.Fatalf("warnings miss the unreachable remote or the unreadable catalog: %q", w)
	}
}
