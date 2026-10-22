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
	"primeradiant.com/evener/execsupport/shellquote"
)

// SSH alone is replaced by a local shell. The generated commands execute the
// real hub endpoint entrypoint in separate processes, with real TOML and kernel
// path lookup. No hub is started and no live SSH server is used.
func buildConfigIdentityResolver(t *testing.T) string {
	t.Helper()
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
	return resolver
}

func TestOwnerConfigFileIdentity(t *testing.T) {
	resolver := buildConfigIdentityResolver(t)
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

func TestSystemdConfigFileIdentity(t *testing.T) {
	resolver := buildConfigIdentityResolver(t)
	root := t.TempDir()
	cwd := filepath.Join(root, "working ")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "hub.toml")
	for _, file := range []string{config, filepath.Join(root, "other.toml")} {
		if err := os.WriteFile(file, fmt.Appendf(nil, "addr = \"127.0.0.1:0\"\nhub_state_root = %q\n", filepath.Join(root, "state")), 0600); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(root, "alias.toml")
	if err := os.Symlink(config, alias); err != nil {
		t.Fatal(err)
	}
	for _, live := range []bool{false, true} {
		for _, selector := range []string{alias, filepath.Join(root, "other.toml")} {
			t.Run(fmt.Sprintf("live=%t/%s", live, filepath.Base(selector)), func(t *testing.T) {
				statCommands := map[string]bool{}
				for _, filename := range []string{selector, config} {
					quoted := shellquote.RemoteWord(filename)
					statCommands[ownerContext(cwd, []string{"HOME=" + root})+"stat -Lc '%d %i' "+quoted+" 2>/dev/null || stat -L -f '%d %i' "+quoted] = true
				}
				fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
					return nil, fmt.Errorf("unexpected remote command: %v", argv)
				}}
				fixture := modelSelectedProcess(t, fr, resolver, "", "")
				fixture.stopped = !live
				base := fr.runFn
				serviceStarts, adHocStarts := 0, 0
				stop := errors.New("intercepted selected service start")
				fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
					remote := strings.Join(argv[12:], " ")
					switch {
					case remote == "systemctl start evener-hub.service":
						serviceStarts++
						return nil, stop
					case strings.Contains(remote, "nohup "):
						adHocStarts++
						return nil, stop
					case remote == supervisorShowRemote(supervisor{kind: supervisorSystemd, label: "evener-hub.service"}):
						pid := "0"
						if live {
							pid = "4242"
						}
						return fmt.Appendf(nil, "Id=evener-hub.service\nMainPID=%s\nUser=dev\nExecStart={ path=%s ; argv[]=%s hub --config %s ; ignore_errors=no ; }\nWorkingDirectory=%s\n", pid, resolver, resolver, selector, cwd), nil
					case remote == hubArgvRemote("4242"):
						return []byte(resolver + "\x00hub\x00--config\x00" + selector + "\x00"), nil
					case remote == "readlink /proc/4242/cwd", remote == "pwd":
						return []byte(cwd + "\n"), nil
					case remote == "getent passwd dev | cut -d: -f6":
						return []byte(root + "\n"), nil
					case remote == freshOwnerEnvironmentRemote:
						return []byte("HOME=" + root + "\x00"), nil
					case remote == serviceLockVnodeRemote(filepath.Join(root, "state"), "4242"):
						return []byte("match\n"), nil // Declared process holds this selected root's lock.
					case statCommands[remote], strings.Contains(remote, " hub endpoint --json"), strings.Contains(remote, "evener_resolve"), strings.HasSuffix(remote, " && pwd -P"):
						cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
						cmd.Dir = root
						cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
						out, err := cmd.Output()
						if err != nil {
							return out, fmt.Errorf("%s: %w", remote, err)
						}
						return out, nil
					}
					return base(ctx, argv, in)
				}
				host := hostreg.Host{Name: "fixture", SSH: "dev@fixture.invalid", EvenerPath: resolver, ConfigPath: config}
				m := newTestManager(t, testRegistry(t, host), fr, Options{})
				report, err := m.resolveHostEndpoint(t.Context(), host, selectedRestartFacts())
				if err != nil {
					t.Fatal(err)
				}
				if live {
					pid, err := m.selectedServicePID(t.Context(), host, report)
					if selector == alias && (err != nil || pid != "4242") {
						t.Fatalf("same file skipped: pid=%q err=%v", pid, err)
					}
					if selector != alias && err == nil {
						t.Fatal("unrelated service selected")
					}
					return
				}
				owner, err := m.resolveStoppedOwner(t.Context(), host, selectedRestartFacts(), report)
				if err != nil {
					t.Fatal(err)
				}
				if selector != alias {
					if owner.supervisor.kind != supervisorNone {
						t.Fatalf("unrelated service selected: %+v", owner)
					}
					return
				}
				err = m.bootstrapHub(t.Context(), host, selectedRestartFacts(), "", "")
				if !errors.Is(err, stop) || serviceStarts != 1 || adHocStarts != 0 || owner.supervisor.kind != supervisorSystemd {
					t.Fatalf("bootstrap: err=%v service=%d adhoc=%d owner=%+v", err, serviceStarts, adHocStarts, owner)
				}
			})
		}
	}
}
