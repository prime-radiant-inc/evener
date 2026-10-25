package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/remoteinstall"
)

// The SSH boundary executes the actual generated POSIX shell in an owned home.
// PATH contains only named fixture tools, never the developer's evener.
type freshDeployEffects struct{ installers, pushes int }

func freshTargetShell(t *testing.T) (string, string, *fakeRunner, *freshDeployEffects) {
	t.Helper()
	home := t.TempDir()
	tools := filepath.Join(home, "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"uname", "id", "dirname", "readlink", "mkdir", "mktemp", "cat", "wc", "tr", "chmod", "mv", "rm"} {
		source, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(source, filepath.Join(tools, tool)); err != nil {
			t.Fatal(err)
		}
	}
	effects := &freshDeployEffects{}
	fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
		remote := strings.Join(argv[12:], " ")
		if in != nil {
			data, err := io.ReadAll(in)
			if err != nil {
				return nil, err
			}
			if bytes.Equal(data, remoteinstall.Script) {
				// Never run the network installer. Reaching this external side effect is
				// itself the failure in the appeared-target cases.
				effects.installers++
				return nil, errReplacementFixture
			}
			effects.pushes++
			in = bytes.NewReader(data)
		}
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
		cmd.Env = []string{"HOME=" + home, "PATH=" + tools, "XDG_CONFIG_HOME=", "XDG_STATE_HOME="}
		cmd.Dir = home
		cmd.Stdin = in
		return cmd.CombinedOutput()
	}}
	return home, tools, fr, effects
}

func TestFreshDeployRevalidatesResolvedTarget(t *testing.T) {
	savedChannel, savedDirty := buildinfo.Channel, buildinfo.GitDirty
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitDirty = savedChannel, savedDirty })
	buildinfo.Channel, buildinfo.GitDirty = "snapshot", ""
	for _, installer := range []bool{false, true} {
		for _, selector := range []string{"default", "configured", "PATH"} {
			t.Run(fmt.Sprintf("installer=%v/selector=%s", installer, selector), func(t *testing.T) {
				home, tools, fr, effects := freshTargetShell(t)
				target := filepath.Join(home, ".local", "bin", "evener")
				if selector == "PATH" {
					target = filepath.Join(tools, "evener")
				}
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				host := hostreg.Host{Name: "alpha", SSH: "fixture.invalid"}
				if selector == "configured" {
					host.EvenerPath = target
				}
				opts := Options{controllerVersionOverride: "dev"}
				if !installer {
					opts.BuildBinary = func(_ context.Context, _, _, out string) error {
						return os.WriteFile(out, []byte("new-push-bytes"), 0700)
					}
				}
				m := newTestManager(t, testRegistry(t, host), fr, opts)
				facts, err := m.preflight(t.Context(), host)
				if err != nil || !facts.ExecutableMissing {
					t.Fatalf("preflight=%+v err=%v", facts, err)
				}
				concurrent := []byte("#!/bin/sh\nprintf '%s\\n' '" + goodLaunchCheck + "'\n")
				if err := os.WriteFile(target, concurrent, 0700); err != nil {
					t.Fatal(err)
				}
				_, err = m.deploySelectedWithAuthority(t.Context(), host, facts, false)
				after, readErr := os.ReadFile(target)
				if err == nil || readErr != nil || !bytes.Equal(after, concurrent) || effects.installers != 0 || effects.pushes != 0 || len(fr.recordedStarts()) != 0 {
					t.Fatalf("appeared file not preserved/refused: err=%v read=%v same-bytes=%v installer=%d pushes=%d starts=%d", err, readErr, bytes.Equal(after, concurrent), effects.installers, effects.pushes, len(fr.recordedStarts()))
				}
				if !errors.Is(err, errOwnerEvidence) && !errors.Is(err, ErrEndpointMigration) {
					t.Fatalf("unexpected refusal class: %v", err)
				}
			})
		}
	}
}

func TestFreshDeployRevalidatesAfterBuild(t *testing.T) {
	home, _, fr, effects := freshTargetShell(t)
	target := filepath.Join(home, ".local", "bin", "evener")
	concurrent := []byte("#!/bin/sh\nprintf '%s\\n' '" + goodLaunchCheck + "'\n")
	host := hostreg.Host{Name: "alpha", SSH: "fixture.invalid"}
	m := newTestManager(t, testRegistry(t, host), fr, Options{BuildBinary: func(_ context.Context, _, _, out string) error {
		if err := os.WriteFile(target, concurrent, 0700); err != nil {
			return err
		}
		return os.WriteFile(out, []byte("new-push-bytes"), 0700)
	}})
	facts, err := m.preflight(t.Context(), host)
	if err != nil || !facts.ExecutableMissing {
		t.Fatalf("preflight=%+v err=%v", facts, err)
	}
	_, err = m.deploySelectedWithAuthority(t.Context(), host, facts, false)
	after, readErr := os.ReadFile(target)
	if !errors.Is(err, errOwnerEvidence) || readErr != nil || !bytes.Equal(after, concurrent) || effects.pushes != 0 {
		t.Fatalf("post-build revalidation: err=%v read=%v same-bytes=%v", err, readErr, bytes.Equal(after, concurrent))
	}
}

func TestFreshDeployResolvedTargetFailuresAreNotAbsence(t *testing.T) {
	for _, failure := range []string{"transport", "unreadable-file", "unsearchable-parent"} {
		t.Run(failure, func(t *testing.T) {
			if failure == "unsearchable-parent" && os.Geteuid() == 0 {
				t.Skip("root bypasses directory search permissions")
			}
			home, _, fr, effects := freshTargetShell(t)
			target := filepath.Join(home, ".local", "bin", "evener")
			concurrent := []byte("concurrent-installation-bytes")
			host := hostreg.Host{Name: "alpha", SSH: "fixture.invalid"}
			originalRun := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				if failure == "transport" && strings.Join(argv[12:], " ") == target+" launch-check --json" {
					return nil, errReplacementFixture
				}
				return originalRun(ctx, argv, in)
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{BuildBinary: func(_ context.Context, _, _, out string) error {
				if failure != "transport" {
					if err := os.WriteFile(target, concurrent, 0000); err != nil {
						return err
					}
				}
				if failure == "unsearchable-parent" {
					if err := os.Chmod(filepath.Dir(target), 0000); err != nil {
						return err
					}
				}
				return os.WriteFile(out, []byte("new-push-bytes"), 0700)
			}})
			facts, err := m.preflight(t.Context(), host)
			if err != nil || !facts.ExecutableMissing {
				t.Fatalf("preflight=%+v err=%v", facts, err)
			}
			_, err = m.deploySelectedWithAuthority(t.Context(), host, facts, false)
			if restoreErr := os.Chmod(filepath.Dir(target), 0700); restoreErr != nil {
				t.Fatal(restoreErr)
			}
			want := ErrSSHStart
			switch failure {
			case "unreadable-file":
				want = ErrEndpointMigration
			case "unsearchable-parent":
				want = ErrDeploy // The post-build target resolution refuses first.
			}
			if !errors.Is(err, want) || effects.installers != 0 || effects.pushes != 0 || len(fr.recordedStarts()) != 0 {
				t.Fatalf("failure=%s err=%v installers=%d pushes=%d starts=%d", failure, err, effects.installers, effects.pushes, len(fr.recordedStarts()))
			}
			if failure == "transport" {
				if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("transport authorized write: %v", err)
				}
			} else {
				if err := os.Chmod(target, 0600); err != nil {
					t.Fatal(err)
				}
				after, readErr := os.ReadFile(target)
				if readErr != nil || !bytes.Equal(after, concurrent) {
					t.Fatalf("concurrent bytes changed: %v", readErr)
				}
			}
		})
	}
}

func TestFreshDeployDefaultAbsentPushSucceeds(t *testing.T) {
	home, _, fr, effects := freshTargetShell(t)
	host := hostreg.Host{Name: "alpha", SSH: "fixture.invalid"}
	data := []byte("new-push-bytes")
	m := newTestManager(t, testRegistry(t, host), fr, Options{BuildBinary: func(_ context.Context, _, _, out string) error { return os.WriteFile(out, data, 0700) }})
	facts, err := m.preflight(t.Context(), host)
	if err != nil || !facts.ExecutableMissing {
		t.Fatalf("preflight=%+v err=%v", facts, err)
	}
	target, err := m.deploySelectedWithAuthority(t.Context(), host, facts, false)
	expected := filepath.Join(home, ".local", "bin", "evener")
	after, readErr := os.ReadFile(expected)
	if err != nil || target != expected || readErr != nil || !bytes.Equal(after, data) || effects.installers != 0 || effects.pushes != 1 {
		t.Fatalf("target=%s err=%v read=%v installer=%d", target, err, readErr, effects.installers)
	}
}
