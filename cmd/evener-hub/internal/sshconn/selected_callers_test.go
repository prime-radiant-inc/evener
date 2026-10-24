package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestEnsureSelectedConfigOnly(t *testing.T) {
	for _, defaultPath := range []bool{false, true} {
		t.Run(fmt.Sprintf("default-path=%v", defaultPath), func(t *testing.T) {
			host, _ := ownerCommands(t)
			exe := "/opt/evener"
			if defaultPath {
				host.EvenerPath = ""
				exe = "evener"
			}
			report := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
			report.Running.Version = "dev"
			commands := []ownerCommand{{"uname -s", "Linux\n"}, {"uname -m", "x86_64\n"}, {envProbeScript, "HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"}, {"id -u", "1000\n"}, {exe + " launch-check --json", goodLaunchCheck}, {exe + " hub endpoint --json --config /srv/selected.toml", mustOwnerJSON(t, report)}}
			fr, index := ownerRunner(t, commands)
			fr.startFn = func(_ context.Context, argv []string, _ io.Writer) (Stdio, error) {
				if got := strings.Join(argv[12:], " "); got != exe+" hub attach --stdio --config /srv/selected.toml" {
					t.Fatalf("attach = %s", got)
				}
				return newFakeBridge(appwire.ProtocolVersion).stdio, nil
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			ch, err := m.Ensure(t.Context(), host.Name)
			if err != nil {
				t.Fatal(err)
			}
			if ch == nil || *index != len(commands) || len(fr.recordedStarts()) != 1 {
				t.Fatalf("channel=%v commands=%d/%d", ch, *index, len(commands))
			}
		})
	}
}

func selectedPreflightCommands(version string) []ownerCommand {
	return []ownerCommand{{"uname -s", "Linux\n"}, {"uname -m", "x86_64\n"}, {envProbeScript, "HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"}, {"id -u", "1000\n"}, {"/opt/evener launch-check --json", strings.ReplaceAll(goodLaunchCheck, `"version":"dev"`, `"version":"`+version+`"`)}}
}

func TestEnsureSelectedDeployPreservesRunTarget(t *testing.T) {
	const link = "/usr/local/bin/evener"
	const target = "/opt/evener/bin/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: link}
	launchCalls, pushes := 0, 0
	fr := &fakeRunner{startFn: goodStartFn(t)}
	fr.runFn = func(_ context.Context, argv []string, in io.Reader) ([]byte, error) {
		remote := strings.Join(argv[12:], " ")
		switch remote {
		case "uname -s":
			return []byte("Linux\n"), nil
		case "uname -m":
			return []byte("x86_64\n"), nil
		case envProbeScript:
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case "id -u":
			return []byte("1000\n"), nil
		case link + " launch-check --json", link + " launch-check --protocol evener-appwire-v7 --json", target + " launch-check --json", target + " launch-check --protocol evener-appwire-v7 --json":
			version := "oldsha"
			if launchCalls > 0 {
				version = "newsha"
			}
			launchCalls++
			return []byte(strings.ReplaceAll(goodLaunchCheck, `"version":"dev"`, `"version":"`+version+`"`)), nil
		case "test -d /usr/local/bin", "systemctl restart evener-hub.service":
			return nil, nil
		default:
			if remote == pushBinaryRemote(target, int64(len("new binary"))) {
				pushes++
				_, err := io.Copy(io.Discard, in)
				return nil, err
			}
			return nil, fmt.Errorf("undeclared external command: %s", remote)
		}
	}
	modelSelectedProcess(t, fr, link, "oldsha", "newsha")
	base := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
		remote := strings.Join(argv[12:], " ")
		if remote == resolveDeployCommand(link) || remote == resolveDeployCommand(target) {
			return []byte(target + "\n"), nil
		}
		return base(ctx, argv, in)
	}
	requireProtocolUpgrade(t, fr)
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: func(_ context.Context, _, _, out string) error {
		return os.WriteFile(out, []byte("new binary"), 0o755)
	}})
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)
	ch, err := m.Ensure(t.Context(), host.Name)
	if err != nil {
		t.Fatal(err)
	}
	if ch == nil || pushes != 1 || m.pendingRestart(host.Name).selected != nil {
		t.Fatalf("channel=%v pushes=%d pending=%+v", ch, pushes, m.pendingRestart(host.Name))
	}
	starts := fr.recordedStarts()
	if len(starts) != 2 || strings.Join(starts[1][12:], " ") != link+" hub attach --stdio" {
		t.Fatalf("attach commands=%v", starts)
	}
}

func TestEnsureSelectedRestartReattaches(t *testing.T) {
	host, first := ownerCommands(t)
	_, second := ownerCommands(t)
	commands := selectedPreflightCommands("release-selected")
	commands = append(commands, first[0]) // Ensure decision consumes this report.
	commands = append(commands, first...)
	commands = append(commands, second...)
	commands = append(commands, replacementGateCommands(t, second, true)...)
	commands = append(commands, ownerCommand{"systemctl restart evener-hub-selected.service", ""})
	commands = append(commands, selectedSuccessorCommands(t)...)
	commands = append(commands, selectedPreflightCommands("release-selected")[4])
	fr, index := ownerRunner(t, commands)
	fr.startFn = goodStartFn(t)
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "release-selected"})
	// A compatible hub is restarted only under an existing recorded intent.
	m.setPendingRestart(host.Name, "systemctl restart evener-hub-selected.service", "release-selected", "", hubIdentity{}, nil)
	var recorded, finished int
	m.SetEnsureRestartHook(func(got hostreg.Host) (func(error), error) {
		if got.ConfigPath != host.ConfigPath {
			t.Fatal("recorder lost selector")
		}
		recorded++
		return func(err error) {
			if err != nil {
				t.Errorf("record finish: %v", err)
			}
			finished++
		}, nil
	})
	ch, err := m.Ensure(t.Context(), host.Name)
	if err != nil {
		t.Fatal(err)
	}
	if ch == nil || recorded != 1 || finished != 1 || *index != len(commands) || m.pendingRestart(host.Name).selected != nil || len(fr.recordedStarts()) != 1 {
		t.Fatalf("recorded=%d finished=%d commands=%d/%d", recorded, finished, *index, len(commands))
	}
}

func TestEnsureSelectedStoppedReplacement(t *testing.T) {
	host, first := ownerCommands(t)
	_, second := ownerCommands(t)
	commands := first
	commands = append(commands, second...)
	commands = append(commands, replacementGateCommands(t, second, true)...)
	commands = append(commands, ownerCommand{"systemctl restart evener-hub-selected.service", ""})
	failAt := len(commands)
	stopped := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
	stopped.State, stopped.Running = "stopped", nil
	raw := mustOwnerJSON(t, stopped)
	commands = append(commands, selectedPreflightCommands("release-selected")...)
	commands = append(commands, ownerCommand{first[0].command, raw}, ownerCommand{first[0].command, raw}, ownerCommand{first[9].command, raw}, first[10], first[11], ownerCommand{first[13].command, strings.ReplaceAll(first[13].output, "MainPID=4242", "MainPID=0")}, first[15], ownerCommand{first[0].command, raw})
	commands = append(commands, replacementGateCommands(t, identityOwnerCommands(t, "linux", true), false)...)
	commands = append(commands, ownerCommand{"systemctl start evener-hub-selected.service", ""})
	commands = append(commands, selectedSuccessorCommands(t)...)
	commands = append(commands, selectedPreflightCommands("release-selected")[4])
	fr, index := ownerRunner(t, commands)
	run := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
		out, err := run(ctx, argv, in)
		if *index == failAt {
			return out, errReplacementFixture
		}
		return out, err
	}
	fr.startFn = goodStartFn(t)
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "release-selected"})
	testRestartRecorder(t, m)
	host, _ = m.reg.Get(host.Name)
	if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); !errors.Is(err, errReplacementFixture) {
		t.Fatalf("first replacement=%v", err)
	}
	if _, err := m.Ensure(t.Context(), host.Name); err != nil {
		t.Fatal(err)
	}
	if *index != len(commands) || m.pendingRestart(host.Name).selected != nil || len(fr.recordedStarts()) != 1 {
		t.Fatalf("commands=%d/%d pending=%+v", *index, len(commands), m.pendingRestart(host.Name))
	}
}

func TestDeploySelectedOldImageBeforeFirstWrite(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprintf("Ensure=%v", automatic), func(t *testing.T) {
			host, commands := ownerCommands(t)
			damaged := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
			damaged.State, damaged.Reason, damaged.Running = "unavailable", "unreachable", nil
			commands[0].output = mustOwnerJSON(t, damaged)
			commands = append(commands[:16], ownerCommand{"/proc/4242/exe launch-check --json", `{"protocol":"old","version":"old"}`})
			if automatic {
				prefix := selectedPreflightCommands("oldsha")
				prefix = append(prefix, commands[0])
				commands = append(prefix, commands...)
			}
			fr, index := ownerRunner(t, commands)
			if automatic {
				fr.startFn = goodStartFn(t)
				requireProtocolUpgrade(t, fr)
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: func(context.Context, string, string, string) error {
				t.Fatal("build before predecessor authorization")
				return nil
			}})
			testDeployRecorder(t, m)
			var err error
			if automatic {
				_, err = m.Ensure(t.Context(), host.Name)
			} else {
				_, _, err = m.DeployForOperation(t.Context(), host, selectedRestartFacts(), true, nil)
			}
			wantBridges := 0
			if automatic {
				wantBridges = 1
			}
			if !errors.Is(err, ErrEndpointMigration) || *index != len(commands) || len(fr.recordedStarts()) != wantBridges {
				t.Fatalf("err=%v commands=%d/%d", err, *index, len(commands))
			}
		})
	}
}
