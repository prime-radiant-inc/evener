//go:build serffuzz

package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func FuzzCoverageSourceManifestUnion(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		t.Run("source-existing", func(t *testing.T) {
			TestSource_UnmarshalObjectForms(t)
			TestSource_UnmarshalStringForm(t)
			TestSource_UnmarshalRejectsUnknownKind(t)
			TestFetchPluginSource_RelativeCopiesFromMarketplace(t)
		})
		t.Run("manifest-existing", func(t *testing.T) {
			TestHasPluginManifest(t)
			TestEnsureManifestFallback_ExistingManifestIsNoop(t)
			TestEnsureManifestFallback_NoManifestNoFields_Installs(t)
			TestEnsureManifestFallback_NotStaged_ClearError(t)
			TestEnsureManifestFallback_PackageJSONBin_WiresMCP(t)
			TestEnsureManifestFallback_BinTargetMissing_NoteNoWire(t)
			TestEnsureManifestFallback_MultipleBins_PicksPluginName(t)
			TestEnsureManifestFallback_MultipleBins_NoMatch_NoteSkip(t)
			TestEnsureManifestFallback_MCPJSONPresent_NoBinWiring(t)
			TestEnsureManifestFallback_NoPackageJSON_LSPShape(t)
			TestEnsureManifestFallback_PackageJSONNoBin(t)
			TestEnsureManifestFallback_BinEscapesDir_NoteSkip(t)
			TestEnsureManifestFallback_MalformedPackageJSON(t)
			TestEnsureManifestFallback_EmptyBinName_NoteSkip(t)
			TestEnsureManifestFallback_StringBin_NoteSkip(t)
		})
		coverageSourceJSON(t)
		coverageFetchSources(t)
		coverageCopyTreeErrors(t)
		coverageManifestFallbackErrors(t)
		coverageManifestVersion(t)
	})
}

func coverageSourceJSON(t *testing.T) {
	for _, input := range [][]byte{[]byte(`"unterminated`), []byte(`{`), []byte(`{"source":"git"}`), []byte(`{"source":"bogus"}`)} {
		var src Source
		_ = src.UnmarshalJSON(input)
	}
	_, _ = (Source{Kind: SourceURL}).MarshalJSON()
	for _, tc := range []struct{ a, b, c, want string }{
		{"plugin", "declared", "commit", "plugin"},
		{"", "declared", "commit", "declared"},
		{"", "", "1234567890123", "123456789012"},
		{"", "", "short", "short"},
		{"", "", "", "unknown"},
	} {
		if got := computeVersion(tc.a, tc.b, tc.c); got != tc.want {
			t.Fatalf("version %q", got)
		}
	}
}

func coverageFetchSources(t *testing.T) {
	oldClone, oldHead, oldSparse, oldRemove := sourceGitClone, sourceGitHeadSHA, sourceSparseClone, sourceRemoveAll
	t.Cleanup(func() {
		sourceGitClone, sourceGitHeadSHA, sourceSparseClone, sourceRemoveAll = oldClone, oldHead, oldSparse, oldRemove
	})
	boom := errors.New("boom")
	ctx := context.Background()
	sourceGitClone = func(context.Context, string, string, string, string) error { return boom }
	for _, src := range []Source{{Kind: SourceGitHub}, {Kind: SourceURL}} {
		if _, err := fetchPluginSource(ctx, src, "", t.TempDir()); err == nil {
			t.Fatal("clone failure accepted")
		}
	}
	sourceGitClone = func(context.Context, string, string, string, string) error { return nil }
	sourceGitHeadSHA = func(context.Context, string) (string, error) { return "sha", nil }
	for _, src := range []Source{{Kind: SourceGitHub}, {Kind: SourceURL}} {
		if sha, err := fetchPluginSource(ctx, src, "", t.TempDir()); err != nil || sha != "sha" {
			t.Fatalf("fetch = %q, %v", sha, err)
		}
	}
	sourceSparseClone = func(context.Context, string, string, string, string, string) error { return boom }
	if _, err := fetchPluginSource(ctx, Source{Kind: SourceGitSubdir}, "", filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Fatal("sparse failure accepted")
	}
	sourceSparseClone = func(_ context.Context, _ string, clone, sub, _, _ string) error {
		return os.MkdirAll(filepath.Join(clone, sub), 0o755)
	}
	dst := filepath.Join(t.TempDir(), "dst")
	if sha, err := fetchPluginSource(ctx, Source{Kind: SourceGitSubdir, Path: "sub"}, "", dst); err != nil || sha != "sha" {
		t.Fatalf("sparse fetch = %q, %v", sha, err)
	}
	sourceStat = func(string) (os.FileInfo, error) { return nil, boom }
	if _, err := fetchPluginSource(ctx, Source{Kind: SourceGitSubdir, Path: "sub"}, "", filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Fatal("sparse copy failure accepted")
	}
	if _, err := fetchPluginSource(ctx, Source{Kind: SourceDirectory, Path: "x"}, "", "y"); err == nil {
		t.Fatal("directory failure accepted")
	}
	sourceStat = os.Stat
	if _, err := fetchPluginSource(ctx, Source{Kind: "bad"}, "", ""); err == nil {
		t.Fatal("bad source accepted")
	}
}

func coverageCopyTreeErrors(t *testing.T) {
	oldStat, oldWalk, oldRel, oldOpen := sourceStat, sourceWalk, sourceRel, sourceOpen
	oldMkdir, oldOpenFile, oldCopy, oldClose := sourceMkdirAll, sourceOpenFile, sourceCopy, sourceClose
	t.Cleanup(func() {
		sourceStat, sourceWalk, sourceRel, sourceOpen = oldStat, oldWalk, oldRel, oldOpen
		sourceMkdirAll, sourceOpenFile, sourceCopy, sourceClose = oldMkdir, oldOpenFile, oldCopy, oldClose
	})
	boom := errors.New("boom")
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(file, t.TempDir()); err == nil {
		t.Fatal("file source accepted")
	}
	sourceWalk = func(root string, fn filepath.WalkFunc) error { return fn(root, nil, boom) }
	if err := copyTree(root, t.TempDir()); err == nil {
		t.Fatal("walk error accepted")
	}
	sourceWalk = filepath.Walk
	sourceRel = func(string, string) (string, error) { return "", boom }
	if err := copyTree(root, t.TempDir()); err == nil {
		t.Fatal("rel error accepted")
	}
	sourceRel = filepath.Rel
	sourceMkdirAll = func(string, os.FileMode) error { return boom }
	if err := copyTree(root, t.TempDir()); err == nil {
		t.Fatal("mkdir error accepted")
	}
	sourceMkdirAll = os.MkdirAll
	sourceOpen = func(string) (*os.File, error) { return nil, boom }
	if err := copyTree(root, t.TempDir()); err == nil {
		t.Fatal("open error accepted")
	}
	sourceOpen = os.Open
	calls := 0
	sourceMkdirAll = func(path string, mode os.FileMode) error {
		calls++
		if calls > 1 {
			return boom
		}
		return os.MkdirAll(path, mode)
	}
	if err := copyTree(root, t.TempDir()); err == nil {
		t.Fatal("parent mkdir error accepted")
	}
	sourceMkdirAll = os.MkdirAll
	sourceOpenFile = func(string, int, os.FileMode) (*os.File, error) { return nil, boom }
	if err := copyTree(root, t.TempDir()); err == nil {
		t.Fatal("create error accepted")
	}
	sourceOpenFile = os.OpenFile
	sourceCopy = func(io.Writer, io.Reader) (int64, error) { return 0, boom }
	if err := copyTree(root, t.TempDir()); err == nil {
		t.Fatal("copy error accepted")
	}
	sourceCopy = io.Copy
	closed := 0
	sourceClose = func(f *os.File) error {
		closed++
		_ = f.Close()
		if closed == 1 {
			return boom
		}
		return nil
	}
	if err := copyTree(root, t.TempDir()); err == nil {
		t.Fatal("close error accepted")
	}
}

func coverageManifestFallbackErrors(t *testing.T) {
	oldStat, oldRead, oldMarshal, oldIndent := manifestStat, manifestReadFile, manifestMarshal, manifestMarshalIndent
	oldMkdir, oldWrite := manifestMkdirAll, manifestWriteFile
	t.Cleanup(func() {
		manifestStat, manifestReadFile, manifestMarshal, manifestMarshalIndent = oldStat, oldRead, oldMarshal, oldIndent
		manifestMkdirAll, manifestWriteFile = oldMkdir, oldWrite
	})
	boom := errors.New("boom")
	dir := t.TempDir()
	manifestMarshalIndent = func(any, string, string) ([]byte, error) { return nil, boom }
	if _, err := ensureManifestFallback(dir, true, CatalogPlugin{Name: "p"}); err == nil {
		t.Fatal("marshal error accepted")
	}
	manifestMarshalIndent = json.MarshalIndent
	manifestMkdirAll = func(string, os.FileMode) error { return boom }
	if _, err := ensureManifestFallback(dir, true, CatalogPlugin{Name: "p"}); err == nil {
		t.Fatal("mkdir error accepted")
	}
	manifestMkdirAll = os.MkdirAll
	manifestWriteFile = func(string, []byte, os.FileMode) error { return boom }
	if _, err := ensureManifestFallback(dir, true, CatalogPlugin{Name: "p"}); err == nil {
		t.Fatal("write error accepted")
	}
	manifestWriteFile = os.WriteFile
	if _, err := ensureManifestFallback(dir, true, CatalogPlugin{Name: "p"}); err != nil {
		t.Fatal(err)
	}
	if note, err := ensureManifestFallback(dir, true, CatalogPlugin{Name: "ignored"}); err != nil || note != "" {
		t.Fatalf("existing fallback = %q, %v", note, err)
	}

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "package.json"), []byte(`{"bin":{"p":"cli.js"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "cli.js"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestMarshal = func(any) ([]byte, error) { return nil, boom }
	servers, note := detectNPMBinServer(binDir, "p")
	if servers != nil || note != "" {
		t.Fatalf("marshal failure = %s, %q", servers, note)
	}
}

func coverageManifestVersion(t *testing.T) {
	dir := t.TempDir()
	if got := pluginManifestVersion(dir); got != "" {
		t.Fatal(got)
	}
	for _, name := range []string{".claude-plugin", ".codex-plugin"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex-plugin", "plugin.json"), []byte(`{"version":"v2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := pluginManifestVersion(dir); got != "v2" {
		t.Fatalf("manifest version = %q", got)
	}
	if err := validatePluginDir(t.TempDir()); err == nil {
		t.Fatal("invalid plugin validated")
	}
}
