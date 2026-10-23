package sshconn

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// Process/service evidence uses the existing selected-owner model. Every install
// path lookup and write crosses a real local shell and real symlink instead.
func TestRetainedSymlinkInstallTarget(t *testing.T) {
	for _, mode := range []string{"nonexecutable", "retargeted", "retargeted-at-write"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"bin", "physical", "other"} {
				if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			target, other := filepath.Join(root, "physical", "evener"), filepath.Join(root, "other", "evener")
			for _, file := range []string{target, other} {
				if err := os.WriteFile(file, []byte("old-binary"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			launch := filepath.Join(root, "bin", "evener")
			if err := os.Symlink(target, launch); err != nil {
				t.Fatal(err)
			}
			repair := false
			writes, starts := 0, 0
			retarget := func() {
				t.Helper()
				if err := os.Remove(launch); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, launch); err != nil {
					t.Fatal(err)
				}
			}
			fr, selected := deployRunnerForFixture(t, "linux", "amd64", func(int) ([]byte, error) { return launchContract("newsha"), nil }, func(int) ([]byte, error) {
				return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
			})
			selected.exe = launch
			base := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if strings.Contains(remote, "systemctl start") || strings.Contains(remote, "systemctl restart") || strings.Contains(remote, "nohup ") {
					starts++
				}
				if strings.Contains(remote, "cat >") {
					writes++
					if !repair {
						return nil, errors.New("interrupted recorded write")
					}
				}
				if strings.Contains(remote, "evener_resolve") || strings.HasPrefix(remote, "test -d ") || strings.HasPrefix(remote, "if test -x ") || strings.Contains(remote, "cat >") {
					cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
					cmd.Stdin = in
					return cmd.Output()
				}
				return base(ctx, argv, in)
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: launch}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: func(ctx context.Context, goos, goarch, file string) error {
				if repair && mode == "retargeted-at-write" {
					retarget()
				}
				return writeStageBinary(ctx, goos, goarch, file)
			}})
			host, _ = m.reg.Get(host.Name)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			intent := m.deploymentIntent(false, hubIdentity{})
			intent.binding = newEffectBinding(host, func() error { return nil })
			if _, _, err := m.deployForOperation(t.Context(), host, facts, intent); err == nil {
				t.Fatal("initial deployment did not retain interrupted write")
			}
			retained := m.pendingRestart(host.Name)
			if retained.selected == nil || !retained.deployment || writes != 1 {
				t.Fatalf("missing real retained deployment: writes=%d pending=%+v", writes, retained)
			}
			canonical, err := filepath.EvalSymlinks(launch)
			if err != nil || retained.installTarget != canonical || retained.selected.host.EvenerPath != launch {
				t.Fatalf("install/launch identity not retained separately: target=%q launch=%q canonical=%q err=%v", retained.installTarget, retained.selected.host.EvenerPath, canonical, err)
			}
			if err := os.Chmod(target, 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "retargeted" {
				retarget()
			}
			if err := os.Chmod(other, 0600); err != nil {
				t.Fatal(err)
			}
			repair = true
			_, _, err = m.deployForOperation(t.Context(), host, Preflight{Host: host.Name, OS: "linux", Arch: "amd64", UID: "1000", ExecutableMissing: true}, retained)
			data, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "nonexecutable" {
				if err != nil || writes != 2 || string(data) != "staged-binary" || m.pendingRestart(host.Name).active() {
					t.Fatalf("repair err=%v writes=%d target=%q pending=%+v", err, writes, data, m.pendingRestart(host.Name))
				}
				info, err := os.Stat(target)
				if err != nil || info.Mode()&0111 == 0 {
					t.Fatalf("target not executable: %v %v", info, err)
				}
				link, err := os.Readlink(launch)
				if err != nil || link != target {
					t.Fatalf("launch symlink changed: %q %v", link, err)
				}
			} else if err == nil || writes != 1 || string(data) != "old-binary" || !m.pendingRestart(host.Name).deployment {
				t.Fatalf("retarget refusal err=%v writes=%d target=%q pending=%+v", err, writes, data, m.pendingRestart(host.Name))
			}
			if starts != 0 || len(fr.recordedStarts()) != 0 {
				t.Fatal("deploy-only recovery started or attached a hub")
			}
		})
	}
}
