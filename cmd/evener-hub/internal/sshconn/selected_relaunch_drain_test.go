package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Config inspection and pathname resolution execute production commands against
// real TOML, directories and executable symlinks. Process exit and launches are
// modeled: no remote service or child hub is signaled or started.
func TestAdhocRelaunchRevalidatesAfterDrain(t *testing.T) {
	resolver := buildConfigIdentityResolver(t)
	for _, drift := range []string{"unchanged", "root", "cwd", "executable"} {
		t.Run(drift, func(t *testing.T) {
			dir := t.TempDir()
			physical, other := filepath.Join(dir, "physical"), filepath.Join(dir, "other")
			cwd, launch := physical, filepath.Join(dir, "evener")
			target, otherTarget := filepath.Join(dir, "original"), filepath.Join(dir, "replacement")
			config, root := filepath.Join(dir, "hub.toml"), filepath.Join(dir, "state")
			for _, d := range []string{physical, other} {
				if err := os.Mkdir(d, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range []string{target, otherTarget} {
				data := "#!/bin/sh\nexec " + shellquote.RemoteWord(resolver) + " \"$@\"\n"
				if err := os.WriteFile(p, []byte(data), 0700); err != nil {
					t.Fatal(err)
				}
			}
			link := func(name, to string) {
				if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if err := os.Symlink(to, name); err != nil {
					t.Fatal(err)
				}
			}
			writeConfig := func(state string) {
				if err := os.WriteFile(config, fmt.Appendf(nil, "addr = \"127.0.0.1:9180\"\nhub_state_root = %q\n", state), 0600); err != nil {
					t.Fatal(err)
				}
			}
			link(launch, target)
			writeConfig(root)
			host := hostreg.Host{Name: "alpha", SSH: "dev@fixture.invalid", EvenerPath: launch, ConfigPath: config}
			kills, launches, exits := 0, 0, 0
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}}
			f := modelSelectedProcess(t, fr, launch, "dev", "dev")
			f.adhoc = true
			base := fr.runFn
			argv := []string{launch, "hub", "--config", config}
			env := []string{"HOME=/home/dev"}
			relaunch := ownerContext(cwd, env) + relaunchCommand(argv, "")
			shell := func(ctx context.Context, remote string) ([]byte, error) {
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
				cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/home/dev"}
				return cmd.Output()
			}
			fr.runFn = func(ctx context.Context, args []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(args[12:], " ")
				switch {
				case remote == "kill -- 4242":
					kills++
					f.stopped = true
					return nil, nil
				case remote == processAliveRemote("4242"):
					exits++
					// Mutate only after the last live gate, during graceful drain.
					switch drift {
					case "root":
						writeConfig(filepath.Join(dir, "other-state"))
					case "cwd":
						if err := os.Rename(cwd, filepath.Join(dir, "saved-cwd")); err != nil {
							t.Fatal(err)
						}
						link(cwd, other)
					case "executable":
						link(launch, otherTarget)
					}
					return []byte(pidGoneMarker), nil
				case remote == relaunch:
					launches++
					f.stopped = false
					f.mutated = true
					return nil, nil
				case strings.HasPrefix(remote, launch+" launch-check "):
					return []byte(goodLaunchCheck), nil
				case strings.Contains(remote, " hub endpoint --json"):
					out, err := shell(ctx, remote)
					if err != nil {
						return out, err
					}
					var report hubendpoint.Report
					if err := json.Unmarshal(out, &report); err != nil {
						return nil, err
					}
					if !f.stopped {
						if f.mutated {
							f.successorPolls++
						}
						modeled := remoteEndpointFixture(config, "127.0.0.1:9180", "boot-one")
						if f.mutated {
							modeled.Running.BootID = "boot-two"
							modeled.Running.PID = 4343
							modeled.Running.StartedAt = modeled.Running.StartedAt.Add(time.Second)
						}
						report.State = "running"
						report.Running = modeled.Running
						report.Running.ConfigPath = config
						report.Running.HubStateRoot = report.HubStateRoot
						report.Running.Version = "dev"
					}
					return json.Marshal(report)
				case remote == resolveDeployCommand(launch), remote == resolveDeployCommand(target), remote == resolveDeployCommand(otherTarget), strings.HasSuffix(remote, "pwd -P"):
					return shell(ctx, remote)
				case remote == "readlink /proc/4242/cwd", remote == "readlink /proc/4343/cwd":
					return []byte(cwd + "\n"), nil
				case remote == hubArgvRemote("4242"), remote == hubArgvRemote("4343"):
					return []byte(strings.Join(argv, "\x00") + "\x00"), nil
				case remote == selectedLockRemote(root):
					translated := append([]string(nil), args[:12]...)
					translated = append(translated, selectedLockRemote("/home/dev/.local/state/evener"))
					return base(ctx, translated, in)
				case remote == "lsof -p 4242 -a -d 1,2":
					return nil, nil
				}
				return base(ctx, args, in)
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "dev", sleep: func(context.Context, time.Duration) error { return nil }})
			host, _ = m.reg.Get(host.Name)
			_, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil)
			if drift == "unchanged" {
				if err != nil || kills != 1 || launches != 1 || exits != 1 || m.pendingRestart(host.Name).active() {
					t.Fatalf("ordinary restart: err=%v kills=%d launches=%d exits=%d pending=%+v", err, kills, launches, exits, m.pendingRestart(host.Name))
				}
				return
			}
			if !errors.Is(err, errOwnerEvidence) || kills != 1 || launches != 0 || exits != 1 || !f.stopped || !m.pendingRestart(host.Name).active() {
				t.Fatalf("changed stopped launch was dispatched: err=%v kills=%d launches=%d exits=%d stopped=%t pending=%+v", err, kills, launches, exits, f.stopped, m.pendingRestart(host.Name))
			}
			retained := m.pendingRestart(host.Name)
			// A retry while drift persists must not update the frozen intent.
			_, err = m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil)
			if err == nil || launches != 0 || kills != 1 || !reflect.DeepEqual(retained, m.pendingRestart(host.Name)) {
				t.Fatalf("retry lost intent: err=%v kills=%d launches=%d pending=%+v", err, kills, launches, m.pendingRestart(host.Name))
			}
			writeConfig(root)
			if drift == "cwd" {
				if err := os.Remove(cwd); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(dir, "saved-cwd"), cwd); err != nil {
					t.Fatal(err)
				}
			}
			link(launch, target)
			_, err = m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil)
			if err != nil || kills != 1 || launches != 1 || m.pendingRestart(host.Name).active() {
				t.Fatalf("restored retry: err=%v kills=%d launches=%d pending=%+v", err, kills, launches, m.pendingRestart(host.Name))
			}
		})
	}
}
