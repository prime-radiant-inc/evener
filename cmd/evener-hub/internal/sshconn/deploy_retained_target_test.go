package sshconn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/remoteinstall"
)

// Target resolution, short-transfer rejection and atomic push writes use the real
// filesystem. The embedded installer runs with local archive/checksum downloads;
// process and systemd observations are modeled, not SSH.
func TestRetainedDeploymentInstallTargetBoundary(t *testing.T) {
	channel, dirty, tag := buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = channel, dirty, tag })
	buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = "snapshot", "", ""
	for _, mode := range []string{"honest-push", "redirected-recorded-target", "installer-alias", "installer-regular-target", "alias-during-build", "destination-during-build", "stopped-honest-push", "stopped-alias-retarget", "endpoint-transport", "endpoint-cancelled", "endpoint-schema"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			target, alias, other := filepath.Join(root, "installed", "evener"), filepath.Join(root, "bin", "evener"), filepath.Join(root, "other", "evener")
			for _, name := range []string{target, alias, other} {
				if err := os.Mkdir(filepath.Dir(name), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for name, data := range map[string]string{target: "predecessor", other: "other-installation"} {
				if err := os.WriteFile(name, []byte(data), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, alias); err != nil {
				t.Fatal(err)
			}
			launch := alias
			if mode == "installer-regular-target" {
				launch = target
			}
			installerEnv := retainedInstallerFixture(t)
			outside := filepath.Join(root, "share", "evener", "bin", "evener")
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: launch}
			fr, selected := deployRunnerForFixture(t, "linux", "amd64", func(int) ([]byte, error) {
				data, err := os.ReadFile(target)
				if err != nil {
					return nil, err
				}
				version := "oldsha"
				if string(data) == "staged-binary" {
					version = "newsha"
				}
				return launchContract(version), nil
			}, func(int) ([]byte, error) {
				return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
			})
			selected.exe = launch
			selected.stopped = strings.HasPrefix(mode, "stopped-")
			if selected.stopped {
				selected.observe = nil
			}
			base := fr.runFn
			writes, installers, starts := 0, 0, 0
			var recorded []string
			var m *Manager
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				run := func(input io.Reader) ([]byte, error) {
					cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
					cmd.Stdin = input
					return cmd.Output()
				}
				if remote == resolveDeployCommand(target) {
					return run(in)
				}
				switch remote {
				case resolveDeployCommand(alias), createTargetCommand(target), "test -d " + shellquote.RemoteWord(filepath.Dir(alias)), "test -d " + shellquote.RemoteWord(filepath.Dir(target)):
					return run(in)
				case pushBinaryRemote(target, int64(len("staged-binary"))), pushBinaryRemote(other, int64(len("staged-binary"))):
					writes++
					recorded = append(recorded, m.pendingRestart(host.Name).installTarget)
					if writes == 1 {
						return run(io.LimitReader(in, 3))
					}
					return run(in)
				}
				bindir, share, _, err := installerDirs(host, Preflight{})
				if err != nil {
					t.Fatal(err)
				}
				if remote == remoteinstall.Command("snapshot", "", bindir, share) {
					data, err := io.ReadAll(in)
					if err != nil || !bytes.Equal(data, remoteinstall.Script) {
						t.Fatalf("installer input changed: %v", err)
					}
					installers++
					recorded = append(recorded, m.pendingRestart(host.Name).installTarget)
					cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
					cmd.Env = installerEnv
					cmd.Stdin = bytes.NewReader(data)
					return cmd.CombinedOutput()
				}
				if strings.Contains(remote, "cat >") {
					t.Fatalf("unexpected write command: %s", remote)
				}
				if strings.HasPrefix(remote, "systemctl start ") || strings.HasPrefix(remote, "systemctl restart ") {
					starts++
				}
				return base(ctx, argv, in)
			}
			m = newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: writeStageBinary})
			host, _ = m.reg.Get(host.Name)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.DeployForOperation(t.Context(), host, facts, false, nil); err == nil || writes != 1 {
				t.Fatalf("first deployment did not fail during short transfer: err=%v writes=%d", err, writes)
			}
			prior := m.pendingRestart(host.Name)
			if !prior.deployment || prior.selected == nil || prior.installTarget != target || prior.writeAcknowledged {
				if selected.stopped {
					t.Errorf("missing retained stopped deployment: %+v", prior)
				} else {
					t.Fatalf("missing retained deployment: %+v", prior)
				}
			}
			if selected.stopped && prior.selected != nil && prior.selected.pid != "0" {
				t.Fatalf("stopped deployment retained a live owner: %+v", prior.selected)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "predecessor" {
				t.Fatalf("short transfer replaced predecessor: data=%q err=%v", data, err)
			}
			if mode == "redirected-recorded-target" {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, target); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "stopped-alias-retarget" {
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, alias); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(mode, "installer-") {
				m.opts.BuildBinary = nil
			}
			var afterRetarget map[string]string
			if strings.HasSuffix(mode, "during-build") {
				m.opts.BuildBinary = func(ctx context.Context, goos, goarch, out string) error {
					retarget := alias
					if mode == "destination-during-build" {
						retarget = target
					}
					if err := os.Remove(retarget); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(other, retarget); err != nil {
						t.Fatal(err)
					}
					afterRetarget = retainedTargetSnapshot(t, root)
					return writeStageBinary(ctx, goos, goarch, out)
				}
			}
			before := retainedTargetSnapshot(t, root)
			if strings.HasPrefix(mode, "endpoint-") {
				saved := fr.runFn
				transport := errors.New("endpoint transport sentinel")
				ctx, cancel := context.WithCancel(t.Context())
				fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
					if strings.Contains(strings.Join(argv[12:], " "), "hub endpoint --json") {
						if mode == "endpoint-schema" {
							return []byte(`{"schema_version":2}`), nil
						}
						if mode == "endpoint-cancelled" {
							cancel()
						}
						return nil, transport
					}
					return saved(ctx, argv, in)
				}
				_, _, failure := m.DeployForOperation(ctx, host, facts, false, nil)
				cancel()
				if mode == "endpoint-schema" {
					if !errors.Is(failure, ErrEndpointMigration) || !isTerminal(failure) {
						t.Errorf("unsupported schema is not terminal migration: %v", failure)
					}
				} else {
					want := transport
					if mode == "endpoint-cancelled" {
						want = context.Canceled
					}
					if !errors.Is(failure, want) || errors.Is(failure, ErrEndpointMigration) || isTerminal(failure) {
						t.Errorf("endpoint failure became terminal: %v", failure)
					}
				}
				if writes != 1 || starts != 0 || !reflect.DeepEqual(prior, m.pendingRestart(host.Name)) || !reflect.DeepEqual(before, retainedTargetSnapshot(t, root)) {
					t.Fatalf("endpoint failure changed retained deployment: writes=%d starts=%d pending=%+v", writes, starts, m.pendingRestart(host.Name))
				}
				fr.runFn = saved
			}
			got, after, err := m.DeployForOperation(t.Context(), host, facts, false, nil)
			if strings.HasSuffix(mode, "during-build") {
				if afterRetarget == nil || !reflect.DeepEqual(afterRetarget, retainedTargetSnapshot(t, root)) {
					t.Errorf("push changed an installation after build-time retarget: writes=%d err=%v", writes, err)
				}
			}
			if strings.HasPrefix(mode, "installer-") {
				if !reflect.DeepEqual(before, retainedTargetSnapshot(t, root)) {
					outsideBytes, outsideErr := os.ReadFile(outside)
					t.Errorf("retained installer changed files: installers=%d outside-target=%q read-error=%v", installers, outsideBytes, outsideErr)
				}
				if !Terminal(err) || !strings.Contains(fmt.Sprint(err), "installer fallback") {
					t.Errorf("retained installer must refuse terminally with a clear cause: %v", err)
				}
			}
			otherBytes, readErr := os.ReadFile(other)
			if readErr != nil || string(otherBytes) != "other-installation" {
				t.Errorf("retained retry overwrote another installation: data=%q err=%v", otherBytes, readErr)
			}
			refuse := mode != "honest-push" && mode != "stopped-honest-push" && !strings.HasPrefix(mode, "endpoint-")
			if refuse {
				if !errors.Is(err, errOwnerSelectionMismatch) || writes != 1 || installers != 0 || !reflect.DeepEqual(prior, m.pendingRestart(host.Name)) || !reflect.DeepEqual(recorded, []string{target}) {
					t.Fatalf("retained retry escaped recorded install target: err=%v writes=%d installers=%d recorded=%q pending=%+v", err, writes, installers, recorded, m.pendingRestart(host.Name))
				}
				if mode == "redirected-recorded-target" || mode == "destination-during-build" {
					if link, err := os.Readlink(target); err != nil || link != other {
						t.Fatalf("refusal replaced redirected target: link=%q err=%v", link, err)
					}
				} else if data, err := os.ReadFile(target); err != nil || string(data) != "predecessor" {
					t.Fatalf("refused installer changed predecessor: data=%q err=%v", data, err)
				}
			} else {
				wantWrites, wantInstallers := 2, 0
				if err != nil || got != launch || after.Version != "newsha" || writes != wantWrites || installers != wantInstallers || m.pendingRestart(host.Name).active() || !reflect.DeepEqual(recorded, []string{target, target}) {
					t.Fatalf("honest retained retry failed: launch=%q err=%v writes=%d installers=%d recorded=%q pending=%+v", got, err, writes, installers, recorded, m.pendingRestart(host.Name))
				}
			}
			if m.resolvedTarget(host.Name) != launch || starts != 0 || len(fr.recordedStarts()) != 0 {
				t.Fatalf("retry changed launch intent or started a process: resolved=%q starts=%d", m.resolvedTarget(host.Name), starts)
			}
			link, linkErr := os.Readlink(alias)
			wantLink := target
			if mode == "alias-during-build" || mode == "stopped-alias-retarget" {
				wantLink = other
			}
			if linkErr != nil || link != wantLink {
				t.Fatalf("retry changed launch symlink: link=%q err=%v", link, linkErr)
			}
		})
	}
}

func TestOrdinaryPushTargetDuringBuild(t *testing.T) {
	for _, mode := range []string{"honest-push", "alias-during-build", "destination-during-build"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			target, alias, other := filepath.Join(root, "evener"), filepath.Join(root, "launch", "evener"), filepath.Join(root, "other")
			if err := os.Mkdir(filepath.Dir(alias), 0o755); err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string]string{target: "predecessor", other: "other-installation"} {
				if err := os.WriteFile(name, []byte(data), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, alias); err != nil {
				t.Fatal(err)
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: alias}
			writes, recorded := 0, 0
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				switch remote {
				case "test -d " + shellquote.RemoteWord(filepath.Dir(alias)), resolveDeployCommand(alias):
				case pushBinaryRemote(target, int64(len("staged-binary"))):
					writes++
				default:
					t.Fatalf("unexpected command: %s", remote)
				}
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
				cmd.Stdin = in
				return cmd.Output()
			}}
			var afterBuild map[string]string
			build := func(ctx context.Context, goos, goarch, out string) error {
				if mode != "honest-push" {
					retarget := alias
					if mode == "destination-during-build" {
						retarget = target
					}
					if err := os.Remove(retarget); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(other, retarget); err != nil {
						t.Fatal(err)
					}
				}
				afterBuild = retainedTargetSnapshot(t, root)
				return writeStageBinary(ctx, goos, goarch, out)
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{BuildBinary: build})
			got, err := m.deployPush(t.Context(), host, Preflight{OS: "linux", Arch: "amd64"}, func(string) error { recorded++; return nil })
			if mode == "honest-push" {
				data, readErr := os.ReadFile(target)
				if err != nil || got != target || writes != 1 || recorded != 1 || readErr != nil || string(data) != "staged-binary" {
					t.Fatalf("honest push: target=%q writes=%d recorded=%d data=%q err=%v readErr=%v", got, writes, recorded, data, err, readErr)
				}
			} else if !errors.Is(err, errOwnerSelectionMismatch) || writes != 0 || recorded != 0 || !reflect.DeepEqual(afterBuild, retainedTargetSnapshot(t, root)) {
				t.Fatalf("retargeted ordinary push was not refused before effects: err=%v writes=%d recorded=%d unchanged=%v", err, writes, recorded, reflect.DeepEqual(afterBuild, retainedTargetSnapshot(t, root)))
			}
		})
	}
}

func TestRetainedInstallerFallbackRefusesBeforeRecording(t *testing.T) {
	for _, source := range []string{"no-build-source", "cross-target-own-executable"} {
		t.Run(source, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/prefix/installed/evener"}
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				t.Errorf("retained installer refusal issued a remote command: %q", argv)
				return nil, errors.New("unexpected command")
			}}
			opts := Options{controllerVersionOverride: "newsha"}
			if source == "cross-target-own-executable" {
				opts.BuildBinary, opts.OwnExecutable = writeStageBinary, true
			}
			m := newTestManager(t, testRegistry(t, host), fr, opts)
			recorded := 0
			_, err := m.deploy(t.Context(), host, Preflight{OS: "other-os", Arch: "other-arch"}, func(string) error {
				recorded++
				return nil
			}, host.EvenerPath)
			if !Terminal(err) || !strings.Contains(fmt.Sprint(err), "installer fallback") || recorded != 0 || len(fr.recordedRuns()) != 0 {
				t.Fatalf("retained installer dispatch was not refused before effects: err=%v recorded=%d commands=%q", err, recorded, fr.recordedRuns())
			}
		})
	}
}

// Only curl is substituted. The real installer verifies/extracts the archive,
// installs into share_bindir and replaces the launch path with a symlink.
func retainedInstallerFixture(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	archiveRoot := "evener_" + runtime.GOOS + "_" + runtime.GOARCH
	if err := os.Mkdir(filepath.Join(dir, archiveRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, archiveRoot, "evener"), []byte("staged-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, archiveRoot+".tar.gz")
	if out, err := exec.Command("tar", "-czf", archive, "-C", dir, archiveRoot).CombinedOutput(); err != nil {
		t.Fatalf("create installer archive: %v: %s", err, out)
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), fmt.Appendf(nil, "%x  %s.tar.gz\n", sha256.Sum256(data), archiveRoot), 0o644); err != nil {
		t.Fatal(err)
	}
	base := "https://github.com/prime-radiant-inc/evener/releases/download/snapshot/"
	curl := "#!/bin/sh\nset -eu\n" +
		"if [ \"$#\" = 6 ] && [ \"$1\" = -fsSL ] && [ \"$2\" = -w ] && [ \"$3\" = '%{http_code}' ] && [ \"$4\" = " + shellquote.RemoteWord(base+archiveRoot+".tar.gz") + " ] && [ \"$5\" = -o ]; then\n" +
		"cp " + shellquote.RemoteWord(archive) + " \"$6\"; printf 200\n" +
		"elif [ \"$#\" = 4 ] && [ \"$1\" = -fsSL ] && [ \"$2\" = " + shellquote.RemoteWord(base+"checksums.txt") + " ] && [ \"$3\" = -o ]; then\n" +
		"cp " + shellquote.RemoteWord(filepath.Join(dir, "checksums.txt")) + " \"$4\"\n" +
		"else echo 'unexpected fixture curl arguments' >&2; exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"HOME=" + dir, "TMPDIR=" + dir, "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")}
}

func retainedTargetSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	got := make(map[string]string)
	if err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		if info.Mode().IsRegular() {
			data, err = os.ReadFile(name)
		} else if info.Mode()&os.ModeSymlink != 0 {
			var link string
			link, err = os.Readlink(name)
			data = []byte(link)
		}
		got[name] = fmt.Sprintf("%s:%s", info.Mode(), data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}
