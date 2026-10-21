package sshconn

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
)

func TestRetainedDiscoveredSymlinkDeployment(t *testing.T) {
	for _, mode := range []string{"interrupted-write", "lost-reply", "changed-registration", "retained-reinstall-restart", "dangling-target", "dangling-other-target", "dangling-cycle", "dangling-retarget-at-write"} {
		t.Run(mode, func(t *testing.T) {
			dangling := strings.HasPrefix(mode, "dangling-")
			restart := mode == "retained-reinstall-restart" || dangling
			refuseDangling := mode == "dangling-other-target" || mode == "dangling-cycle" || mode == "dangling-retarget-at-write"
			root := t.TempDir()
			target, alias := filepath.Join(root, "installed", "evener"), filepath.Join(root, "bin", "evener")
			for _, dir := range []string{filepath.Dir(target), filepath.Dir(alias)} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(target, []byte("predecessor"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, alias); err != nil {
				t.Fatal(err)
			}
			linkTarget := target
			if dangling {
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				linkTarget = "../installed/launch"
				if err := os.Symlink(linkTarget, alias); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("evener", filepath.Join(root, "installed", "launch")); err != nil {
					t.Fatal(err)
				}
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
			writes, discoveries, guardCalls, starts, procReads := 0, 0, 0, 0, 0
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
				if starts != 0 {
					return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:01Z"}`), nil
				}
				return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
			})
			selected.exe = alias
			selected.endpointAlias = target + " hub endpoint --json"
			lostReply := errors.New("push completed, reply lost")
			base := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				run := func(input io.Reader) ([]byte, error) {
					cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
					cmd.Stdin = input
					return cmd.Output()
				}
				switch remote {
				case "command -v evener || echo " + evenerPathMissingMarker:
					discoveries++
					return []byte(alias + "\n"), nil
				case resolveDeployCommand(alias), resolveRetainedDeployCommand(alias), resolveDeployCommand(target), createTargetCommand(target), "test -d " + shellquote.RemoteWord(filepath.Dir(alias)), "test -d " + shellquote.RemoteWord(filepath.Dir(target)):
					return run(in)
				case "if test -x " + shellquote.RemoteWord(alias) + "; then exit 0; else exit 1; fi", "if test -x " + shellquote.RemoteWord(target) + "; then exit 0; else exit 1; fi":
					return run(in)
				}
				if strings.Contains(remote, "cat >") {
					writes++
					if remote != pushBinaryRemote(target, int64(len("staged-binary"))) || guardCalls < writes {
						t.Fatalf("write escaped canonical target or recorder guard: %q, guard=%d writes=%d", remote, guardCalls, writes)
					}
					if writes == 1 && mode != "lost-reply" {
						// A real short transfer must fail its byte-count check before rename.
						return run(io.LimitReader(in, 3))
					}
					out, err := run(in)
					if err == nil {
						selected.executableAbsent = false
					}
					if writes == 1 && err == nil {
						return out, lostReply
					}
					return out, err
				}
				if strings.HasPrefix(remote, "systemctl restart ") || strings.HasPrefix(remote, "systemctl start ") {
					starts++
				}
				if strings.Contains(remote, "/proc/4242/exe hub endpoint --json") {
					procReads++
				}
				return base(ctx, argv, in)
			}
			build := func(ctx context.Context, goos, goarch, out string) error {
				if mode == "dangling-retarget-at-write" && selected.executableAbsent {
					chain := filepath.Join(root, "installed", "launch")
					if err := os.Remove(chain); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("other-evener", chain); err != nil {
						t.Fatal(err)
					}
				}
				return writeStageBinary(ctx, goos, goarch, out)
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: build})
			host, _ = m.reg.Get(host.Name)
			guard := func() error { guardCalls++; return nil }
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.DeployForOperation(t.Context(), host, facts, restart, guard); err == nil || writes != 1 {
				t.Fatalf("first push did not fail at its write: err=%v writes=%d", err, writes)
			}
			retained := m.pendingRestart(host.Name)
			if !retained.deployment || retained.writeAcknowledged || retained.selected == nil || retained.selected.host.EvenerPath != alias || retained.installTarget != target || discoveries == 0 {
				t.Fatalf("discovered launch/write intent not retained: discoveries=%d pending=%+v", discoveries, retained)
			}
			data, err := os.ReadFile(target)
			wantFirst := "predecessor"
			if mode == "lost-reply" {
				wantFirst = "staged-binary"
			}
			if err != nil || string(data) != wantFirst {
				t.Fatalf("first write boundary: data=%q err=%v", data, err)
			}
			if mode == "changed-registration" {
				host.SSH = "different.example"
				if err := m.reg.Update(host); err != nil {
					t.Fatal(err)
				}
				host, _ = m.reg.Get(host.Name)
			}
			facts, err = m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if restart {
				if !retained.restart {
					t.Fatalf("missing pending selected restart: %+v", retained)
				}
				if err := os.Chmod(target, 0o644); err != nil {
					t.Fatal(err)
				}
				if dangling {
					if err := os.Remove(target); err != nil {
						t.Fatal(err)
					}
					if refuseDangling && mode != "dangling-retarget-at-write" {
						chain := filepath.Join(root, "installed", "launch")
						if err := os.Remove(chain); err != nil {
							t.Fatal(err)
						}
						other := "other-evener"
						if mode == "dangling-cycle" {
							other = "../bin/evener"
						}
						if err := os.Symlink(other, chain); err != nil {
							t.Fatal(err)
						}
					}
				}
				selected.executableAbsent = true
				facts.ExecutableMissing = true
			}
			discoveredBeforeRetry := discoveries
			launch, after, err := m.DeployForOperation(t.Context(), host, facts, restart, guard)
			if mode == "changed-registration" || refuseDangling {
				if err == nil || writes != 1 || !reflect.DeepEqual(retained, m.pendingRestart(host.Name)) {
					t.Fatalf("changed registration replayed write or lost intent: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
				}
				if refuseDangling && !errors.Is(err, errOwnerSelectionMismatch) {
					t.Fatalf("dangling chain refused for the wrong reason: %v", err)
				}
			} else if restart {
				if err != nil || writes != 2 || procReads < 2 || after.Version != "newsha" || after.HubEndpointSchema != 1 {
					t.Fatalf("retained reinstall: err=%v writes=%d proc=%d after=%+v", err, writes, procReads, after)
				}
				if launch != alias || m.resolvedTarget(host.Name) != alias {
					t.Errorf("retained reinstall published canonical target: launch=%q resolved=%q want alias=%q", launch, m.resolvedTarget(host.Name), alias)
				}
				pending := m.pendingRestart(host.Name)
				if !pending.writeAcknowledged || pending.installTarget != target || pending.selected == nil || pending.selected.host.EvenerPath != alias || pending.command != retained.command || !pending.restart {
					t.Fatalf("reinstall lost selected restart intent: %+v", pending)
				}
				version, err := m.RestartForOperation(t.Context(), host, after, guard)
				if err != nil || version != "newsha" || starts != 1 || m.pendingRestart(host.Name).active() {
					t.Fatalf("reinstalled alias restart: err=%v version=%q starts=%d pending=%+v", err, version, starts, m.pendingRestart(host.Name))
				}
			} else if err != nil || writes != 2 || launch != alias || m.resolvedTarget(host.Name) != alias || after.Version != "newsha" || after.HubEndpointSchema != 1 || m.pendingRestart(host.Name).active() {
				t.Fatalf("retained discovered launch recovery: err=%v launch=%q resolved=%q writes=%d after=%+v pending=%+v", err, launch, m.resolvedTarget(host.Name), writes, after, m.pendingRestart(host.Name))
			}
			data, readErr := os.ReadFile(target)
			link, linkErr := os.Readlink(alias)
			want := "staged-binary"
			if mode == "changed-registration" {
				want = "predecessor"
			}
			wantStarts := 0
			if restart && !refuseDangling {
				wantStarts = 1
			}
			if refuseDangling {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("refused chain created recorded target: data=%q err=%v", data, readErr)
				}
				if _, err := os.Lstat(filepath.Join(root, "installed", "other-evener")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("refused chain wrote a different target: %v", err)
				}
				if mode == "dangling-retarget-at-write" && procReads == 0 {
					t.Fatal("retarget-at-write did not reach staging after the first owner check")
				}
			} else if readErr != nil || string(data) != want {
				t.Fatalf("recovery target: data=%q err=%v want=%q", data, readErr, want)
			}
			if linkErr != nil || link != linkTarget || starts != wantStarts || len(fr.recordedStarts()) != 0 || discoveries != discoveredBeforeRetry {
				t.Fatalf("recovery effects: data=%q err=%v link=%q err=%v starts=%d discoveries=%d/%d", data, readErr, link, linkErr, starts, discoveries, discoveredBeforeRetry)
			}
		})
	}
}
