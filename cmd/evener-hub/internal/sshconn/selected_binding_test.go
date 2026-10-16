package sshconn

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRemoteRestartPreservesBinding(t *testing.T) {
	for _, tc := range []struct{ name, config, args, env, query, launch string }{
		{"default, environment absent", "/home/dev/.config/evener/hub.toml", "", "HOME=/home/dev\x00", "", ""},
		{"equals flags override environment", "/srv/selected.toml", "\x00--config=/srv/selected.toml\x00--addr=[::]:0", "HOME=/home/dev\x00EVENER_HUB_LISTEN_ADDR=127.0.0.1:0\x00", " --config /srv/selected.toml --addr '[::]:0'", " --config=/srv/selected.toml '--addr=[::]:0'"},
		{"environment override, no listener flag", "/srv/selected.toml", "\x00--config\x00/srv/selected.toml", "HOME=/home/dev\x00EVENER_HUB_LISTEN_ADDR=127.0.0.1:0\x00", " --config /srv/selected.toml", " --config /srv/selected.toml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, _ := ownerCommands(t)
			host.ConfigPath = tc.config
			prepare := func(successor bool) []ownerCommand {
				_, commands := ownerCommands(t)
				commands = append(commands[:10], ownerCommand{systemctlListUnits, ""}, ownerCommand{systemctlListUnitsUser, ""}, ownerCommand{"cat /proc/4242/cgroup", "0::/user.slice/session.scope\n"}, commands[16], commands[17])
				commands[0].command = "/opt/evener hub endpoint --json --config " + tc.config
				commands[4].output = "/opt/evener\x00hub" + tc.args + "\x00"
				commands[8].output = tc.env
				exports := "export HOME=/home/dev; "
				if strings.Contains(tc.env, "EVENER_HUB_LISTEN_ADDR=") {
					exports = "export EVENER_HUB_LISTEN_ADDR=127.0.0.1:0; " + exports
				}
				commands[9].command = "cd /srv || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; " + exports + "/opt/evener hub endpoint --json" + tc.query
				for i := range commands {
					commands[i].output = strings.ReplaceAll(commands[i].output, "/srv/selected.toml", tc.config)
					if successor {
						commands[i].command = strings.ReplaceAll(commands[i].command, "4242", "4343")
						if commands[i].command == "generation" {
							commands[i].command = "generation-next"
						}
						commands[i].output = strings.ReplaceAll(commands[i].output, "4242", "4343")
						commands[i].output = strings.ReplaceAll(commands[i].output, "12500", "13500")
						commands[i].output = strings.ReplaceAll(commands[i].output, "boot-one", "boot-two")
						commands[i].output = strings.ReplaceAll(commands[i].output, "19371", "24519")
						commands[i].output = strings.ReplaceAll(commands[i].output, `"version":"dev"`, `"version":"release-selected"`)
					}
				}
				return commands
			}
			first, second := prepare(false), prepare(false)
			// Current TOML is intentionally edited between identification and mutation.
			// It must not freeze the old listener, or inject its actual assigned port.
			second[0].output = strings.ReplaceAll(second[0].output, "0.0.0.0:0", "127.0.0.2:0")
			second[9].output = strings.ReplaceAll(second[9].output, "0.0.0.0:0", "127.0.0.2:0")
			commands := first
			commands = append(commands, ownerCommand{"lsof -p 4242 -a -d 1,2", ""})
			commands = append(commands, second...)
			env := "export HOME=/home/dev; "
			if strings.Contains(tc.env, "EVENER_HUB_LISTEN_ADDR=") {
				env = "export EVENER_HUB_LISTEN_ADDR=127.0.0.1:0; " + env
			}
			launch := "cd /srv || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; " + env + "nohup /opt/evener hub" + tc.launch + " </dev/null >/dev/null 2>&1 &"
			commands = append(commands, ownerCommand{"kill -- 4242", ""}, ownerCommand{processAliveRemote("4242"), pidGoneMarker + "\n"}, ownerCommand{launch, ""})
			commands = append(commands, prepare(true)...)
			fr, index := ownerRunner(t, commands)
			// Model a genuinely absent systemctl, not an empty listing being used to
			// excuse an unreadable supervisor. Commands are still checked in order.
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				out, err := run(ctx, argv, in)
				remote := strings.Join(argv[12:], " ")
				if remote == systemctlListUnits || remote == systemctlListUnitsUser {
					return []byte("systemctl: not found\n"), errReplacementFixture
				}
				return out, err
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			if err := m.RestartForOperation(t.Context(), host, selectedRestartFacts()); err != nil {
				t.Fatal(err)
			}
			if *index != len(commands) || m.pendingRestart(host.Name).selected != nil || len(fr.recordedStarts()) != 0 {
				t.Fatalf("commands=%d/%d, pending retained or early attach", *index, len(commands))
			}
		})
	}
}

func TestRemoteRestartRejectsUnselectedSuccessor(t *testing.T) {
	for _, change := range []string{"wrong PID", "reused generation", "old boot", "wrong build", "wrong config"} {
		t.Run(change, func(t *testing.T) {
			host, first := ownerCommands(t)
			_, second := ownerCommands(t)
			next := selectedSuccessorCommands(t)
			switch change {
			case "wrong PID":
				next[0].output = strings.ReplaceAll(next[0].output, `"pid":4343`, `"pid":5151`)
				next = next[:2]
			case "reused generation":
				next[2].output, next[16].output = "kernel-boot\n12500\n", "kernel-boot\n12500\n"
			case "old boot":
				next[0].output = strings.ReplaceAll(next[0].output, "boot-two", "boot-one")
			case "wrong build":
				next[0].output = strings.ReplaceAll(next[0].output, "release-selected", "unrelated")
			case "wrong config":
				next[0].output = strings.ReplaceAll(next[0].output, "/srv/selected.toml", "/srv/other.toml")
				next = next[:1]
			}
			commands := first
			commands = append(commands, second...)
			commands = append(commands, ownerCommand{"systemctl restart evener-hub-selected.service", ""})
			commands = append(commands, next...)
			fr, index := ownerRunner(t, commands)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m := newTestManager(t, testRegistry(t, host), fr, Options{sleep: func(context.Context, time.Duration) error { cancel(); return context.Canceled }})
			if err := m.RestartForOperation(ctx, host, selectedRestartFacts()); !errors.Is(err, context.Canceled) {
				t.Fatalf("restart=%v", err)
			}
			if *index != len(commands) || m.pendingRestart(host.Name).selected == nil || len(fr.recordedStarts()) != 0 {
				t.Fatalf("wrong successor cleared authority or attached, commands=%d/%d", *index, len(commands))
			}
		})
	}
}

func TestRemoteRestartStoppedReplayInvalidatesChangedSelection(t *testing.T) {
	for _, change := range []string{"registration", "root", "service definition"} {
		t.Run(change, func(t *testing.T) {
			host, first := ownerCommands(t)
			_, second := ownerCommands(t)
			commands := first
			commands = append(commands, second...)
			commands = append(commands, ownerCommand{"systemctl restart evener-hub-selected.service", ""})
			failAt := len(commands)
			stopped := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
			stopped.State, stopped.Running = "stopped", nil
			raw := mustOwnerJSON(t, stopped)
			if change == "root" {
				raw = strings.ReplaceAll(raw, "/srv/state", "/srv/other-state")
			}
			if change != "registration" {
				commands = append(commands, ownerCommand{first[0].command, raw})
			}
			if change == "service definition" {
				commands = append(commands, ownerCommand{first[9].command, raw}, first[10], first[11], ownerCommand{first[13].command, strings.ReplaceAll(first[13].output, "MainPID=4242", "MainPID=0")}, ownerCommand{first[15].command, "5678 901\n"})
			}
			fr, index := ownerRunner(t, commands)
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				out, err := run(ctx, argv, in)
				if *index == failAt {
					return out, errReplacementFixture
				}
				return out, err
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			if err := m.RestartForOperation(t.Context(), host, selectedRestartFacts()); !errors.Is(err, errReplacementFixture) {
				t.Fatal(err)
			}
			if change == "registration" {
				host.Generation++
			}
			if err := m.RestartForOperation(t.Context(), host, selectedRestartFacts()); !errors.Is(err, errOwnerEvidence) {
				t.Fatalf("changed selection=%v", err)
			}
			if *index != len(commands) || m.pendingRestart(host.Name).selected == nil || len(fr.recordedStarts()) != 0 {
				t.Fatal("changed selection mutated or discarded pending intent")
			}
		})
	}
}
