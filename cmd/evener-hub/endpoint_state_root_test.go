package hub

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEndpointConfigRelativeStateRoot(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	physical := filepath.Join(root, "physical")
	for _, dir := range []string{"hub-state", "hub-state ", "physical/inner", "physical/hub-state"} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(physical, "inner"), "link"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, configured, want string
	}{
		{"relative", "hub-state", filepath.Join(root, "hub-state")},
		{"missing", "fresh/nested", filepath.Join(root, "fresh", "nested")},
		{"trailing-space", "hub-state ", filepath.Join(root, "hub-state ")},
		{"symlink-dotdot", "link/../hub-state", filepath.Join(physical, "hub-state")},
		{"symlink-dotdot-missing", "link/../fresh/nested", filepath.Join(physical, "fresh", "nested")},
		{"absolute-unchanged", root + "/link/../hub-state", root + "/link/../hub-state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.HubStateRoot = tc.configured
			resolved, _, err := resolveLoadedEndpointConfig(cfg, "absent.toml", EndpointInput{})
			if err != nil || !filepath.IsAbs(resolved.HubStateRoot) || resolved.HubStateRoot != tc.want {
				t.Fatalf("effective hub_state_root=%q err=%v, want %q", resolved.HubStateRoot, err, tc.want)
			}
			if cfg.HubStateRoot != tc.configured {
				t.Fatal("effective resolution changed configured input")
			}
			if roots := runningStateRoots(resolved.HubStateRoot, ""); len(roots) != 1 || roots[0] != resolved.HubStateRoot {
				t.Fatalf("health changed effective root bytes: %q, want %q", roots, resolved.HubStateRoot)
			}
		})
	}
	for _, path := range []string{"fresh", "physical/fresh"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only resolution created %q: %v", path, err)
		}
	}
}
