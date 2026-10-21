package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const testDataVolumeAlias = "/System/Volumes/Data"

func aliasPath(path string) string { return testDataVolumeAlias + path }

// The classifier must treat plain and /System/Volumes/Data spellings of the
// same managed root as one directory, in both directions and for both roots.
func TestTargetIsMixedManagedContentNormalizesDataVolumeAlias(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	m := NewManager(root)
	aliasRoot := aliasPath(root)
	aliasManager := NewManager(aliasRoot)

	cacheTarget := filepath.Join(root, "cache", "acme", "widget", "rev", "metadata")
	bundledTarget := filepath.Join(root, "bundled", "widget-0123456789abcdef", "metadata")
	external := filepath.Join(t.TempDir(), "metadata")

	cases := []struct {
		name    string
		manager *Manager
		target  string
		want    bool
	}{
		{name: "cache plain", manager: m, target: cacheTarget, want: true},
		{name: "cache alias target", manager: m, target: aliasPath(cacheTarget), want: true},
		{name: "cache alias root", manager: aliasManager, target: cacheTarget, want: true},
		{name: "cache both alias", manager: aliasManager, target: aliasPath(cacheTarget), want: true},
		{name: "bundled plain", manager: m, target: bundledTarget, want: true},
		{name: "bundled alias target", manager: m, target: aliasPath(bundledTarget), want: true},
		{name: "bundled alias root", manager: aliasManager, target: bundledTarget, want: true},
		{name: "git component", manager: m, target: filepath.Join(root, "cache", "acme", "widget", "rev", ".git", "config"), want: false},
		{name: "prefix sibling", manager: m, target: filepath.Join(root, "cache2", "acme", "widget"), want: false},
		{name: "external", manager: m, target: external, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.manager.targetIsMixedManagedContent(tc.target); got != tc.want {
				t.Fatalf("targetIsMixedManagedContent(%q) = %v, want %v", tc.target, got, tc.want)
			}
		})
	}
}

// A legacy .git link or gitdir file that names an ordinary directory through
// the alias spelling must stay in place, leave the store unverified, and leave
// every ordinary byte untouched. Removing it then lets migration complete.
func TestManagedMetadataMigrationPreservesAliasTargets(t *testing.T) {
	for _, kind := range []string{"symlink", "gitdir"} {
		t.Run(kind, func(t *testing.T) {
			m := NewManager(t.TempDir())
			var warnings bytes.Buffer
			m.Stderr = &warnings

			rev := filepath.Join(m.cacheDir(), "acme", "widget", "rev")
			metadata := filepath.Join(rev, "metadata")
			writeTestFile(t, filepath.Join(metadata, "object"), "metadata bytes")
			ordinary := filepath.Join(rev, "README.md")
			writeTestFile(t, ordinary, "ordinary bytes")

			entry := filepath.Join(rev, ".git")
			target := aliasPath(metadata)
			switch kind {
			case "symlink":
				if err := os.Symlink(target, entry); err != nil {
					t.Fatal(err)
				}
			case "gitdir":
				if err := os.WriteFile(entry, []byte("gitdir: "+target+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			m.ensureManagedContentMigrated(context.Background())
			if m.managedContentClean() {
				t.Fatal("an alias target inside managed content was called clean")
			}
			if _, err := os.Lstat(entry); err != nil {
				t.Fatalf("alias .git entry was moved: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(metadata, "object")); err != nil || !bytes.Equal(got, []byte("metadata bytes")) {
				t.Fatalf("metadata target bytes = %q, %v", got, err)
			}
			if got, err := os.ReadFile(ordinary); err != nil || !bytes.Equal(got, []byte("ordinary bytes")) {
				t.Fatalf("ordinary bytes = %q, %v", got, err)
			}
			if warnings.Len() == 0 {
				t.Fatal("an unverified migration emitted no diagnostic")
			}
			stateBytes, err := os.ReadFile(filepath.Join(m.Root, storeFormatFileName))
			if err != nil {
				t.Fatal(err)
			}
			var state storeFormat
			if err := json.Unmarshal(stateBytes, &state); err != nil {
				t.Fatal(err)
			}
			if state.Version != 1 || state.ManagedContent != managedContentUnverified {
				t.Fatalf("persisted state = %+v, want version 1 unverified", state)
			}

			if err := os.Remove(entry); err != nil {
				t.Fatal(err)
			}
			m.ensureManagedContentMigrated(context.Background())
			if !m.managedContentClean() {
				t.Fatal("retry after removing the alias entry did not complete")
			}
		})
	}
}

// APFS can return a case or Unicode spelling different from the configured
// store root. These run only where the filesystem actually does so; Linux CI
// is case-sensitive and skips them.
func TestManagedMetadataMigrationNativeIdentitySpellings(t *testing.T) {
	cases := []struct {
		name    string
		actual  string
		variant string
	}{
		{name: "case", actual: "Store", variant: "store"},
		{name: "unicode", actual: "café", variant: "cafe\u0301"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			actualRoot := filepath.Join(home, tc.actual)
			variantRoot := filepath.Join(home, tc.variant)
			if err := os.MkdirAll(actualRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(variantRoot); err != nil {
				t.Skip("filesystem does not equate the configured spelling with the actual one")
			}
			m := NewManager(variantRoot)
			rev := filepath.Join(actualRoot, "cache", "acme", "widget", "rev")
			metadata := filepath.Join(rev, "metadata")
			writeTestFile(t, filepath.Join(metadata, "object"), "metadata bytes")
			entry := filepath.Join(rev, ".git")
			if err := os.Symlink(metadata, entry); err != nil {
				t.Fatal(err)
			}
			m.ensureManagedContentMigrated(context.Background())
			if m.managedContentClean() {
				t.Fatal("a native identity-spelling target was called clean")
			}
			if _, err := os.Lstat(entry); err != nil {
				t.Fatalf("native identity-spelling .git entry was moved: %v", err)
			}
			if err := os.Remove(entry); err != nil {
				t.Fatal(err)
			}
			m.ensureManagedContentMigrated(context.Background())
			if !m.managedContentClean() {
				t.Fatal("retry after removing the native identity-spelling entry did not complete")
			}
		})
	}
}
