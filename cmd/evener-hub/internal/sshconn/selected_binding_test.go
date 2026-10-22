package sshconn

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestRemoteRestartDrainFailure(t *testing.T) {
	for _, answer := range []string{pidAliveMarker, "unusable", "transport"} {
		t.Run(answer, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener"}
			killed, probes := 0, 0
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				switch remote := strings.Join(argv[12:], " "); remote {
				case "/opt/evener launch-check --protocol evener-appwire-v7 --json":
					return launchContract("release-selected"), nil
				case "lsof -p 4242 -a -d 1,2":
					return nil, nil
				case "kill -- 4242":
					killed++
					return nil, nil
				case processAliveRemote("4242"):
					probes++
					if answer == "transport" {
						return nil, errReplacementFixture
					}
					return []byte(answer), nil
				default:
					t.Errorf("undeclared command after drain failure %q", remote)
					return nil, errors.New("undeclared drain command")
				}
			}}
			f := modelSelectedProcess(t, fr, host.EvenerPath, "dev", "release-selected")
			f.adhoc = true
			m := newTestManager(t, testRegistry(t, host), fr, Options{sleep: func(context.Context, time.Duration) error { return context.Canceled }})
			_, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil)
			if err == nil || killed != 1 || probes != 1 || m.pendingRestart(host.Name).selected == nil || len(fr.recordedStarts()) != 0 {
				t.Fatalf("drain err=%v kills=%d probes=%d pending=%+v", err, killed, probes, m.pendingRestart(host.Name))
			}
		})
	}
}

func TestRemoteRestartPreservesBinding(t *testing.T) {
	for _, tc := range []struct {
		name, config, args, env, query, launch, logPath string
		psHeader                                        bool
	}{
		{name: "default, environment absent", config: "/home/dev/.config/evener/hub.toml", env: "HOME=/home/dev\x00"},
		{name: "equals flags override environment", config: "/srv/selected.toml", args: "\x00--config=/srv/selected.toml\x00--addr=[::]:0", env: "HOME=/home/dev\x00EVENER_HUB_LISTEN_ADDR=127.0.0.1:0\x00", query: " --config /srv/selected.toml --addr '[::]:0'", launch: " --config=/srv/selected.toml '--addr=[::]:0'"},
		{name: "environment override, no listener flag", config: "/srv/selected.toml", args: "\x00--config\x00/srv/selected.toml", env: "HOME=/home/dev\x00EVENER_HUB_LISTEN_ADDR=127.0.0.1:0\x00", query: " --config /srv/selected.toml", launch: " --config /srv/selected.toml"},
		{name: "exact NUL config space, no ps", config: "/srv/my hub.toml", args: "\x00--config\x00/srv/my hub.toml", env: "HOME=/home/dev\x00", query: " --config '/srv/my hub.toml'", launch: " --config '/srv/my hub.toml'"},
		{name: "leading COMMAND header relaunch", config: "/srv/selected.toml", args: "\x00--config\x00/srv/selected.toml", env: "HOME=/home/dev\x00", query: " --config /srv/selected.toml", launch: " --config /srv/selected.toml", psHeader: true},
		{name: "metacharacter recovered log", config: "/srv/selected.toml", args: "\x00--config\x00/srv/selected.toml", env: "HOME=/home/dev\x00", query: " --config /srv/selected.toml", launch: " --config /srv/selected.toml", logPath: "/home/dev/hub.log;printf marker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, _ := ownerCommands(t)
			host.ConfigPath = tc.config
			logPath := tc.logPath
			if logPath == "" {
				logPath = "/home/dev/my hub.log"
			}
			prepare := func(successor bool) []ownerCommand {
				_, commands := ownerCommands(t)
				commands = append(commands[:10], ownerCommand{systemctlListUnits, ""}, ownerCommand{systemctlListUnitsUser, ""}, ownerCommand{"cat /proc/4242/cgroup", "0::/user.slice/session.scope\n"}, commands[16], commands[17])
				configWord := tc.config
				if strings.Contains(configWord, " ") {
					configWord = "'" + configWord + "'"
				}
				commands[0].command = "/opt/evener hub endpoint --json --config " + configWord
				commands[4].output = "/opt/evener\x00hub" + tc.args + "\x00"
				commands[8].output = tc.env
				exports := "export HOME=/home/dev; "
				if strings.Contains(tc.env, "EVENER_HUB_LISTEN_ADDR=") {
					exports = "export EVENER_HUB_LISTEN_ADDR=127.0.0.1:0; " + exports
				}
				commands[9].command = "CDPATH='' cd -P -- /srv || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; " + exports + "/opt/evener hub endpoint --json" + tc.query
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
				if tc.psHeader {
					pid := "4242"
					if successor {
						pid = "4343"
					}
					commands[4].output = ""
					ps := ownerCommand{"ps -p " + pid + " -ww -o command=", "COMMAND\n/opt/evener hub --config /srv/selected.toml\n"}
					commands = append(commands[:5], append([]ownerCommand{ps}, commands[5:]...)...)
				}
				return commands
			}
			first, second := prepare(false), prepare(false)
			// Current TOML is intentionally edited between identification and mutation.
			// It must not freeze the old listener, or inject its actual assigned port.
			for i := range second {
				second[i].output = strings.ReplaceAll(second[i].output, "0.0.0.0:0", "127.0.0.2:0")
			}
			commands := first
			commands = append(commands, ownerCommand{"lsof -p 4242 -a -d 1,2", "evener 4242 dev 1w REG 1,2 123 456 " + logPath + "\nevener 4242 dev 2w REG 1,2 123 456 " + logPath + "\n"})
			commands = append(commands, second...)
			commands = append(commands, ownerCommand{resolveDeployCommand("/opt/evener"), "/opt/evener\n"})
			commands = append(commands, replacementGateCommands(t, second, true)...)
			env := "export HOME=/home/dev; "
			if strings.Contains(tc.env, "EVENER_HUB_LISTEN_ADDR=") {
				env = "export EVENER_HUB_LISTEN_ADDR=127.0.0.1:0; " + env
			}
			launch := "CDPATH='' cd -P -- /srv || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; " + env + "nohup /opt/evener hub" + tc.launch + " >>'" + logPath + "' 2>&1 </dev/null &"
			commands = append(commands, ownerCommand{"kill -- 4242", ""}, ownerCommand{processAliveRemote("4242"), pidAliveMarker + "\n"}, ownerCommand{processAliveRemote("4242"), pidGoneMarker + "\n"},
				ownerCommand{resolveDeployCommand("/opt/evener"), "/opt/evener\n"}, ownerCommand{"CDPATH='' cd -P -- /srv && pwd -P", "/srv\n"})
			configProbe := second[9]
			if tc.psHeader {
				configProbe = second[10]
			}
			commands = append(commands, configProbe, ownerCommand{launch, ""})
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
			sleeps := 0
			m := newTestManager(t, testRegistry(t, host), fr, Options{sleep: func(context.Context, time.Duration) error { sleeps++; return nil }})
			if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); err != nil {
				t.Fatal(err)
			}
			if sleeps != 1 || *index != len(commands) || m.pendingRestart(host.Name).selected != nil || len(fr.recordedStarts()) != 0 {
				t.Fatalf("commands=%d/%d, pending retained or early attach", *index, len(commands))
			}
		})
	}
}

func TestRemoteRestartRejectsUnselectedSuccessor(t *testing.T) {
	for _, change := range []string{"wrong PID", "unchanged PID and birth", "old boot", "wrong build", "wrong config"} {
		t.Run(change, func(t *testing.T) {
			host, first := ownerCommands(t)
			_, second := ownerCommands(t)
			next := selectedSuccessorCommands(t)
			switch change {
			case "wrong PID":
				next[0].output = strings.ReplaceAll(next[0].output, `"pid":4343`, `"pid":5151`)
				next = next[:2]
			case "unchanged PID and birth":
				next = identitySuccessorCommands(t, "linux", true, true)
			case "old boot":
				next[0].output = strings.ReplaceAll(next[0].output, "boot-two", "boot-one")
			case "wrong build":
				next[0].output = strings.ReplaceAll(next[0].output, "release-selected", "unrelated")
			case "wrong config":
				next[0].output = strings.ReplaceAll(next[0].output, "/srv/selected.toml", "/srv/other.toml")
				next = append(next[:1], ownerCommand{resolveDeployCommand("/srv/selected.toml"), "/srv/selected.toml\n"})
				next = append(next,
					ownerCommand{"stat -Lc '%d %i' /srv/selected.toml 2>/dev/null || stat -L -f '%d %i' /srv/selected.toml", "1 100\n"},
					ownerCommand{"stat -Lc '%d %i' /srv/other.toml 2>/dev/null || stat -L -f '%d %i' /srv/other.toml", "1 200\n"})
			}
			commands := first
			commands = append(commands, second...)
			commands = append(commands, replacementGateCommands(t, second, true)...)
			commands = append(commands, ownerCommand{"systemctl restart evener-hub-selected.service", ""})
			commands = append(commands, next...)
			fr, index := ownerRunner(t, commands)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m := newTestManager(t, testRegistry(t, host), fr, Options{sleep: func(context.Context, time.Duration) error { cancel(); return context.Canceled }})
			if _, err := m.RestartForOperation(ctx, host, selectedRestartFacts(), nil); !errors.Is(err, context.Canceled) {
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
			commands = append(commands, replacementGateCommands(t, second, true)...)
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
				if *index == failAt && !strings.Contains(strings.Join(argv[12:], " "), " launch-check ") {
					return out, errReplacementFixture
				}
				return out, err
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); !errors.Is(err, errReplacementFixture) {
				t.Fatal(err)
			}
			if change == "registration" {
				host.Generation++
			}
			if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); !errors.Is(err, errOwnerEvidence) {
				t.Fatalf("changed selection=%v", err)
			}
			if *index != len(commands) || m.pendingRestart(host.Name).selected == nil || len(fr.recordedStarts()) != 0 {
				t.Fatal("changed selection mutated or discarded pending intent")
			}
		})
	}
}
