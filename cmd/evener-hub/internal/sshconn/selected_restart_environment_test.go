package sshconn

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/execsupport/shellquote"
)

// Files, fetches and checksums are real. Process/service observations and the
// restarted listener are modeled, with an exact remote-command allowlist.
func TestSelectedRestartEnvironmentFileListenerChange(t *testing.T) {
	for _, name := range []string{"verified-restart", "lost-reply-recovery", "failed-start-recovery", "unrelated-multiline-live", "unrelated-multiline-cold", "no-home-live", "no-home-cold", "legacy-home-live", "legacy-home-cold", "legacy-unset-home-live", "legacy-unset-home-cold", "empty-user-live", "empty-user-cold", "legacy-empty-user-live", "legacy-empty-user-cold", "unit-user-live", "unit-user-cold", "unit-user-other-uid", "repair-fresh-operation", "repair-fresh-automatic", "repair-fresh-operation-prior", "repair-fresh-automatic-prior"} {
		lostReply, failedStart := name == "lost-reply-recovery", name == "failed-start-recovery"
		t.Run(name, func(t *testing.T) {
			repair := strings.HasPrefix(name, "repair-fresh-")
			noHome, cold := strings.HasPrefix(name, "no-home-"), strings.HasSuffix(name, "-cold")
			emptyUser := strings.Contains(name, "empty-user-")
			unitAccount, otherUID := strings.HasPrefix(name, "unit-user-"), name == "unit-user-other-uid"
			host, oldCommands := ownerCommands(t)
			sup := supervisor{kind: supervisorSystemd, label: "evener-hub-selected.service"}
			oldAddr, newAddr := "127.0.0.1:19371", "127.0.0.1:24519"
			oldEnv := []string{"EVENER_HUB_LISTEN_ADDR=" + oldAddr, "HOME=/home/dev"}
			newEnv := []string{"EVENER_HUB_LISTEN_ADDR=" + newAddr, "HOME=/home/dev"}
			policy, unitEnv := "yes", "HOME=/home/dev"
			unset := ""
			unitUser := "dev"
			fileEnv := "HOME=/home/dev\nEVENER_HUB_LISTEN_ADDR=" + newAddr + "\n"
			if strings.HasPrefix(name, "unrelated-multiline-") {
				fileEnv += "PRIVATE_FIXTURE_SECRET='-----BEGIN FIXTURE-----\nHOME=/not-an-assignment\n-----END FIXTURE-----'\n"
				if cold {
					oldEnv = newEnv
				}
			}
			if noHome {
				policy, unitEnv = "no", ""
				oldEnv = []string{"EVENER_HUB_LISTEN_ADDR=" + oldAddr, "XDG_CONFIG_HOME=/srv/config", "XDG_STATE_HOME=/srv/state"}
				newEnv = []string{"EVENER_HUB_LISTEN_ADDR=" + newAddr, "XDG_CONFIG_HOME=/srv/config", "XDG_STATE_HOME=/srv/state"}
				fileEnv = strings.Join(newEnv, "\n") + "\n"
				if cold {
					oldEnv = newEnv
				}
			}
			if unitAccount {
				unitUser, unitEnv = "hubsvc", ""
				oldEnv = []string{"EVENER_HUB_LISTEN_ADDR=" + oldAddr, "HOME=/home/hubsvc", "XDG_CONFIG_HOME=/srv/config", "XDG_STATE_HOME=/srv/state"}
				newEnv = []string{"EVENER_HUB_LISTEN_ADDR=" + newAddr, "HOME=/home/hubsvc", "XDG_CONFIG_HOME=/srv/config", "XDG_STATE_HOME=/srv/state"}
				fileEnv = "EVENER_HUB_LISTEN_ADDR=" + newAddr + "\nXDG_CONFIG_HOME=/srv/config\nXDG_STATE_HOME=/srv/state\n"
				if cold {
					oldEnv = newEnv
				}
			}
			if strings.HasPrefix(name, "legacy-") {
				policy, unitEnv = "", ""
				fileEnv = "EVENER_HUB_LISTEN_ADDR=" + newAddr + "\n"
				if strings.HasPrefix(name, "legacy-unset-") {
					noHome, unset = true, "HOME"
					oldEnv = []string{"EVENER_HUB_LISTEN_ADDR=" + oldAddr, "XDG_CONFIG_HOME=/srv/config", "XDG_STATE_HOME=/srv/state"}
					newEnv = []string{"EVENER_HUB_LISTEN_ADDR=" + newAddr, "XDG_CONFIG_HOME=/srv/config", "XDG_STATE_HOME=/srv/state"}
					fileEnv = strings.Join(newEnv, "\n") + "\n"
				}
				if cold {
					oldEnv = newEnv
				}
			}
			if emptyUser {
				host.SSH = "root@remote"
				noHome, unitUser, unitEnv = true, "", ""
				oldEnv = []string{"EVENER_HUB_LISTEN_ADDR=" + oldAddr, "XDG_CONFIG_HOME=/srv/config", "XDG_STATE_HOME=/srv/state"}
				newEnv = []string{"EVENER_HUB_LISTEN_ADDR=" + newAddr, "XDG_CONFIG_HOME=/srv/config", "XDG_STATE_HOME=/srv/state"}
				fileEnv = strings.Join(newEnv, "\n") + "\n"
				if cold {
					oldEnv = newEnv
				}
			}
			loginProperty := ""
			if policy != "" {
				loginProperty = "SetLoginEnvironment=" + policy + "\n"
			}
			file := filepath.Join(t.TempDir(), "hub.env")
			// This definition already names the next listener before selection. The
			// predecessor still has its original process environment and listener.
			initialFileEnv := fileEnv
			if repair {
				initialFileEnv += "XDG_STATE_HOME=/other\n"
			}
			if err := os.WriteFile(file, []byte(initialFileEnv), 0600); err != nil {
				t.Fatal(err)
			}
			readDefinition := func() []byte {
				properties := "User=" + unitUser + "\nExecStart=/opt/evener hub --config=/srv/selected.toml\nWorkingDirectory=/srv\nEnvironment=" + unitEnv + "\nEnvironmentFiles=" + file + " (ignore_errors=no)\nPassEnvironment=\nUnsetEnvironment=" + unset + "\n" + loginProperty
				show := "show --property=User,ExecStart,WorkingDirectory,Environment,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- " + sup.label
				stub := "systemctl() { case \"$*\" in " + shellquote.RemoteWord(show) + ") printf '%s' " + shellquote.RemoteWord(properties) + ";; show-environment) :;; *) return 1;; esac; }; "
				out, err := exec.CommandContext(t.Context(), "/bin/sh", "-c", stub+serviceDefinitionRemote(sup)).Output()
				if err != nil {
					t.Fatalf("definition checksum: %v", err)
				}
				return out
			}
			definition := readDefinition()
			makeCommands := func(next bool) map[string]string {
				commands := oldCommands
				pid, env, addr, boot := "4242", oldEnv, oldAddr, "boot-one"
				if next {
					commands = selectedSuccessorCommands(t)
					pid, env, addr, boot = "4343", newEnv, newAddr, "boot-two"
				}
				report := remoteEndpointFixture(host.ConfigPath, addr, boot)
				report.ConfiguredListenAddr, report.ListenSource = addr, "env"
				report.Running.RequestedListenAddr, report.Running.ListenSource = addr, "env"
				if next {
					report.Running.PID, report.Running.Version = 4343, "release-selected"
				}
				allowed := map[string]string{}
				for _, c := range commands {
					if emptyUser && c.command == "ps -o user= -p "+pid {
						c.output = "root\n"
					}
					c.output = strings.ReplaceAll(c.output, "User=dev\n", "User="+unitUser+"\n")
					c.command = strings.ReplaceAll(c.command, " --addr '[::]:0'", "")
					c.output = strings.ReplaceAll(c.output, " --addr=[::]:0", "")
					c.output = strings.ReplaceAll(c.output, "--addr=[::]:0\x00", "")
					switch c.command {
					case "selected-lock":
						c.command = selectedLockRemote("/srv/state")
					case "generation", "generation-next":
						c.command = ownerGenerationRemote(pid)
					case "canonical":
						c.command = resolveDeployCommand("/opt/evener")
					case ownerEnvironmentRemote(pid):
						c.output = strings.Join(env, "\x00") + "\x00"
					}
					if strings.Contains(c.command, " hub endpoint ") {
						c.output = mustOwnerJSON(t, report)
						if strings.HasPrefix(c.command, ownerContext("/srv", []string{"HOME=/home/dev"})) {
							c.command = strings.Replace(c.command, ownerContext("/srv", []string{"HOME=/home/dev"}), ownerContext("/srv", env), 1)
						}
					}
					allowed[c.command] = c.output
				}
				// The pre-stop gate inspects the next environment against the same
				// config/root while the original process still owns that root.
				allowed[ownerContext("/srv", newEnv)+"/opt/evener hub endpoint --json --config /srv/selected.toml"] = mustOwnerJSON(t, report)
				if repair {
					wrong := report
					wrong.HubStateRoot, wrong.Running = "/other", nil
					wrong.State = "stopped"
					wrongEnv := append(append([]string(nil), newEnv...), "XDG_STATE_HOME=/other")
					allowed[ownerContext("/srv", wrongEnv)+"/opt/evener hub endpoint --json --config /srv/selected.toml"] = mustOwnerJSON(t, wrong)
				}
				if noHome {
					// Explicit XDG roots select the same config even if a caller
					// incorrectly invents HOME. Process evidence still has none.
					withHome := []string{newEnv[0], "HOME=/home/dev", newEnv[1], newEnv[2]}
					if emptyUser {
						withHome[1] = "HOME=/root"
					}
					allowed[ownerContext("/srv", withHome)+"/opt/evener hub endpoint --json --config /srv/selected.toml"] = mustOwnerJSON(t, report)
				}
				if unitAccount {
					sshHome := []string{newEnv[0], "HOME=/home/dev", newEnv[2], newEnv[3]}
					allowed[ownerContext("/srv", sshHome)+"/opt/evener hub endpoint --json --config /srv/selected.toml"] = mustOwnerJSON(t, report)
				}
				return allowed
			}
			before, after := makeCommands(false), makeCommands(true)
			stoppedCommands := map[string]string{}
			stoppedReport := remoteEndpointFixture(host.ConfigPath, newAddr, "")
			stoppedReport.State, stoppedReport.Running = "stopped", nil
			stoppedReport.ConfiguredListenAddr, stoppedReport.ListenSource = newAddr, "env"
			for command, out := range before {
				if strings.Contains(command, " hub endpoint ") {
					out = mustOwnerJSON(t, stoppedReport)
				}
				if cold && command == systemctlListUnits {
					out = sup.label + " loaded inactive dead\n"
				}
				if cold && command == systemctlListUnitsUser {
					out = ""
				}
				stoppedCommands[command] = strings.ReplaceAll(out, "MainPID=4242", "MainPID=0")
			}
			stoppedCommands["CDPATH='' cd -P -- /srv && pwd -P"] = "/srv\n"
			var m *Manager
			var saved pendingRestartState
			restarted, effects, fileReads, polls := false, 0, 0, 0
			rootHomeReads := 0
			stopped, oldEnvConfigReads := cold, 0
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				switch remote {
				case "/opt/evener launch-check --json", "/opt/evener launch-check --protocol " + appwire.ProtocolVersion + " --json":
					return launchContract("release-selected"), nil
				case serviceDefinitionRemote(sup):
					got := readDefinition()
					if !bytes.Equal(got, definition) {
						t.Fatal("fixture definition changed after selection")
					}
					return got, nil
				case "getent passwd dev | cut -d: -f6":
					return []byte("/home/dev\n"), nil
				case "getent passwd hubsvc | cut -d: -f6":
					return []byte("/home/hubsvc\n"), nil
				case "getent passwd root | cut -d: -f6":
					if !emptyUser {
						t.Fatal("unexpected root HOME lookup")
					}
					rootHomeReads++
					return []byte("/root\n"), nil
				case "id -u dev":
					return []byte("1000\n"), nil
				case "id -u hubsvc":
					if otherUID {
						return []byte("1001\n"), nil
					}
					return []byte("1000\n"), nil
				case "systemctl show --property=User,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- " + sup.label:
					return []byte("User=" + unitUser + "\nEnvironmentFiles=" + file + " (ignore_errors=no)\nPassEnvironment=\nUnsetEnvironment=" + unset + "\n" + loginProperty), nil
				case "values=$(systemctl show --property=Environment --value -- " + sup.label + ") || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter:
					return nil, nil
				case systemdEnvironmentFileRemote(file, false):
					fileReads++
					return exec.CommandContext(ctx, "/bin/sh", "-c", remote).Output()
				case "systemctl restart " + sup.label, "systemctl start " + sup.label:
					effects++
					wantCommand := "systemctl restart " + sup.label
					if cold || effects > 1 {
						wantCommand = "systemctl start " + sup.label
					}
					if remote != wantCommand || effects > 1 && !failedStart {
						t.Fatalf("unexpected replacement effect: %s", remote)
					}
					saved = m.pendingRestart(host.Name)
					if !noHome && !unitAccount && !reflect.DeepEqual(saved.nextEnv, newEnv) {
						t.Fatalf("replacement environment not retained before restart: %q", saved.nextEnv)
					}
					snapshot := m.pendingRestart(host.Name)
					snapshot.nextEnv[0] = "EVENER_HUB_LISTEN_ADDR=wrong"
					if !reflect.DeepEqual(m.pendingRestart(host.Name).nextEnv, saved.nextEnv) {
						t.Fatal("pending snapshot aliases retained replacement environment")
					}
					if failedStart && effects == 1 {
						stopped = true
						return nil, errReplacementFixture
					}
					restarted, stopped = true, false
					if lostReply {
						return nil, errReplacementFixture
					}
					return nil, nil
				}
				allowed := before
				if stopped {
					allowed = stoppedCommands
					if !cold && remote == ownerContext("/srv", oldEnv)+"/opt/evener hub endpoint --json --config /srv/selected.toml" {
						oldEnvConfigReads++
					}
				}
				if restarted {
					allowed = after
					if remote == "/opt/evener hub endpoint --json --config /srv/selected.toml" {
						polls++
					}
				}
				if out, ok := allowed[remote]; ok {
					return []byte(out), nil
				}
				t.Fatalf("unexpected remote command: %s", remote)
				return nil, errOwnerEvidence
			}}
			m = newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "release-selected", sleep: func(context.Context, time.Duration) error { return nil }})
			if strings.HasSuffix(name, "-prior") {
				m.pendingRestarts[host.Name] = pendingRestartState{command: "previous-command", expectedVersion: "release-selected", restart: true}
			}
			restart := func() error {
				if strings.HasPrefix(name, "repair-fresh-automatic") {
					_, err := m.restartHubWithStartProof(t.Context(), host, selectedRestartFacts(), "release-selected", "", hubIdentity{}, nil)
					return err
				}
				_, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil)
				return err
			}
			if repair {
				previous := m.pendingRestart(host.Name)
				if err := restart(); !errors.Is(err, errOwnerSelectionMismatch) || effects != 0 || !reflect.DeepEqual(m.pendingRestart(host.Name), previous) {
					t.Errorf("read-only refusal: err=%v effects=%d pending restored=%v", err, effects, reflect.DeepEqual(m.pendingRestart(host.Name), previous))
				}
				if err := os.WriteFile(file, []byte(fileEnv), 0600); err != nil {
					t.Fatal(err)
				}
				definition = readDefinition()
				fileReads = 0
			}
			restartErr := restart()
			if otherUID {
				if !errors.Is(restartErr, errOwnerEvidence) || effects != 0 || len(fr.recordedStarts()) != 0 || m.pendingRestart(host.Name).active() {
					t.Fatalf("different UID: err=%v effects=%d attaches=%d pending=%v", restartErr, effects, len(fr.recordedStarts()), m.pendingRestart(host.Name).active())
				}
				return
			}
			if lostReply || failedStart {
				if !errors.Is(restartErr, errReplacementFixture) {
					t.Errorf("lost reply: %v", restartErr)
				}
			} else if restartErr != nil {
				t.Errorf("healthy successor on %s failed verification after %d observations: %v", newAddr, polls, restartErr)
			}
			if saved.selected == nil {
				t.Fatalf("restart did not reach launch: effects=%d err=%v", effects, restartErr)
			}
			if (noHome || unitAccount) && !reflect.DeepEqual(saved.nextEnv, newEnv) {
				t.Errorf("replacement environment=%q, want=%q", saved.nextEnv, newEnv)
			}
			if emptyUser && rootHomeReads != 0 {
				t.Errorf("empty system User queried root HOME: reads=%d", rootHomeReads)
			}
			wantFileReads := 1
			if cold {
				wantFileReads = 3
			}
			if effects != 1 || fileReads != wantFileReads || !reflect.DeepEqual(saved.selected.env, oldEnv) {
				t.Fatalf("effects=%d file reads=%d predecessor env=%q", effects, fileReads, saved.selected.env)
			}
			if failedStart && (!stopped || !m.pendingRestart(host.Name).active()) {
				t.Fatal("failed start must leave stopped hub and pending intent")
			}
			if !lostReply && !failedStart && restartErr == nil {
				if m.pendingRestart(host.Name).active() {
					t.Fatal("verified restart left pending intent")
				}
				// Simulate losing the completion acknowledgement after verification.
				m.pendingRestarts[host.Name] = saved
			}
			if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); err != nil {
				t.Errorf("healthy successor recovery refused: %v", err)
			}
			wantEffects := 1
			if failedStart {
				wantEffects = 2
			}
			if effects != wantEffects || stopped || !restarted || oldEnvConfigReads != 0 || m.pendingRestart(host.Name).active() || len(fr.recordedStarts()) != 0 {
				t.Errorf("recovery: effects=%d stopped=%v restarted=%v old-env config reads=%d pending=%v attaches=%d", effects, stopped, restarted, oldEnvConfigReads, m.pendingRestart(host.Name).active(), len(fr.recordedStarts()))
			}
		})
	}
}

func TestSelectedSuccessorExpectedEnvironment(t *testing.T) {
	for _, name := range []string{"expected-change", "unexpected-env", "legacy-unchanged", "legacy-changed", "explicit-empty", "empty-mismatch", "unexecuted-live", "unexecuted-empty-live", "validated-stopped-start", "validated-empty-stopped-start", "legacy-stopped", "definition", "argv", "cwd", "supervisor", "config", "root", "same-process", "old-boot", "wrong-build"} {
		t.Run(name, func(t *testing.T) {
			host, _ := ownerCommands(t)
			prior := selectedOwner{host: host, pid: "4242", generation: "old", supervisor: supervisor{kind: supervisorSystemd, label: "evener-hub-selected.service"}, argv: []string{"/opt/evener", "hub"}, cwdIdentity: "/srv", env: []string{"HOME=/home/dev"}, definition: "stable-definition", report: remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "old-boot")}
			now := prior.clone()
			now.pid, now.generation, now.env = "4343", "new", []string{"HOME=/home/dev", "EVENER_HUB_LISTEN_ADDR=127.0.0.1:24519"}
			now.report.Running.PID, now.report.Running.BootID, now.report.Running.Version = 4343, "new-boot", "release-selected"
			pending := pendingRestartState{selected: &prior, nextEnv: append([]string(nil), now.env...)}
			want := false
			switch name {
			case "expected-change":
				want = true
				if sameLaunch(now, prior) || !sameLaunchWithEnv(now, prior, pending.nextEnv) {
					t.Fatal("expected successor environment changed predecessor matching")
				}
			case "unexpected-env":
				now.env = []string{"HOME=/wrong"}
			case "legacy-unchanged":
				pending.nextEnv, now.env, want = nil, prior.env, true
			case "legacy-changed":
				pending.nextEnv = nil
			case "explicit-empty":
				pending.nextEnv, now.env, want = []string{}, nil, true
			case "empty-mismatch":
				pending.nextEnv = []string{}
			case "unexecuted-live":
				prior.report.Running, pending.nextEnv, now.env = nil, nil, prior.env
			case "unexecuted-empty-live":
				prior.report.Running, prior.env, pending.nextEnv, now.env = nil, nil, nil, nil
			case "validated-stopped-start":
				prior.report.Running, want = nil, true
			case "validated-empty-stopped-start":
				prior.report.Running, pending.nextEnv, now.env, want = nil, []string{}, nil, true
			case "legacy-stopped":
				prior.pid, prior.report.Running, pending.nextEnv, now.env, want = "0", nil, nil, prior.env, true
			case "definition":
				now.definition = "changed-definition"
			case "argv":
				now.argv = append(now.argv, "--addr=other")
			case "cwd":
				now.cwdIdentity = "/other"
			case "supervisor":
				now.supervisor = supervisor{}
			case "config":
				now.report.ConfigPath = "/other.toml"
			case "root":
				now.report.HubStateRoot = "/other"
			case "same-process":
				now.pid, now.generation = prior.pid, prior.generation
			case "old-boot":
				now.report.Running.BootID = prior.report.Running.BootID
			case "wrong-build":
				now.report.Running.Version = "wrong"
			}
			if got := selectedSuccessor(now, prior, pending.nextEnv, "release-selected", ""); got != want {
				t.Fatalf("successor=%v want=%v", got, want)
			}
		})
	}
}
