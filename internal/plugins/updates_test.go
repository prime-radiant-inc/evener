package plugins

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	writeURLCatalog(t, f.mktRepo, "widget", f.pluginRepo, sourceExtra(f.pluginRepo, head))
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

// writeURLCatalog writes acme's marketplace.json into mktRepo, listing plugin
// by a url source at pluginRepo with sourceExtra appended to that source.
func writeURLCatalog(t *testing.T, mktRepo, plugin, pluginRepo, sourceExtra string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(mktRepo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mktRepo, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"`+plugin+`","source":{"source":"url","url":"`+pluginRepo+`"`+sourceExtra+`}}]}`), 0o644); err != nil {
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
	writeURLCatalog(t, f.mktRepo, "widget", f.pluginRepo, `,"sha":"`+next+`"`)
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

// A plugin stored in its marketplace's own repo ("./plugins/widget") is
// upgradable from the refreshed marketplace clone. A check flags it when the
// clone's tree at its folder is not the one installed, so a commit elsewhere
// in the marketplace flags nothing, and an Upgrade copies the folder's new
// contents and settles the flag.
func TestCheckUpdates_RelativeSourcePluginUpgradesFromItsRefreshedMarketplace(t *testing.T) {
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(context.Background(), "widget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}
	refresh := func() {
		t.Helper()
		if err := m.RefreshMarketplace(context.Background(), name); err != nil {
			t.Fatalf("RefreshMarketplace: %v", err)
		}
	}
	advanceRepo(t, mktRepo)
	refresh()
	if checkThenList(t, m) {
		t.Fatal("plugin flagged by a marketplace commit that did not touch its folder")
	}

	if err := os.WriteFile(filepath.Join(mktRepo, "plugins", "widget", "extra.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, mktRepo, "add", ".")
	gitIn(t, mktRepo, "commit", "-qm", "change widget")
	// The check refreshes the marketplace first, so a change pushed since
	// the last refresh is seen without one.
	if !checkThenList(t, m) {
		t.Fatal("plugin whose folder changed in its marketplace not flagged")
	}
	entry, err := m.Upgrade(context.Background(), "widget", name)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(entry.InstallPath, "extra.txt")); err != nil || string(b) != "v2" {
		t.Fatalf("upgraded plugin's extra.txt = %q (%v), want the marketplace's new contents", b, err)
	}
	if checkThenList(t, m) {
		t.Fatal("upgraded plugin still flagged")
	}
}

// A relative plugin is flagged by what its folder holds, not by the history
// that touched it: commits that change the folder and change it back flag
// nothing, as a reclone that moves a shallow clone's root does not.
func TestCheckUpdates_RelativePluginWhoseFolderIsUnchangedIsNotFlagged(t *testing.T) {
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(context.Background(), "widget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}
	extra := filepath.Join(mktRepo, "plugins", "widget", "extra.txt")
	if err := os.WriteFile(extra, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, mktRepo, "add", ".")
	gitIn(t, mktRepo, "commit", "-qm", "change widget")
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	gitIn(t, mktRepo, "add", "-A")
	gitIn(t, mktRepo, "commit", "-qm", "change it back")
	if err := m.RefreshMarketplace(context.Background(), name); err != nil {
		t.Fatalf("RefreshMarketplace: %v", err)
	}
	if checkThenList(t, m) {
		t.Fatal("plugin whose folder holds what was installed flagged")
	}
}

// A relative plugin installed before installs recorded its folder's tree has
// none, so the first check flags it: it may be behind its marketplace, and
// nothing says otherwise. One Upgrade records the tree and settles it.
func TestCheckUpdates_RelativePluginInstalledWithNoCommitIsFlaggedOnce(t *testing.T) {
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(context.Background(), "widget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	key := registryKey("widget", name)
	reg.Plugins[key][0].GitCommitSha = ""
	if err := m.saveRegistry(reg); err != nil {
		t.Fatal(err)
	}
	if !checkThenList(t, m) {
		t.Fatal("relative plugin installed with no commit not flagged")
	}
	if _, err := m.Upgrade(context.Background(), "widget", name); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if checkThenList(t, m) {
		t.Fatal("relative plugin still flagged after its upgrade recorded the commit")
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
		{"refs/tags/v1", first},
		{"refs/heads/feature", first},
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

func TestCheckUpdates_AnswerGoesStaleWhenItsMarketplaceChanges(t *testing.T) {
	f := installURLPlugin(t, unpinned)
	advanceRepo(t, f.pluginRepo)
	if !checkThenList(t, f.m) {
		t.Fatal("plugin behind its remote head not listed as having an update")
	}
	// A refresh can change the catalog's source, ref or pin, so an answer
	// checked against the old catalog no longer says what Upgrade would do.
	if err := f.m.RefreshMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RefreshMarketplace: %v", err)
	}
	if listedUpdateAvailable(t, f.m) {
		t.Fatal("answer checked against the catalog before a refresh still listed")
	}
}

func TestCheckUpdates_AnOlderCheckCannotOverwriteANewerOne(t *testing.T) {
	m := NewManager(t.TempDir())
	older := m.beginCheck()
	newer := m.beginCheck()
	m.publishCheck(newer, map[string]checkedHead{"widget@acme": {head: "b", installed: "a"}})
	m.publishCheck(older, map[string]checkedHead{})
	if !m.updateAvailable("widget@acme", "a") {
		t.Fatal("an older check's late answer replaced a newer check's")
	}
}

func TestCheckUpdates_UnfetchedMarketplaceIsNotReadFromTheWorkingDirectory(t *testing.T) {
	f := installURLPlugin(t, unpinned)
	advanceRepo(t, f.pluginRepo)
	// A catalog in the working directory must never be taken for the
	// marketplace's: a recorded but unfetched marketplace has no clone.
	cwd := t.TempDir()
	writeURLCatalog(t, cwd, "widget", f.pluginRepo, "")
	t.Chdir(cwd)
	mk, err := f.m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	ref := mk["acme"]
	ref.InstallLocation = ""
	mk["acme"] = ref
	if err := f.m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}
	// The check's refresh would fetch it; with its source gone it stays
	// unfetched.
	if err := os.Rename(f.mktRepo, f.mktRepo+".gone"); err != nil {
		t.Fatal(err)
	}
	if checkThenList(t, f.m) {
		t.Fatal("plugin flagged from a catalog read out of the working directory")
	}
}

// An upgrade that lands while a check is in flight leaves the plugin
// unflagged once the check publishes: the check's answer (the remote's head
// before it moved) is for the install it read, which the upgrade replaced.
// This is the overlap the hub allows by running evener/plugin/checkUpdates
// off the connection's serial worker.
func TestCheckUpdates_AnUpgradeDuringACheckIsNotFlaggedAfterIt(t *testing.T) {
	f := installURLPlugin(t, unpinned)
	answered := make(chan struct{})
	publish := make(chan struct{})
	realGit := gitRun
	t.Cleanup(func() { gitRun = realGit })
	gitRun = func(ctx context.Context, dir string, args ...string) (string, error) {
		out, err := realGit(ctx, dir, args...)
		if len(args) > 0 && args[0] == "ls-remote" {
			close(answered)
			<-publish
		}
		return out, err
	}
	checked := make(chan error, 1)
	go func() { checked <- f.m.CheckUpdates(context.Background()) }()
	<-answered
	// The remote moves on and the plugin is upgraded to it after the check
	// read the old head, before it publishes.
	advanceRepo(t, f.pluginRepo)
	if _, err := f.m.Upgrade(context.Background(), "widget", "acme"); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	close(publish)
	if err := <-checked; err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if listedUpdateAvailable(t, f.m) {
		t.Fatal("a check that read the remote before an upgrade flagged the upgraded plugin")
	}
}

func TestCheckUpdates_WarnsAboutAPluginWhoseMarketplaceIsGone(t *testing.T) {
	f := installURLPlugin(t, unpinned)
	reg, err := LoadRegistry(f.m.registryPath())
	if err != nil {
		t.Fatal(err)
	}
	reg.Plugins["ghost@gone"] = []InstallEntry{{Source: Source{Kind: SourceURL, URL: f.pluginRepo}, GitCommitSha: "abc"}}
	if err := SaveRegistry(f.m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}
	if err := f.m.CheckUpdates(context.Background()); err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if w := f.warnings(); strings.Count(w, `marketplace "gone"`) != 1 {
		t.Fatalf("want one warning naming the missing marketplace, got %q", w)
	}
}

func TestCheckUpdates_WarnsAboutAPluginKeyWithNoMarketplace(t *testing.T) {
	f := installURLPlugin(t, unpinned)
	reg, err := LoadRegistry(f.m.registryPath())
	if err != nil {
		t.Fatal(err)
	}
	reg.Plugins["ghost"] = []InstallEntry{{Source: Source{Kind: SourceURL, URL: f.pluginRepo}, GitCommitSha: "abc"}}
	if err := SaveRegistry(f.m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}
	if err := f.m.CheckUpdates(context.Background()); err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if w := f.warnings(); w != "warning: plugin \"ghost\" is installed with no marketplace; not checking it for updates\n" {
		t.Fatalf("want one warning naming the plugin with no marketplace, got %q", w)
	}
}

// A remote that never answers is cut off by the check's overall deadline, not
// only by its own updateCheckTimeout, so the check answers before a client
// waiting on it gives up. The hung remote is warned about like any other
// remote that cannot be read.
func TestCheckUpdates_OverallDeadlineCutsOffAHungRemote(t *testing.T) {
	f := installURLPlugin(t, unpinned)
	realGit, realDeadline := gitRun, updateCheckDeadline
	t.Cleanup(func() { gitRun, updateCheckDeadline = realGit, realDeadline })
	updateCheckDeadline = 50 * time.Millisecond
	gitRun = func(ctx context.Context, dir string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "ls-remote" {
			<-ctx.Done()
			return "", ctx.Err()
		}
		return realGit(ctx, dir, args...)
	}
	start := time.Now()
	if checkThenList(t, f.m) {
		t.Fatal("plugin whose remote hung listed as having an update")
	}
	// The warning names the deadline whichever timeout ended the hang, so
	// only the time taken shows the overall deadline, not the 20s
	// per-remote one, cut it off.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("check took %v; the overall deadline did not cut off the hung remote", elapsed)
	}
	if w := f.warnings(); !strings.Contains(w, "checking widget@acme for updates: "+errUpdateCheckDeadline.Error()) {
		t.Fatalf("no warning names the hung plugin: %q", w)
	}
}

// A marketplace the check cannot refresh is warned about and checked as its
// clone stands; the check itself still answers.
func TestCheckUpdates_AMarketplaceThatCannotRefreshIsAWarning(t *testing.T) {
	f := installURLPlugin(t, unpinned)
	advanceRepo(t, f.pluginRepo)
	if err := os.Rename(f.mktRepo, f.mktRepo+".gone"); err != nil {
		t.Fatal(err)
	}
	if !checkThenList(t, f.m) {
		t.Fatal("plugin behind its remote head not flagged when its marketplace could not refresh")
	}
	if w := f.warnings(); !strings.Contains(w, `refreshing marketplace "acme"`) {
		t.Fatalf("no warning names the marketplace that could not refresh: %q", w)
	}
}
