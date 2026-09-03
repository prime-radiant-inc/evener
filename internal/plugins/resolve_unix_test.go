//go:build unix

package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"primeradiant.com/evener/internal/bundled"
)

// Preview and launch must agree about a store that cannot be published into:
// a read-only <Root>/bundled fails both rather than previewing as selectable
// and then failing the launch it promised.
func TestPreviewForLaunch_ReadOnlyStoreFailsPreviewAndLaunchAlike(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory mode")
	}
	m := NewManager(t.TempDir())
	store := filepath.Join(m.Root, "bundled")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store, 0o755) })

	preview, err := m.PreviewForLaunch(nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := preview.ValidateSelection(); err == nil {
		t.Errorf("preview selected a bundled plugin a launch cannot publish: %+v", preview.Candidates)
	}
	if len(preview.Diagnostics) != 1 || preview.Diagnostics[0].Source != LaunchPluginSourceBundled {
		t.Errorf("preview Diagnostics = %+v, want one bundled diagnostic", preview.Diagnostics)
	}

	launch, err := m.ResolveForLaunch(nil, &[]string{"coordinator-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := launch.ValidateSelection(); err == nil {
		t.Fatalf("launch selected a bundled plugin from a read-only store: %+v", launch.Candidates)
	}
}

// Publication cannot destroy foreign data that lands on the destination after
// it was observed absent. The source of the rename is always a directory (the
// staging payload), and rename(2) refuses to replace anything a foreign writer
// could leave there: a regular file fails with ENOTDIR, a non-empty directory
// with ENOTEMPTY. Only an absent path or an empty directory can be replaced,
// so the check-then-rename window costs a failed publish, never a lost file.
// The publish that lost the rename does not adopt what it found there either.
func TestBundledStore_PublishNeverReplacesForeignData(t *testing.T) {
	tests := []struct {
		name    string
		plant   func(t *testing.T, dest string)
		want    []error
		survive func(t *testing.T, dest string)
	}{
		{
			name: "regular file",
			plant: func(t *testing.T, dest string) {
				if err := os.WriteFile(dest, []byte("someone else's data"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: []error{syscall.ENOTDIR},
			survive: func(t *testing.T, dest string) {
				content, err := os.ReadFile(dest)
				if err != nil {
					t.Fatal(err)
				}
				if string(content) != "someone else's data" {
					t.Errorf("destination content = %q, want it untouched", content)
				}
			},
		},
		{
			name: "non-empty directory",
			plant: func(t *testing.T, dest string) {
				if err := os.MkdirAll(filepath.Join(dest, "theirs"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: []error{syscall.ENOTEMPTY, syscall.EEXIST},
			survive: func(t *testing.T, dest string) {
				if _, err := os.Stat(filepath.Join(dest, "theirs")); err != nil {
					t.Errorf("destination lost its contents: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			dest, staging, err := m.prepareBundledStore("coordinator-workflow", true)
			if err != nil {
				t.Fatal(err)
			}
			if staging == nil {
				t.Fatal("prepareBundledStore adopted a published copy in an empty store")
			}
			t.Cleanup(func() { _ = os.RemoveAll(staging.dir) })
			if err := os.CopyFS(staging.payload, mustSubFS(bundled.Plugins(), "coordinator-workflow")); err != nil {
				t.Fatal(err)
			}
			// The race the absent-destination check cannot exclude: a foreign
			// writer takes the destination between that check and the rename.
			test.plant(t, dest)

			err = os.Rename(staging.payload, dest)
			if err == nil {
				t.Fatalf("publishing over a %s succeeded", test.name)
			}
			matched := false
			for _, want := range test.want {
				matched = matched || errors.Is(err, want)
			}
			if !matched {
				t.Errorf("rename error = %v, want one of %v", err, test.want)
			}
			test.survive(t, dest)

			// What a publisher that lost the rename does next: it adopts the
			// winner's copy. Foreign data is not that copy, so the adoption
			// check refuses it rather than loading it as the bundled plugin.
			adopted, adoptErr := publishedBundledCopy(dest, staging.digest)
			if adopted || adoptErr == nil {
				t.Errorf("publishedBundledCopy = %v, %v; want a refusal for the %s at the destination", adopted, adoptErr, test.name)
			}
		})
	}
}
