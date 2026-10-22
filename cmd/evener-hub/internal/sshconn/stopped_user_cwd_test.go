package sshconn

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

// Process, passwd and systemd observations are modeled with exact commands.
// The account home is deliberately independent of the service's HOME value.
func TestStoppedServiceDefaultWorkingDirectory(t *testing.T) {
	for _, tc := range []struct {
		name, property, cwd string
		kind                supervisorKind
		env                 []string
	}{
		{"user-home-override", "", "/home/dev", supervisorSystemdUser, []string{"HOME=/alternate"}},
		{"user-home-unset", "", "/home/dev", supervisorSystemdUser, nil},
		{"user-explicit-directory", "/srv", "/srv", supervisorSystemdUser, []string{"HOME=/alternate"}},
		{"system-default-directory", "", "/", supervisorSystemd, []string{"HOME=/alternate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, _ := ownerCommands(t)
			sup := supervisor{kind: tc.kind, label: "evener-hub-selected.service"}
			manager := "systemctl"
			if tc.kind == supervisorSystemdUser {
				manager += " --user"
			}
			endpoint := "/opt/evener hub endpoint --json --config /srv/selected.toml"
			before := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "")
			before.State, before.Running = "stopped", nil
			after := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:24519", "boot-two")
			after.Running.PID, after.Running.Version = 4343, "release-selected"
			stopped, running := map[string]string{}, map[string]string{}
			for _, command := range selectedSuccessorCommands(t) {
				c, out := command.command, command.output
				c = strings.ReplaceAll(c, " --addr '[::]:0'", "")
				out = strings.ReplaceAll(out, " --addr=[::]:0", "")
				out = strings.ReplaceAll(out, "--addr=[::]:0\x00", "")
				switch c {
				case "selected-lock":
					c = selectedLockRemote("/srv/state")
				case "generation-next":
					c = ownerGenerationRemote("4343")
				case "canonical":
					c = resolveDeployCommand("/opt/evener")
				case "readlink /proc/4343/cwd":
					out = tc.cwd + "\n"
				case ownerEnvironmentRemote("4343"):
					out = ""
					if len(tc.env) != 0 {
						out = strings.Join(tc.env, "\x00") + "\x00"
					}
				}
				if strings.Contains(c, " hub endpoint ") {
					out = mustOwnerJSON(t, after)
					if strings.HasPrefix(c, ownerContext("/srv", []string{"HOME=/home/dev"})) {
						c = ownerContext(tc.cwd, tc.env) + endpoint
					}
				}
				running[c] = out
			}
			for _, target := range []struct {
				commands map[string]string
				pid, raw string
			}{{stopped, "0", mustOwnerJSON(t, before)}, {running, "4343", mustOwnerJSON(t, after)}} {
				commands := target.commands
				commands[endpoint] = target.raw
				commands[systemctlListUnits], commands[systemctlListUnitsUser] = "", ""
				listing := systemctlListUnits
				if tc.kind == supervisorSystemdUser {
					listing = systemctlListUnitsUser
				}
				commands[listing] = sup.label + " loaded inactive dead\n"
				if target.pid != "0" {
					commands[listing] = sup.label + " loaded active running\n"
				}
				commands[supervisorShowRemote(sup)] = "Id=" + sup.label + "\nMainPID=" + target.pid + "\nUser=dev\nExecStart={ path=/opt/evener ; argv[]=/opt/evener hub --config=/srv/selected.toml ; ignore_errors=no ; }\nWorkingDirectory=" + tc.property + "\n"
				commands[serviceDefinitionRemote(sup)] = "1234 567\n"
				commands["getent passwd dev | cut -d: -f6"] = "/home/dev\n"
				commands["id -u dev"] = "1000\n"
				commands[resolveDeployCommand("/opt/evener")] = "/opt/evener\n"
				commands[manager+" show --property=User,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- "+sup.label] = "User=dev\nEnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\nSetLoginEnvironment=no\n"
				commands["values=$("+manager+" show-environment) || exit; printf '%s\\n' \"$values\" | "+narrowEnvironmentFilter] = ""
				unitEnv := ""
				if len(tc.env) != 0 {
					unitEnv = strings.Join(tc.env, "\x00") + "\x00"
				}
				commands["values=$("+manager+" show --property=Environment --value -- "+sup.label+") || exit; printf '%s\\n' \"$values\" | "+narrowEnvironmentFilter] = unitEnv
				// Absolute config/root do not depend on cwd, so even the faulty
				// baseline must reach the independent supervisor/cwd comparison.
				for _, cwd := range []string{"/home/dev", "/alternate", "/srv", "/"} {
					commands[ownerContext(cwd, tc.env)+endpoint] = target.raw
					commands["CDPATH='' cd -P -- "+cwd+" && pwd -P"] = cwd + "\n"
				}
			}
			stopped[selectedLockRemote("/srv/state")] = ""
			effects := 0
			var m *Manager
			var selected selectedOwner
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				switch remote {
				case "/opt/evener launch-check --json", "/opt/evener launch-check --protocol " + appwire.ProtocolVersion + " --json":
					return launchContract("release-selected"), nil
				case manager + " start " + sup.label:
					effects++
					pending := m.pendingRestart(host.Name)
					if pending.selected == nil {
						t.Fatal("start has no selected owner")
					}
					selected = pending.selected.clone()
					return nil, nil
				}
				commands := stopped
				if effects != 0 {
					commands = running
				}
				if out, ok := commands[remote]; ok {
					return []byte(out), nil
				}
				t.Fatalf("unexpected remote command: %s", remote)
				return nil, errOwnerEvidence
			}}
			m = newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "release-selected", sleep: func(context.Context, time.Duration) error { return nil }})
			_, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil)
			if err != nil || effects != 1 || selected.cwd != tc.cwd || selected.cwdIdentity != tc.cwd || !reflect.DeepEqual(selected.env, tc.env) || selected.supervisor != sup || m.pendingRestart(host.Name).active() || len(fr.recordedStarts()) != 0 {
				t.Fatalf("err=%v starts=%d cwd=%q physical=%q env=%q supervisor=%+v pending=%v attaches=%d", err, effects, selected.cwd, selected.cwdIdentity, selected.env, selected.supervisor, m.pendingRestart(host.Name).active(), len(fr.recordedStarts()))
			}
		})
	}
}
