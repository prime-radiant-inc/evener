package sshconn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// SSH alone is replaced by a local shell. The generated commands execute the
// real hub endpoint entrypoint in separate processes, with real TOML and kernel
// path lookup. No hub is started and no live SSH server is used.
func TestOwnerConfigFileIdentity(t *testing.T) {
	build := t.TempDir()
	source := filepath.Join(build, "main.go")
	if err := os.WriteFile(source, []byte(`package main
import (
 "os"
 hub "primeradiant.com/evener/cmd/evener-hub"
)
func main() {
 args := os.Args[1:]
 if len(args) > 0 && args[0] == "hub" { args = args[1:] }
 os.Exit(hub.Run(args, os.Stdin, os.Stdout, os.Stderr))
}
`), 0600); err != nil {
		t.Fatal(err)
	}
	resolver := filepath.Join(build, "endpoint-resolver")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", resolver, source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build real endpoint resolver: %v\n%s", err, out)
	}
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(root, "physical", "inner"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "hub.toml")
	for _, p := range []string{config, filepath.Join(root, "other.toml"), filepath.Join(root, "unrelated.toml"), filepath.Join(root, "physical", "hub.toml")} {
		if err := os.WriteFile(p, fmt.Appendf(nil, "addr = \"127.0.0.1:0\"\nhub_state_root = %q\n", state), 0600); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(root, "alias.toml")
	if err := os.Symlink(config, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "physical", "inner"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, selection := range []struct{ name, path string }{
		{"absolute", config}, {"relative", "hub.toml"}, {"symlink", alias}, {"dotdot", root + "/physical/../hub.toml"},
	} {
		t.Run(selection.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, owner string
				same        bool
			}{
				{"same-absolute", config, true},
				{"relative-owner", "hub.toml", true},
				{"absolute-symlink-alias", alias, true},
				{"dotdot-alias", root + "/physical/../hub.toml", true},
				{"relative-symlink-dotdot-equivalent", "link/../../hub.toml", true},
				{"different-relative-file", "other.toml", false},
				{"unrelated-existing-absolute-file", filepath.Join(root, "unrelated.toml"), false},
				{"symlink-dotdot-different", root + "/link/../hub.toml", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ownerPath := tc.owner
					if !filepath.IsAbs(ownerPath) {
						ownerPath = root + "/" + ownerPath // Do not clean symlink/.. before kernel lookup.
					}
					a, err := os.Stat(config)
					if err != nil {
						t.Fatal(err)
					}
					b, err := os.Stat(ownerPath)
					if err != nil {
						t.Fatal(err)
					}
					if os.SameFile(a, b) != tc.same {
						t.Fatal("kernel file identity disagrees with fixture")
					}
					env := []string{"HOME=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_STATE_HOME=" + state}
					ownerResolutions := 0
					var selectedJSON []byte
					fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
						remote := strings.Join(argv[12:], " ")
						if strings.HasPrefix(remote, ownerContext(root, env)) && strings.Contains(remote, " hub endpoint --json") {
							ownerResolutions++
							// This one negative fixtures a misleading owner report. Its
							// argv sanity lookup still uses the real, different file.
							if tc.name == "unrelated-existing-absolute-file" {
								return selectedJSON, nil
							}
						}
						cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
						cmd.Dir = root
						if selection.name == "absolute" {
							// The selection resolver starts in a different directory
							// from the owner, whose command must cd to root itself.
							cmd.Dir = filepath.Join(root, "physical", "inner")
						}
						cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, env...)
						var stderr bytes.Buffer
						cmd.Stderr = &stderr
						out, err := cmd.Output()
						if err != nil {
							return nil, fmt.Errorf("real endpoint command: %w, stderr=%s", err, stderr.String())
						}
						return out, nil
					}}
					host := hostreg.Host{Name: "fixture", SSH: "fixture.invalid", EvenerPath: resolver, ConfigPath: selection.path}
					m := newTestManager(t, testRegistry(t, host), fr, Options{})
					selected, err := m.resolveHostEndpoint(t.Context(), host, selectedRestartFacts())
					if err != nil {
						t.Fatal(err)
					}
					selectedJSON, err = json.Marshal(selected)
					if err != nil {
						t.Fatal(err)
					}
					// TempDir itself may have a symlinked parent, notably /var on macOS.
					canonical, err := filepath.EvalSymlinks(config)
					if err != nil {
						t.Fatal(err)
					}
					if selected.ConfigPath != canonical || !selected.ConfigExists || selected.State != "stopped" || selected.HubStateRoot != state {
						t.Errorf("noncanonical or unexpected real report: %+v, want config %q", selected, canonical)
					}
					err = m.checkOwnerConfig(t.Context(), host, []string{resolver, "hub", "--config", tc.owner}, root, env, selected)
					if tc.same {
						if err != nil || ownerResolutions != 1 {
							t.Fatalf("same file: owner resolutions=%d err=%v", ownerResolutions, err)
						}
					} else if !errors.Is(err, errOwnerSelectionMismatch) {
						t.Fatalf("different existing file: got %v, want typed selection mismatch", err)
					}
				})
			}
		})
	}
}
