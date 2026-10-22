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

	"primeradiant.com/evener/execsupport/shellquote"
)

// Process/service evidence is modeled. Count the actual manager mutation commands,
// not an inferred error or the number of read-only inspections.
func TestSelectedRestartBoundary(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		for _, name := range []string{"matching-live", "no-live-report", "different-boot", "different-pid", "different-config", "different-root", "live-A-definition-B", "stopped-A-definition-B", "binding-during-ad-hoc-gate", "live-environment-file", "changed-environment-file", "cold-environment-file", "stable-B-environment-file", "stable-B-unit-environment"} {
			environmentFile := strings.HasSuffix(name, "environment-file")
			if platform == "darwin" && (name == "binding-during-ad-hoc-gate" || environmentFile || name == "stable-B-unit-environment") {
				continue
			}
			t.Run(platform+"/"+name, func(t *testing.T) {
				host, _ := ownerCommands(t)
				stopped := name == "stopped-A-definition-B" || name == "cold-environment-file"
				commands := identityOwnerCommands(t, platform, stopped)
				report := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
				owner := selectedOwner{host: host, pid: "4242", generation: "kernel-boot:12500", supervisor: supervisor{kind: supervisorSystemd, label: "evener-hub-selected.service"}, argv: []string{"/opt/evener", "hub", "--config=/srv/selected.toml", "--addr=[::]:0"}, cwd: "/srv", cwdIdentity: "/srv", env: []string{"HOME=/home/dev"}, definition: "1234 567", report: report}
				bindingCase, entryValid := name == "binding-during-ad-hoc-gate", true
				if bindingCase {
					owner.supervisor, owner.definition = supervisor{}, ""
				}
				facts := selectedRestartFacts()
				facts.OS = platform
				if platform == "darwin" {
					facts.UID, owner.domainUID = "1000", "1000"
					owner.supervisor = supervisor{kind: supervisorLaunchd, label: "com.example.evener-hub"}
					owner.generation = "Fri Oct 16 10:00:00 2026"
					owner.definition = launchdIntent(launchdDefinition{program: "/opt/evener", cwd: owner.cwd, argv: owner.argv, env: owner.env})
				}
				if stopped {
					owner.pid, owner.report.State, owner.report.Running = "0", "stopped", nil
				}
				var envFile string
				if environmentFile {
					envFile = filepath.Join(t.TempDir(), "hub.env")
					contents := "HOME=/home/dev\n"
					if name == "stable-B-environment-file" {
						contents += "XDG_STATE_HOME=/srv/B\n"
					}
					if err := os.WriteFile(envFile, []byte(contents), 0o600); err != nil {
						t.Fatal(err)
					}
					owner.definition = strings.Join(strings.Fields(string(environmentFileDefinition(t, owner.supervisor, envFile))), " ")
					if name == "changed-environment-file" {
						if err := os.WriteFile(envFile, []byte("HOME=/home/changed\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
				fresh := owner.clone().report
				switch name {
				case "no-live-report":
					fresh.State, fresh.Running = "unavailable", nil
				case "different-boot":
					fresh.Running.BootID = "other-boot"
				case "different-pid":
					fresh.Running.PID = 5151
				case "different-config":
					fresh.Running.ConfigPath = "/srv/B.toml"
				case "different-root":
					fresh.HubStateRoot, fresh.Running.HubStateRoot = "/srv/B", "/srv/B"
				}
				mutations, definitionReads, coldEnvironmentReads, nextRootReads := 0, 0, 0, 0
				fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
					remote := strings.Join(argv[12:], " ")
					if environmentFile && remote == serviceDefinitionRemote(owner.supervisor) {
						definitionReads++
						return environmentFileDefinition(t, owner.supervisor, envFile), nil
					}
					if environmentFile && remote == "systemctl show --property=EnvironmentFiles,PassEnvironment,UnsetEnvironment -- evener-hub-selected.service" {
						coldEnvironmentReads++
						return []byte("EnvironmentFiles=" + envFile + " (ignore_errors=no)\nPassEnvironment=\nUnsetEnvironment=\n"), nil
					}
					if environmentFile && remote == systemdEnvironmentFileRemote(envFile, false) {
						return exec.CommandContext(t.Context(), "/bin/sh", "-c", remote).Output()
					}
					if platform == "linux" && strings.HasSuffix(name, "definition-B") && remote == serviceDefinitionRemote(owner.supervisor) {
						return []byte("9999 567"), nil
					}
					if bindingCase {
						if remote == systemctlListUnits || remote == systemctlListUnitsUser || remote == "cat /proc/4242/cgroup" {
							return nil, nil
						}
						if strings.Contains(remote, " hub endpoint ") {
							entryValid = false
						}
					}
					if strings.HasPrefix(remote, "systemctl restart ") || strings.HasPrefix(remote, "systemctl start ") || strings.HasPrefix(remote, "launchctl kickstart ") || strings.HasPrefix(remote, "kill -- ") {
						mutations++
						return nil, errReplacementFixture
					}
					if remote == selectedLockRemote("/srv/state") {
						return []byte("4242\n"), nil
					}
					if remote == ownerGenerationRemote("4242") {
						return []byte("kernel-boot\n12500\n"), nil
					}
					if remote == resolveDeployCommand("/opt/evener") {
						return []byte("/opt/evener\n"), nil
					}
					for _, config := range []string{"/srv/selected.toml", "/srv/B.toml"} {
						if remote == resolveDeployCommand(config) {
							return []byte(config + "\n"), nil
						}
					}
					if strings.HasPrefix(remote, "values=$(systemctl show --property=Environment ") && (strings.HasSuffix(name, "definition-B") || name == "stable-B-unit-environment") {
						return []byte("XDG_STATE_HOME=/srv/B\x00"), nil
					}
					if remote == ownerContext("/srv", []string{"HOME=/home/dev", "XDG_STATE_HOME=/srv/B"})+"/opt/evener hub endpoint --json --config /srv/selected.toml --addr '[::]:0'" {
						nextRootReads++
						reportB := fresh
						reportB.HubStateRoot = "/srv/B/evener"
						if fresh.Running != nil {
							running := *fresh.Running
							running.HubStateRoot = reportB.HubStateRoot
							reportB.Running = &running
						}
						return []byte(mustOwnerJSON(t, reportB)), nil
					}
					for _, command := range commands {
						if command.command == remote {
							if strings.Contains(remote, " hub endpoint ") {
								return []byte(mustOwnerJSON(t, fresh)), nil
							}
							output := command.output
							if strings.HasPrefix(remote, "launchctl print ") && strings.HasSuffix(name, "definition-B") {
								output += "XDG_STATE_HOME => /srv/B\n"
							}
							return []byte(output), nil
						}
					}
					switch remote {
					case "getent passwd dev | cut -d: -f6":
						return []byte("/home/dev\n"), nil
					case "systemctl show --property=EnvironmentFiles,PassEnvironment,UnsetEnvironment -- evener-hub-selected.service":
						coldEnvironmentReads++
						return []byte("EnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\n"), nil
					case "values=$(systemctl show --property=Environment --value -- evener-hub-selected.service) || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter:
						if strings.HasSuffix(name, "definition-B") {
							return []byte("XDG_STATE_HOME=/srv/B\x00"), nil
						}
						return nil, nil
					}
					t.Errorf("unexpected read-only command: %s", remote)
					return nil, errOwnerEvidence
				}}
				m := newTestManager(t, testRegistry(t, host), fr, Options{})
				if bindingCase {
					host, _ = m.reg.Get(host.Name)
					owner.host = host
				}
				command, _ := owner.supervisor.restartRemote(facts.UID)
				pending := m.setSelectedRestart(host, owner, command, "release-selected", "")
				if bindingCase {
					pending.binding = newEffectBinding(host, func() error {
						if !entryValid {
							return errEntryBinding
						}
						return nil
					})
					m.pendingRestarts[host.Name] = pending
				}
				saved := m.pendingRestart(host.Name)
				err := m.restartSelectedPending(t.Context(), host, facts, pending, !stopped)
				refusal := errOwnerEvidence
				if name == "different-config" {
					refusal = ErrPreflightDecode
				}
				if bindingCase {
					refusal = errEntryBinding
				}
				if name == "matching-live" || name == "live-environment-file" || name == "cold-environment-file" {
					if mutations != 1 || !errors.Is(err, errReplacementFixture) {
						t.Fatalf("mutations=%d err=%v, want one authorized restart", mutations, err)
					}
				} else if mutations != 0 || !errors.Is(err, refusal) || !reflect.DeepEqual(m.pendingRestart(host.Name), saved) {
					t.Fatalf("refusal: mutations=%d err=%v pending preserved=%v", mutations, err, reflect.DeepEqual(m.pendingRestart(host.Name), saved))
				}
				if strings.HasPrefix(name, "stable-B-") && nextRootReads != 1 {
					t.Fatalf("next-launch root B was not inspected: reads=%d err=%v", nextRootReads, err)
				}
				if environmentFile {
					wantReads := 1
					if name == "changed-environment-file" {
						wantReads = 0 // The unchanged definition comparison refuses first.
					}
					if definitionReads == 0 || coldEnvironmentReads != wantReads {
						t.Fatalf("wrong environment evidence: definition reads=%d cold environment reads=%d err=%v", definitionReads, coldEnvironmentReads, err)
					}
				}
			})
		}
	}
}

// Execute the production checksum command over a real EnvironmentFile. Only
// systemctl metadata is modeled; changing file contents must change the proof.
func environmentFileDefinition(t *testing.T, sup supervisor, envFile string) []byte {
	t.Helper()
	properties := "User=dev\nExecStart=/opt/evener hub --config=/srv/selected.toml --addr=[::]:0\nWorkingDirectory=/srv\nEnvironment=HOME=/home/dev\nEnvironmentFiles=" + envFile + " (ignore_errors=no)\nPassEnvironment=\nUnsetEnvironment=\n"
	show := "show --property=User,ExecStart,WorkingDirectory,Environment,EnvironmentFiles,PassEnvironment,UnsetEnvironment -- " + sup.label
	stub := "systemctl() { case \"$*\" in " + shellquote.RemoteWord(show) + ") printf '%s' " + shellquote.RemoteWord(properties) + ";; show-environment) :;; *) return 1;; esac; }; "
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", stub+serviceDefinitionRemote(sup))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("service definition checksum: %v: %s", err, out)
	}
	return out
}

func TestSelectedStartDoesNotStop(t *testing.T) {
	command, ok := (supervisor{kind: supervisorLaunchd, label: "com.evener.hub"}).startRemote("501")
	if !ok || command != "launchctl kickstart gui/501/com.evener.hub" {
		t.Fatalf("unsafe start command %q, ok=%v", command, ok)
	}
}
