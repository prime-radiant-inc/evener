package sshconn

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/remoteinstall"
)

// Target resolution, short-transfer rejection and atomic push writes use the real
// filesystem. Process, systemd and installer observations are modeled, not SSH.
func TestRetainedDeploymentInstallTargetBoundary(t *testing.T) {
	channel, dirty, tag := buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = channel, dirty, tag })
	buildinfo.Channel, buildinfo.GitDirty, buildinfo.ReleaseTag = "snapshot", "", ""
	for _, mode := range []string{"honest-push", "redirected-recorded-target", "installer-different-target", "installer-same-target"} {
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
			if mode == "installer-same-target" {
				launch = target
			}
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
					// Do not execute an installer that would fetch external artifacts.
					return nil, os.WriteFile(target, []byte("staged-binary"), 0o755)
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
				t.Fatalf("missing retained deployment: %+v", prior)
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
			if strings.HasPrefix(mode, "installer-") {
				m.opts.BuildBinary = nil
			}
			got, after, err := m.DeployForOperation(t.Context(), host, facts, false, nil)
			otherBytes, readErr := os.ReadFile(other)
			if readErr != nil || string(otherBytes) != "other-installation" {
				t.Errorf("retained retry overwrote another installation: data=%q err=%v", otherBytes, readErr)
			}
			refuse := mode == "redirected-recorded-target" || mode == "installer-different-target"
			if refuse {
				if !errors.Is(err, errOwnerSelectionMismatch) || writes != 1 || installers != 0 || !reflect.DeepEqual(prior, m.pendingRestart(host.Name)) || !reflect.DeepEqual(recorded, []string{target}) {
					t.Fatalf("retained retry escaped recorded install target: err=%v writes=%d installers=%d recorded=%q pending=%+v", err, writes, installers, recorded, m.pendingRestart(host.Name))
				}
				if mode == "redirected-recorded-target" {
					if link, err := os.Readlink(target); err != nil || link != other {
						t.Fatalf("refusal replaced redirected target: link=%q err=%v", link, err)
					}
				} else if data, err := os.ReadFile(target); err != nil || string(data) != "predecessor" {
					t.Fatalf("refused installer changed predecessor: data=%q err=%v", data, err)
				}
			} else {
				wantWrites, wantInstallers := 2, 0
				if mode == "installer-same-target" {
					wantWrites, wantInstallers = 1, 1
				}
				if err != nil || got != launch || after.Version != "newsha" || writes != wantWrites || installers != wantInstallers || m.pendingRestart(host.Name).active() || !reflect.DeepEqual(recorded, []string{target, target}) {
					t.Fatalf("honest retained retry failed: launch=%q err=%v writes=%d installers=%d recorded=%q pending=%+v", got, err, writes, installers, recorded, m.pendingRestart(host.Name))
				}
			}
			if m.resolvedTarget(host.Name) != launch || starts != 0 || len(fr.recordedStarts()) != 0 {
				t.Fatalf("retry changed launch intent or started a process: resolved=%q starts=%d", m.resolvedTarget(host.Name), starts)
			}
			link, linkErr := os.Readlink(alias)
			if linkErr != nil || link != target {
				t.Fatalf("retry changed launch symlink: link=%q err=%v", link, linkErr)
			}
		})
	}
}
