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
	for _, mode := range []string{"interrupted-write", "lost-reply", "changed-registration"} {
		t.Run(mode, func(t *testing.T) {
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
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
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
			selected.exe = alias
			selected.endpointAlias = target + " hub endpoint --json"
			writes, discoveries, guardCalls, starts := 0, 0, 0, 0
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
				case resolveDeployCommand(alias), resolveDeployCommand(target), createTargetCommand(target), "test -d " + shellquote.RemoteWord(filepath.Dir(alias)), "test -d " + shellquote.RemoteWord(filepath.Dir(target)):
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
					if writes == 1 && err == nil {
						return out, lostReply
					}
					return out, err
				}
				if strings.HasPrefix(remote, "systemctl restart ") || strings.HasPrefix(remote, "systemctl start ") {
					starts++
				}
				return base(ctx, argv, in)
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: writeStageBinary})
			host, _ = m.reg.Get(host.Name)
			guard := func() error { guardCalls++; return nil }
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.DeployForOperation(t.Context(), host, facts, false, guard); err == nil || writes != 1 {
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
			discoveredBeforeRetry := discoveries
			launch, after, err := m.DeployForOperation(t.Context(), host, facts, false, guard)
			if mode == "changed-registration" {
				if err == nil || writes != 1 || !reflect.DeepEqual(retained, m.pendingRestart(host.Name)) {
					t.Fatalf("changed registration replayed write or lost intent: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
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
			if readErr != nil || string(data) != want || linkErr != nil || link != target || starts != 0 || len(fr.recordedStarts()) != 0 || discoveries != discoveredBeforeRetry {
				t.Fatalf("recovery effects: data=%q err=%v link=%q err=%v starts=%d discoveries=%d/%d", data, readErr, link, linkErr, starts, discoveries, discoveredBeforeRetry)
			}
		})
	}
}
