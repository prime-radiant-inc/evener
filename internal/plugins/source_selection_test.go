package plugins

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

const sourceMetadataMarker = "synthetic-source-metadata-marker"

func addSourceMarker(t *testing.T, repo string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"),
		[]byte("[core]\n\trepositoryformatversion = 0\n# "+sourceMetadataMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func addAdminMarker(t *testing.T, adminDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(adminDir, "config"),
		[]byte("[core]\n\trepositoryformatversion = 0\n# "+sourceMetadataMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSourceCatalog(t *testing.T, marketRepo, plugin, sourceJSON string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(marketRepo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"` + plugin + `","source":` + sourceJSON + `}]}`
	if err := os.WriteFile(filepath.Join(marketRepo, ".claude-plugin", "marketplace.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, marketRepo, "README.md", "market")
}

func cacheContains(t *testing.T, root, needle string) bool {
	t.Helper()
	found := false
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Contains(data, []byte(needle)) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	// An absent root means the refused install never published anything. Any
	// other error means the scan itself failed, which must not read as absent.
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		t.Fatalf("walking %s: %v", root, walkErr)
	}
	return found
}

func TestInstallRejectsGitMetadataSourceSelections(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	root := t.TempDir()
	pluginRepo := filepath.Join(root, "plugin-repo")
	makeGitRepo(t, pluginRepo, "README.md", "plugin")
	addSourceMarker(t, pluginRepo)
	if err := os.WriteFile(filepath.Join(pluginRepo, ".git", "objects", "synthetic-marker"), []byte(sourceMetadataMarker), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "metadata-alias")
	if err := os.Symlink(filepath.Join(pluginRepo, ".git"), alias); err != nil {
		t.Fatal(err)
	}
	bareRepo := filepath.Join(root, "bare.git")
	gitIn(t, root, "init", "-q", "--bare", bareRepo)
	addAdminMarker(t, bareRepo)
	separateAdmin := filepath.Join(root, "separate-admin.git")
	separateWorktree := filepath.Join(root, "separate-worktree")
	gitIn(t, root, "init", "-q", "--separate-git-dir", separateAdmin, separateWorktree)
	addAdminMarker(t, separateAdmin)
	linkedWorktree := filepath.Join(root, "linked-worktree")
	gitIn(t, pluginRepo, "worktree", "add", "-q", linkedWorktree)
	linkedAdmin := filepath.Join(pluginRepo, ".git", "worktrees", filepath.Base(linkedWorktree))
	addAdminMarker(t, linkedAdmin)

	cases := []struct {
		name   string
		source string
	}{
		{name: "direct root", source: `{"source":"directory","path":"` + filepath.Join(pluginRepo, ".git") + `"}`},
		{name: "metadata subtree", source: `{"source":"directory","path":"` + filepath.Join(pluginRepo, ".git", "objects") + `"}`},
		{name: "resolved alias subtree", source: `{"source":"directory","path":"` + filepath.Join(alias, "objects") + `"}`},
		{name: "relative metadata", source: `"./.git"`},
		{name: "git subdir metadata", source: `{"source":"git-subdir","url":"` + pluginRepo + `","path":".git"}`},
		{name: "bare repository", source: `{"source":"directory","path":"` + bareRepo + `"}`},
		{name: "separate git dir", source: `{"source":"directory","path":"` + separateAdmin + `"}`},
		{name: "linked worktree admin", source: `{"source":"directory","path":"` + linkedAdmin + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marketRepo := filepath.Join(root, "market-"+tc.name)
			writeSourceCatalog(t, marketRepo, "widget", tc.source)
			if tc.name == "relative metadata" {
				addSourceMarker(t, marketRepo)
			}
			m := NewManager(t.TempDir())
			ctx := context.Background()
			if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: marketRepo}); err != nil {
				t.Fatal(err)
			}
			if tc.name == "relative metadata" {
				if err := os.WriteFile(filepath.Join(m.marketplacesDir(), "acme", ".git", "config"), []byte("# "+sourceMetadataMarker+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := m.Install(ctx, "widget", "acme"); err == nil {
				t.Fatal("a source selecting Git metadata installed successfully")
			}
			if cacheContains(t, m.cacheDir(), sourceMetadataMarker) {
				t.Fatal("source Git metadata was published into the content root")
			}
			items, err := m.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 0 {
				t.Fatalf("a refused source selection was registered: %+v", items)
			}
		})
	}
}

func installDirectorySource(t *testing.T, sourcePath string) (InstallEntry, error) {
	t.Helper()
	marketRepo := filepath.Join(t.TempDir(), "market")
	writeSourceCatalog(t, marketRepo, "widget", `{"source":"directory","path":"`+sourcePath+`"}`)
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: marketRepo}); err != nil {
		t.Fatal(err)
	}
	return m.Install(ctx, "widget", "acme")
}

func TestInstallAllowsWorkingTreeDirectorySource(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	pluginRepo := filepath.Join(t.TempDir(), "plugin-worktree")
	makeGitRepo(t, pluginRepo, "README.md", "plugin")
	writePlugin(t, pluginRepo, "widget", map[string]string{"resources/companion.txt": "companion"})
	gitIn(t, pluginRepo, "add", ".")
	gitIn(t, pluginRepo, "commit", "-qm", "manifest")

	entry, err := installDirectorySource(t, pluginRepo)
	if err != nil {
		t.Fatalf("a normal repository working tree was refused: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(entry.InstallPath, ".git")); !os.IsNotExist(err) {
		t.Fatalf("published working tree carries .git: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(entry.InstallPath, "resources", "companion.txt")); err != nil || string(got) != "companion" {
		t.Fatalf("ordinary working-tree content = %q, %v", got, err)
	}
}

func TestInstallAllowsPluginTreeThatLooksLikeGitAdministration(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	pluginDir := filepath.Join(t.TempDir(), "plugin-with-git-like-names")
	writePlugin(t, pluginDir, "widget", map[string]string{
		"HEAD":            "ordinary plugin file",
		"config":          "[core]\nordinary plugin file\n",
		"objects/content": "ordinary plugin file",
	})
	if _, err := installDirectorySource(t, pluginDir); err != nil {
		t.Fatalf("an ordinary plugin tree with Git-like names was refused: %v", err)
	}
}

func TestInstallWithoutGitRefusesUnverifiableAdministration(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available to build the fixture")
	}
	root := t.TempDir()
	admin := filepath.Join(root, "bare-admin")
	gitIn(t, root, "init", "-q", "--bare", admin)
	addAdminMarker(t, admin)
	marketRepo := filepath.Join(root, "market")
	writeSourceCatalog(t, marketRepo, "widget", `{"source":"directory","path":"`+admin+`"}`)
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: marketRepo}); err != nil {
		t.Fatal(err)
	}
	oldLookPath := gitLookPath
	gitLookPath = func(string) (string, error) { return "", errors.New("git unavailable") }
	t.Cleanup(func() { gitLookPath = oldLookPath })
	if _, err := m.Install(ctx, "widget", "acme"); err == nil {
		t.Fatal("an unverifiable administrative directory installed without Git")
	}
	if cacheContains(t, m.cacheDir(), sourceMetadataMarker) {
		t.Fatal("unverifiable administration was published without Git")
	}
}

func TestInstallRejectsGitMetadataSelectionWithGitTrace(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	for _, tc := range []struct {
		name  string
		trace bool
	}{
		{name: "without trace"},
		{name: "with trace", trace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			makeGitRepo(t, repo, "README.md", "plugin")
			addSourceMarker(t, repo)
			marketRepo := filepath.Join(root, "market")
			writeSourceCatalog(t, marketRepo, "widget",
				`{"source":"directory","path":"`+filepath.Join(repo, ".git")+`"}`)
			m := NewManager(t.TempDir())
			ctx := context.Background()
			if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: marketRepo}); err != nil {
				t.Fatal(err)
			}
			if tc.trace {
				// Trace output goes to stderr; it must not change the verdict.
				t.Setenv("GIT_TRACE", "1")
			}
			if _, err := m.Install(ctx, "widget", "acme"); err == nil {
				t.Fatal("a source selecting Git metadata installed successfully")
			}
			if cacheContains(t, m.cacheDir(), sourceMetadataMarker) {
				t.Fatal("source Git metadata was published into the content root")
			}
			walkErr := filepath.Walk(m.cacheDir(), func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				switch base := filepath.Base(path); {
				case !info.IsDir() && (base == "config" || base == "HEAD" || base == "objects"):
					t.Fatalf("source metadata file %q was published into the content root", path)
				case info.IsDir() && base == "objects":
					t.Fatalf("source metadata directory %q was published into the content root", path)
				}
				return nil
			})
			if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
				t.Fatalf("walking %s: %v", m.cacheDir(), walkErr)
			}
		})
	}
}

func TestInstallAllowsGitLikeTreeInsideAWorktree(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	outer := filepath.Join(t.TempDir(), "outer")
	makeGitRepo(t, outer, "README.md", "outer")
	pluginDir := filepath.Join(outer, "plugin-with-git-like-names")
	files := map[string]string{
		"HEAD":              "ref: refs/heads/main\n",
		"config":            "[core]\nordinary plugin file\n",
		"objects/info/note": "ordinary plugin bytes",
	}
	writePlugin(t, pluginDir, "widget", files)

	entry, err := installDirectorySource(t, pluginDir)
	if err != nil {
		t.Fatalf("an ordinary plugin tree inside a worktree was refused: %v", err)
	}
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(entry.InstallPath, rel))
		if err != nil || string(got) != want {
			t.Fatalf("published %s = %q, %v; want %q", rel, got, err, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(entry.InstallPath, ".git")); !os.IsNotExist(err) {
		t.Fatalf("published tree carries .git: %v", err)
	}
}

func setAmbientRepositorySelection(t *testing.T, unrelated string) {
	t.Helper()
	for name, value := range map[string]string{
		"GIT_DIR":                          filepath.Join(unrelated, ".git"),
		"GIT_WORK_TREE":                    unrelated,
		"GIT_COMMON_DIR":                   filepath.Join(unrelated, ".git"),
		"GIT_INDEX_FILE":                   filepath.Join(unrelated, "index"),
		"GIT_OBJECT_DIRECTORY":             filepath.Join(unrelated, ".git", "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(unrelated, ".git", "objects"),
		"GIT_CEILING_DIRECTORIES":          unrelated,
		"GIT_DISCOVERY_ACROSS_FILESYSTEM":  "1",
	} {
		t.Setenv(name, value)
	}
}

func TestAmbientRepositorySelectionCannotAuthorizeMetadata(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	root := t.TempDir()
	pluginRepo := filepath.Join(root, "plugin")
	makeGitRepo(t, pluginRepo, "README.md", "plugin")
	addSourceMarker(t, pluginRepo)
	unrelated := filepath.Join(root, "unrelated")
	makeGitRepo(t, unrelated, "README.md", "unrelated")
	marketRepo := filepath.Join(root, "market")
	writeSourceCatalog(t, marketRepo, "widget", `{"source":"directory","path":"`+filepath.Join(pluginRepo, ".git")+`"}`)
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: marketRepo}); err != nil {
		t.Fatal(err)
	}
	setAmbientRepositorySelection(t, unrelated)
	if _, err := m.Install(ctx, "widget", "acme"); err == nil {
		t.Fatal("ambient repository selection authorized a metadata source")
	}
	if cacheContains(t, m.cacheDir(), sourceMetadataMarker) {
		t.Fatal("ambient repository selection exposed metadata in managed content")
	}
}

func TestAmbientRepositorySelectionDoesNotBreakNormalInstall(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	root := t.TempDir()
	pluginRepo := filepath.Join(root, "plugin")
	makeGitRepo(t, pluginRepo, "README.md", "plugin")
	writePlugin(t, pluginRepo, "widget", map[string]string{"resources/companion.txt": "companion"})
	gitIn(t, pluginRepo, "add", ".")
	gitIn(t, pluginRepo, "commit", "-qm", "manifest")
	marketRepo := filepath.Join(root, "market")
	writeURLCatalog(t, marketRepo, "widget", pluginRepo, "")
	makeGitRepo(t, marketRepo, "README.md", "market")
	unrelated := filepath.Join(root, "unrelated")
	makeGitRepo(t, unrelated, "README.md", "unrelated")
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: marketRepo}); err != nil {
		t.Fatal(err)
	}
	setAmbientRepositorySelection(t, unrelated)
	entry, err := m.Install(ctx, "widget", "acme")
	if err != nil {
		t.Fatalf("ambient repository selection broke a normal install: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(entry.InstallPath, "resources", "companion.txt")); err != nil || string(got) != "companion" {
		t.Fatalf("normal installed content = %q, %v", got, err)
	}
}
