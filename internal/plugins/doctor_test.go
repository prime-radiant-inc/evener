package plugins

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// findFinding returns the first finding whose Message contains substr, or
// fails the test if none matches.
func findFinding(t *testing.T, findings []DoctorFinding, substr string) DoctorFinding {
	t.Helper()
	for _, f := range findings {
		if strings.Contains(f.Message, substr) {
			return f
		}
	}
	t.Fatalf("no finding contains %q; findings=%+v", substr, findings)
	return DoctorFinding{}
}

func hasFinding(findings []DoctorFinding, substr string) bool {
	for _, f := range findings {
		if strings.Contains(f.Message, substr) {
			return true
		}
	}
	return false
}

func TestDoctor_OrphanEntryMissingInstallPath(t *testing.T) {
	m := NewManager(t.TempDir())
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{InstallPath: filepath.Join(m.Root, "nowhere"), Version: "1.0.0", Enabled: true, Source: Source{Kind: SourceDirectory, Path: "/nowhere"}}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "widget@acme")
	if f.Level != LevelFail {
		t.Errorf("orphan entry level = %s, want %s; finding=%+v", f.Level, LevelFail, f)
	}
	if f.Remediation == "" {
		t.Error("orphan entry finding has no remediation")
	}
}

func TestDoctor_OrphanEntryNotADirectory(t *testing.T) {
	m := NewManager(t.TempDir())
	filePath := filepath.Join(m.Root, "not-a-dir")
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{InstallPath: filePath, Version: "1.0.0", Enabled: true, Source: Source{Kind: SourceDirectory, Path: filePath}}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "not a directory")
	if f.Level != LevelFail {
		t.Errorf("level = %s, want %s", f.Level, LevelFail)
	}
}

func TestDoctor_OrphanCacheDir(t *testing.T) {
	m := NewManager(t.TempDir())
	orphan := m.pluginCacheDir("acme", "widget", "deadbeef")
	writePlugin(t, orphan, "widget", nil)
	if err := SaveRegistry(m.registryPath(), Registry{Plugins: map[string][]InstallEntry{}}); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, orphan)
	if f.Level != LevelWarn {
		t.Errorf("orphan cache dir level = %s, want %s", f.Level, LevelWarn)
	}
	if !strings.Contains(f.Remediation, "gc") {
		t.Errorf("remediation should point at gc: %q", f.Remediation)
	}
}

func TestDoctor_OrphanCacheDir_NoneWhenReferenced(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := m.pluginCacheDir("acme", "widget", "deadbeef")
	writePlugin(t, dir, "widget", nil)
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{InstallPath: dir, Version: "1.0.0", Enabled: true, Source: Source{Kind: SourceDirectory, Path: dir}}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if hasFinding(findings, "orphaned cache directory") {
		t.Errorf("referenced cache dir flagged as orphan: %+v", findings)
	}
}

func TestDoctor_VersionMismatchWarns(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := filepath.Join(t.TempDir(), "widget")
	writePlugin(t, dir, "widget", nil) // plugin.json version "1.0.0"
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{InstallPath: dir, Version: "0.9.0", Enabled: true, Source: Source{Kind: SourceDirectory, Path: dir}}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "does not match")
	if f.Level != LevelWarn {
		t.Errorf("version mismatch level = %s, want %s", f.Level, LevelWarn)
	}
	if !strings.Contains(f.Message, "0.9.0") || !strings.Contains(f.Message, "1.0.0") {
		t.Errorf("message should name both versions: %q", f.Message)
	}
}

func TestDoctor_VersionMatch_NoWarning(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := filepath.Join(t.TempDir(), "widget")
	writePlugin(t, dir, "widget", nil) // plugin.json version "1.0.0"
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{InstallPath: dir, Version: "1.0.0", Enabled: true, Source: Source{Kind: SourceDirectory, Path: dir}}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if hasFinding(findings, "does not match") {
		t.Errorf("matching versions should not warn: %+v", findings)
	}
}

// TestDoctor_VersionMismatch_DirectorySource_HonestRemediation reproduces the
// Important finding where a directory (or Rel) source's version-mismatch WARN
// pointed at `serf plugin upgrade`, which Manager.Upgrade always no-ops for
// such a source (sourceCannotUpgrade): following the remediation could never
// clear the warning. The remediation must instead be honest about that.
func TestDoctor_VersionMismatch_DirectorySource_HonestRemediation(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := filepath.Join(t.TempDir(), "widget")
	writePlugin(t, dir, "widget", nil) // plugin.json version "1.0.0"
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{InstallPath: dir, Version: "0.9.0", Enabled: true, Source: Source{Kind: SourceDirectory, Path: dir}}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "does not match")
	if strings.Contains(f.Remediation, "serf plugin upgrade") {
		t.Errorf("directory-source version mismatch must not point at the no-op upgrade command: %q", f.Remediation)
	}
	if !strings.Contains(f.Remediation, "directory") {
		t.Errorf("directory-source version mismatch remediation should explain the directory-source situation: %q", f.Remediation)
	}
}

// TestDoctor_VersionMismatch_GitSource_PointsAtUpgrade confirms the git-backed
// case (where Manager.Upgrade can actually resync the version) keeps pointing
// at `serf plugin upgrade` — only sources that can never upgrade get the
// honest alternative text.
func TestDoctor_VersionMismatch_GitSource_PointsAtUpgrade(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := filepath.Join(t.TempDir(), "widget")
	writePlugin(t, dir, "widget", nil) // plugin.json version "1.0.0"
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{
			InstallPath: dir, Version: "0.9.0", Enabled: true,
			Source: Source{Kind: SourceURL, URL: "https://example.com/widget.git"},
		}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "does not match")
	if !strings.Contains(f.Remediation, "serf plugin upgrade widget@acme") {
		t.Errorf("git-backed version mismatch should still point at upgrade: %q", f.Remediation)
	}
}

func TestDoctor_BrokenComponentFailsValidation(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := filepath.Join(t.TempDir(), "widget")
	writePlugin(t, dir, "widget", map[string]string{
		"agents/broken.md": "no frontmatter here",
	})
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{InstallPath: dir, Version: "1.0.0", Enabled: true, Source: Source{Kind: SourceDirectory, Path: dir}}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "widget@acme")
	if f.Level != LevelFail || f.Category != "component" {
		t.Errorf("broken component finding = %+v, want FAIL/component", f)
	}
}

func TestDoctor_DisabledBrokenPluginSkipsComponentCheck(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := filepath.Join(t.TempDir(), "widget")
	writePlugin(t, dir, "widget", map[string]string{
		"agents/broken.md": "no frontmatter here",
	})
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{InstallPath: dir, Version: "1.0.0", Enabled: false, Source: Source{Kind: SourceDirectory, Path: dir}}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if hasFinding(findings, "widget@acme") {
		t.Errorf("disabled broken plugin should not surface a component finding: %+v", findings)
	}
}

func TestDoctor_AutoUpgradeOnDirectorySourceWarns(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := filepath.Join(t.TempDir(), "widget")
	writePlugin(t, dir, "widget", nil)
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{
			InstallPath: dir, Version: "1.0.0", Enabled: true, AutoUpgrade: true,
			Source: Source{Kind: SourceDirectory, Path: dir},
		}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "auto-upgrade is on")
	if f.Level != LevelWarn || f.Category != "autoupgrade" {
		t.Errorf("autoupgrade sanity finding = %+v, want WARN/autoupgrade", f)
	}
}

func TestDoctor_AutoUpgradeOnRelSourceWarns(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := filepath.Join(t.TempDir(), "widget")
	writePlugin(t, dir, "widget", nil)
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{
			InstallPath: dir, Version: "1.0.0", Enabled: true, AutoUpgrade: true,
			Source: Source{Kind: SourceDirectory, Path: "./plugins/widget", Rel: true},
		}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if !hasFinding(findings, "auto-upgrade is on") {
		t.Errorf("expected autoupgrade sanity warning for a Rel source; findings=%+v", findings)
	}
}

func TestDoctor_AutoUpgradeOnGitSource_NoWarning(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := filepath.Join(t.TempDir(), "widget")
	writePlugin(t, dir, "widget", nil)
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{
			InstallPath: dir, Version: "1.0.0", Enabled: true, AutoUpgrade: true,
			Source: Source{Kind: SourceURL, URL: "https://example.com/widget.git"},
		}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if hasFinding(findings, "auto-upgrade is on") {
		t.Errorf("git-backed auto-upgrade should not warn: %+v", findings)
	}
}

func TestDoctor_MarketplaceMissingClone(t *testing.T) {
	m := NewManager(t.TempDir())
	loc := filepath.Join(m.Root, "marketplaces", "acme")
	mk := Marketplaces{"acme": {Source: Source{Kind: SourceURL, URL: "https://example.com/acme.git"}, InstallLocation: loc, LastUpdated: time.Now()}}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "acme")
	if f.Level != LevelFail || f.Category != "marketplace" {
		t.Errorf("missing clone finding = %+v, want FAIL/marketplace", f)
	}
}

func TestDoctor_MarketplaceBadJSON(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := t.TempDir() // exists, but has no .claude-plugin/marketplace.json
	mk := Marketplaces{"local": {Source: Source{Kind: SourceDirectory, Path: dir}, InstallLocation: dir, LastUpdated: time.Now()}}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "marketplace.json invalid")
	if f.Level != LevelFail {
		t.Errorf("bad marketplace.json level = %s, want %s", f.Level, LevelFail)
	}
}

func TestDoctor_MarketplaceSeededPointerIsOK(t *testing.T) {
	m := NewManager(t.TempDir())
	mk := Marketplaces{"acme": {Source: Source{Kind: SourceGitHub, Repo: "acme/plugins"}}} // empty InstallLocation
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "acme")
	if f.Level != LevelOK {
		t.Errorf("seeded pointer level = %s, want %s; finding=%+v", f.Level, LevelOK, f)
	}
	if !strings.Contains(f.Message, "not yet fetched") {
		t.Errorf("seeded pointer message should say not yet fetched: %q", f.Message)
	}
}

func TestDoctor_MarketplaceNotAGitRepo(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	m := NewManager(t.TempDir())
	dir := t.TempDir() // a real directory, but never `git init`-ed
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := Marketplaces{"acme": {Source: Source{Kind: SourceURL, URL: "https://example.com/acme.git"}, InstallLocation: dir, LastUpdated: time.Now()}}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "not a valid git repository")
	if f.Level != LevelFail {
		t.Errorf("non-repo clone level = %s, want %s", f.Level, LevelFail)
	}
}

func TestDoctor_MarketplaceStaleWarns(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	src := makeMarketplaceRepo(t, "acme")
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: src}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}

	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	ref := mk["acme"]
	ref.LastUpdated = time.Now().Add(-60 * 24 * time.Hour)
	mk["acme"] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "acme")
	if f.Level != LevelWarn {
		t.Errorf("stale marketplace level = %s, want %s; finding=%+v", f.Level, LevelWarn, f)
	}
	if !strings.Contains(f.Message, "last updated") {
		t.Errorf("stale message should mention last updated: %q", f.Message)
	}
}

func TestDoctor_MarketplaceHealthy_DirectorySource(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := makeMarketplaceRepoDirNoGit(t, "acme")
	mk := Marketplaces{"acme": {Source: Source{Kind: SourceDirectory, Path: dir}, InstallLocation: dir, LastUpdated: time.Now().Add(-999 * 24 * time.Hour)}}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "acme")
	if f.Level != LevelOK {
		t.Errorf("healthy directory marketplace level = %s, want %s (staleness must not apply to directory sources); finding=%+v", f.Level, LevelOK, f)
	}
}

// makeMarketplaceRepoDirNoGit builds a marketplace.json without git — for
// directory-source tests where no clone/pull semantics apply.
func makeMarketplaceRepoDirNoGit(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mkt-"+name)
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	mj := `{"name":"` + name + `","owner":{"name":"o"},"plugins":[]}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDoctor_EnvironmentWritable(t *testing.T) {
	m := NewManager(t.TempDir())
	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	f := findFinding(t, findings, "is writable")
	if f.Level != LevelOK {
		t.Errorf("writable store level = %s, want %s", f.Level, LevelOK)
	}

	wantGitLevel := LevelWarn
	if gitAvailable() {
		wantGitLevel = LevelOK
	}
	g := findFinding(t, findings, "git")
	if g.Level != wantGitLevel {
		t.Errorf("git environment finding level = %s, want %s", g.Level, wantGitLevel)
	}
}

// TestDoctor_StoreRootNotYetCreated_DoesNotCreateOrFail reproduces the
// Important finding where checkStoreWritable called os.MkdirAll on the store
// root before probing it, so merely running Doctor on a fresh machine created
// the store directory — a mutation a read-only diagnostic must never make. A
// not-yet-created root must also not be reported as a FAIL.
func TestDoctor_StoreRootNotYetCreated_DoesNotCreateOrFail(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nope", "plugins")
	m := NewManager(root)

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if _, statErr := os.Stat(root); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("Doctor must not create the store root %s (stat err = %v)", root, statErr)
	}
	for _, f := range findings {
		if f.Category == catEnvironment && f.Level == LevelFail {
			t.Errorf("a not-yet-created store root must not FAIL: %+v", f)
		}
	}
}

func TestDoctor_CorruptRegistryReturnsError(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.registryPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Doctor(); err == nil {
		t.Fatal("Doctor should surface a corrupt installed_plugins.json as an error")
	}
}

func TestDoctor_CorruptMarketplacesReturnsError(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.marketplacesFile(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Doctor(); err == nil {
		t.Fatal("Doctor should surface a corrupt known_marketplaces.json as an error")
	}
}

// TestDoctor_CleanStoreHasNoFailOrWarn is the happy-path check: a fully
// healthy enabled plugin and a writable store produce no FAIL/WARN findings
// (beyond, possibly, the environment's git-availability WARN on a machine
// without git — excluded here since it is environment-dependent, not a
// store-health signal).
func TestDoctor_CleanStoreHasNoFailOrWarn(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := filepath.Join(t.TempDir(), "widget")
	writePlugin(t, dir, "widget", nil)
	reg := Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{InstallPath: dir, Version: "1.0.0", Enabled: true, Source: Source{Kind: SourceDirectory, Path: dir}}},
	}}
	if err := SaveRegistry(m.registryPath(), reg); err != nil {
		t.Fatal(err)
	}

	findings, err := m.Doctor()
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	for _, f := range findings {
		if f.Category == catEnvironment && f.Level == LevelWarn {
			continue // git-availability is a machine property, not a store defect
		}
		if f.Level == LevelWarn || f.Level == LevelFail {
			t.Errorf("clean store produced a non-OK finding: %+v", f)
		}
	}
}
