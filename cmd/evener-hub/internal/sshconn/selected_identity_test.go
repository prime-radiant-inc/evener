package sshconn

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// All commands are checked by ownerRunner. These declarations are platform
// fixtures, not execution of systemd, launchd or an SSH connection.
func identityOwnerCommands(t *testing.T, platform string, stopped bool) []ownerCommand {
	t.Helper()
	host, base := ownerCommands(t)
	if !stopped {
		if platform == "darwin" {
			return darwinOwnerCommands(t, false, false)
		}
		return base
	}
	report := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
	report.State, report.Running = "stopped", nil
	raw := mustOwnerJSON(t, report)
	if platform == "darwin" {
		darwin := darwinOwnerCommands(t, false, false)
		return []ownerCommand{{base[0].command, raw}, {"launchctl list", "-\t0\tcom.example.evener-hub\n"}, {darwin[2].command, strings.ReplaceAll(darwin[2].output, "pid = 4242", "pid = 0")}, darwin[3], darwin[4], {base[9].command, raw}, {"CDPATH='' cd -P -- /srv && pwd -P", "/srv\n"}}
	}
	return []ownerCommand{
		{base[0].command, raw},
		{systemctlListUnits, "evener-hub-selected.service loaded inactive dead\n"},
		{base[13].command, strings.ReplaceAll(base[13].output, "MainPID=4242", "MainPID=0")},
		base[6], base[7],
		{"systemctl show --property=User,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- evener-hub-selected.service", "User=dev\nEnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\nSetLoginEnvironment=yes\n"},
		{"getent passwd dev | cut -d: -f6", "/home/dev\n"},
		{"values=$(systemctl show --property=Environment --value -- evener-hub-selected.service) || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter, ""},
		{base[9].command, raw}, base[15], {"CDPATH='' cd -P -- /srv && pwd -P", "/srv\n"}, {systemctlListUnitsUser, ""},
	}
}

func identitySuccessorCommands(t *testing.T, platform string, sameBirth, samePID bool) []ownerCommand {
	t.Helper()
	commands := selectedSuccessorCommands(t)
	if platform == "darwin" {
		commands = darwinOwnerCommands(t, false, true)
	}
	for i := range commands {
		if sameBirth {
			commands[i].output = strings.ReplaceAll(commands[i].output, "13500", "12500")
			commands[i].output = strings.ReplaceAll(commands[i].output, "10:00:01", "10:00:00")
		}
		if samePID {
			commands[i].command = strings.ReplaceAll(commands[i].command, "4343", "4242")
			commands[i].command = strings.ReplaceAll(commands[i].command, "generation-next", "generation")
			commands[i].output = strings.ReplaceAll(commands[i].output, "4343", "4242")
		}
	}
	return commands
}

func identityRestartCommand(platform string) ownerCommand {
	if platform == "darwin" {
		return ownerCommand{"launchctl kickstart -k gui/1000/com.example.evener-hub", ""}
	}
	return ownerCommand{"systemctl restart evener-hub-selected.service", ""}
}

func TestRemoteRestartIdentityResolvedTarget(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		for _, stopped := range []bool{false, true} {
			state := "live"
			if stopped {
				state = "stopped"
			}
			for _, replay := range []string{"ready", "lost-response", "stopped-retry", "changed-cache", "changed-registration"} {
				if replay == "stopped-retry" && !stopped {
					continue
				}
				t.Run(platform+"/"+state+"/"+replay, func(t *testing.T) {
					host, _ := ownerCommands(t)
					host.EvenerPath = ""
					facts := selectedRestartFacts()
					facts.OS, facts.UID = platform, "1000"
					first := identityOwnerCommands(t, platform, stopped)
					commands := first
					commands = append(commands, identityOwnerCommands(t, platform, stopped)...)
					commands = append(commands, replacementGateCommands(t, identityOwnerCommands(t, platform, stopped), !stopped)...)
					mutation := identityRestartCommand(platform)
					if stopped {
						if platform == "linux" {
							mutation.command = "systemctl start evener-hub-selected.service"
						} else {
							mutation.command = "launchctl kickstart gui/1000/com.example.evener-hub"
						}
					}
					commands = append(commands, mutation)
					mutationAt := len(commands)
					next := identitySuccessorCommands(t, platform, false, false)
					if replay == "lost-response" {
						commands = append(commands, next[0])
					}
					if replay == "stopped-retry" {
						if platform == "darwin" {
							commands = append(commands, first[0], first[5], first[1], first[2], first[0])
						} else {
							commands = append(commands, first[0], first[8], first[1], first[11], first[2], first[9], first[0])
						}
						commands = append(commands, replacementGateCommands(t, first, false)...)
						commands = append(commands, mutation)
					}
					if replay == "ready" || replay == "lost-response" || replay == "stopped-retry" {
						commands = append(commands, next...)
						if stopped {
							commands = append(commands, next...)
						}
					}
					fr, index := ownerRunner(t, commands)
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					run := fr.runFn
					fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
						out, err := run(ctx, argv, in)
						if replay != "ready" && *index == mutationAt && !strings.Contains(strings.Join(argv, " "), "launch-check") {
							cancel() // The launch may complete after its SSH response is lost.
							return out, context.Canceled
						}
						return out, err
					}
					m := newTestManager(t, testRegistry(t, host), fr, Options{})
					m.setResolvedTarget(host.Name, "/opt/evener")
					_, err := m.RestartForOperation(ctx, host, facts, nil)
					if replay != "ready" {
						if !errors.Is(err, context.Canceled) || m.pendingRestart(host.Name).selected == nil {
							t.Fatalf("lost response: err=%v pending=%+v", err, m.pendingRestart(host.Name))
						}
						switch replay {
						case "changed-cache":
							m.setResolvedTarget(host.Name, "/opt/other/evener")
						case "changed-registration":
							host.Generation++
						}
						_, err = m.RestartForOperation(t.Context(), host, facts, nil)
					}
					invalidated := strings.HasPrefix(replay, "changed-")
					if invalidated && !errors.Is(err, errOwnerEvidence) || !invalidated && err != nil {
						t.Fatalf("restart=%v", err)
					}
					if *index != len(commands) || (m.pendingRestart(host.Name).selected != nil) != invalidated || len(fr.recordedStarts()) != 0 {
						t.Fatalf("commands=%d/%d pending=%+v", *index, len(commands), m.pendingRestart(host.Name))
					}
				})
			}
		}
	}
}

func TestRemoteRestartIdentitySameBirth(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		for _, recovery := range []bool{false, true} {
			mode := "readiness"
			if recovery {
				mode = "recoverRestart"
			}
			for _, samePID := range []bool{false, true} {
				pid := "different-PID"
				if samePID {
					pid = "unchanged-PID"
				}
				t.Run(platform+"/"+mode+"/"+pid, func(t *testing.T) {
					host, _ := ownerCommands(t)
					facts := selectedRestartFacts()
					facts.OS, facts.UID = platform, "1000"
					commands := identityOwnerCommands(t, platform, false)
					commands = append(commands, identityOwnerCommands(t, platform, false)...)
					commands = append(commands, replacementGateCommands(t, identityOwnerCommands(t, platform, false), true)...)
					commands = append(commands, identityRestartCommand(platform))
					mutationAt := len(commands)
					next := identitySuccessorCommands(t, platform, true, samePID)
					if recovery {
						commands = append(commands, next[0])
					}
					commands = append(commands, next...)
					if recovery && samePID {
						commands = append(commands, next[0])
					}
					fr, index := ownerRunner(t, commands)
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					run := fr.runFn
					fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
						out, err := run(ctx, argv, in)
						if recovery && *index == mutationAt {
							cancel()
							return out, context.Canceled
						}
						return out, err
					}
					m := newTestManager(t, testRegistry(t, host), fr, Options{sleep: func(context.Context, time.Duration) error { return errReplacementFixture }})
					_, err := m.RestartForOperation(ctx, host, facts, nil)
					if recovery {
						if !errors.Is(err, context.Canceled) || m.pendingRestart(host.Name).selected == nil {
							t.Fatalf("lost response: err=%v pending=%+v", err, m.pendingRestart(host.Name))
						}
						err = m.recoverSelectedRestart(t.Context(), host, facts, m.pendingRestart(host.Name))
					}
					want := errReplacementFixture
					if recovery && samePID {
						want = errOwnerEvidence
					}
					if samePID && !errors.Is(err, want) || !samePID && err != nil {
						t.Fatalf("same birth settlement: err=%v", err)
					}
					if *index != len(commands) || (m.pendingRestart(host.Name).selected != nil) != samePID || len(fr.recordedStarts()) != 0 {
						t.Fatalf("commands=%d/%d pending=%+v", *index, len(commands), m.pendingRestart(host.Name))
					}
				})
			}
		}
	}
}
