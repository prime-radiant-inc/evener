package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// The process boundary is ordered and exact. In particular no curl, port lookup,
// credential read or attach command is accepted on this manager restart path.
func TestRemoteRestartSelectedInstance(t *testing.T) {
	host, first := ownerCommands(t)
	_, second := ownerCommands(t)
	successor := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:24519", "boot-two")
	successor.HubStateRoot, successor.Running.HubStateRoot = "/srv/state", "/srv/state"
	successor.Running.PID = 4343
	successor.Running.Version = "release-selected"
	commands := first
	commands = append(commands, second...)
	commands = append(commands, replacementGateCommands(t, second, true)...)
	commands = append(commands, ownerCommand{"systemctl restart evener-hub-selected.service", ""})
	// Independent successor association is checked, not just a version at an old port.
	_, next := ownerCommands(t)
	for i := range next {
		if next[i].command == "generation" {
			next[i].command = "generation-next"
		}
		next[i].command = strings.ReplaceAll(next[i].command, "4242", "4343")
		next[i].output = strings.ReplaceAll(next[i].output, "4242", "4343")
		next[i].output = strings.ReplaceAll(next[i].output, "12500", "13500")
	}
	next[0].output = mustOwnerJSON(t, successor)
	commands = append(commands, next...)
	fr, index := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "release-selected"})
	if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); err != nil {
		t.Fatal(err)
	}
	if *index != len(commands) || m.pendingRestart(host.Name).command != "" || len(fr.recordedStarts()) != 0 {
		t.Fatalf("commands=%d/%d pending=%+v starts=%d", *index, len(commands), m.pendingRestart(host.Name), len(fr.recordedStarts()))
	}
}

func selectedRestartFacts() Preflight {
	return Preflight{OS: "linux", Version: "release-selected", LaunchCheckKnown: true, HubEndpointSchema: 1}
}

func selectedSuccessorCommands(t *testing.T) []ownerCommand {
	_, next := ownerCommands(t)
	for i := range next {
		next[i].command = strings.ReplaceAll(next[i].command, "4242", "4343")
		if next[i].command == "generation" {
			next[i].command = "generation-next"
		}
		next[i].output = strings.ReplaceAll(next[i].output, "4242", "4343")
		next[i].output = strings.ReplaceAll(next[i].output, "12500", "13500")
		next[i].output = strings.ReplaceAll(next[i].output, "boot-one", "boot-two")
		next[i].output = strings.ReplaceAll(next[i].output, "19371", "24519")
		next[i].output = strings.ReplaceAll(next[i].output, `"version":"dev"`, `"version":"release-selected"`)
	}
	return next
}

func TestRemoteRestartUnresponsiveService(t *testing.T) {
	host, first := ownerCommands(t)
	_, second := ownerCommands(t)
	damaged := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
	damaged.State, damaged.Reason, damaged.Running = "unavailable", "unreachable", nil
	first[0].output, second[0].output = mustOwnerJSON(t, damaged), mustOwnerJSON(t, damaged)
	first[9].output, second[9].output = mustOwnerJSON(t, damaged), mustOwnerJSON(t, damaged)
	// First contact uses the original image, not the newly installed pathname.
	first = append(first[:16], append([]ownerCommand{{"/proc/4242/exe launch-check --json", goodLaunchCheck}}, first[16:]...)...)
	commands := first
	commands = append(commands, second...)
	commands = append(commands, replacementGateCommands(t, second, true)...)
	commands = append(commands, identityRestartCommand("linux"))
	commands = append(commands, selectedSuccessorCommands(t)...)
	// No predecessor time exists, so return a freshly verified successor time.
	commands = append(commands, selectedSuccessorCommands(t)...)
	fr, index := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "release-selected"})
	if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); err != nil {
		t.Fatal(err)
	}
	if *index != len(commands) || m.pendingRestart(host.Name).active() || len(fr.recordedStarts()) != 0 {
		t.Fatalf("commands=%d/%d pending=%+v", *index, len(commands), m.pendingRestart(host.Name))
	}
}

func TestRemoteRestartRecoversStoppedReplacement(t *testing.T) {
	for _, direct := range []bool{true, false} {
		t.Run(fmt.Sprint("direct=", direct), func(t *testing.T) {
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
			// Revalidate exactly the selected definition, not another dormant unit.
			commands = append(commands, ownerCommand{first[0].command, raw}, ownerCommand{first[9].command, raw}, first[10], first[11], ownerCommand{first[13].command, strings.ReplaceAll(first[13].output, "MainPID=4242", "MainPID=0")}, first[15], ownerCommand{first[0].command, raw})
			commands = append(commands, replacementGateCommands(t, identityOwnerCommands(t, "linux", true), false)...)
			commands = append(commands, ownerCommand{"systemctl start evener-hub-selected.service", ""})
			commands = append(commands, selectedSuccessorCommands(t)...)
			fr, index := ownerRunner(t, commands)
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				out, err := run(ctx, argv, in)
				if *index == failAt && !strings.Contains(strings.Join(argv[12:], " "), " launch-check ") {
					return out, errReplacementFixture
				}
				return out, err
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "release-selected"})
			if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); !errors.Is(err, errReplacementFixture) {
				t.Fatalf("first err=%v", err)
			}
			pending := m.pendingRestart(host.Name)
			if pending.selected == nil || pending.command == "" || *index != failAt {
				t.Fatalf("pending=%+v commands=%d", pending, *index)
			}
			// Return values must not grant mutation of the manager's retained authority.
			pending.selected.argv[0] = "/wrong/evener"
			var err error
			if direct {
				_, err = m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil)
			} else {
				err = m.recoverSelectedRestart(t.Context(), host, selectedRestartFacts(), m.pendingRestart(host.Name))
			}
			if err != nil {
				t.Fatal(err)
			}
			if *index != len(commands) || m.pendingRestart(host.Name).command != "" || len(fr.recordedStarts()) != 0 {
				t.Fatalf("commands=%d/%d pending=%+v", *index, len(commands), m.pendingRestart(host.Name))
			}
		})
	}
}

var errReplacementFixture = errors.New("fixture replacement launch failed")
