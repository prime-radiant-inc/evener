package sshconn

import (
	"errors"
	"strings"
	"testing"
)

func TestRemoteRestartDarwinStoppedAdHocRejectsNewService(t *testing.T) {
	host, linux := ownerCommands(t)
	stopped := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
	stopped.State, stopped.Running = "stopped", nil
	raw := mustOwnerJSON(t, stopped)
	darwin := darwinOwnerCommands(t, false, false)
	commands := []ownerCommand{{linux[0].command, raw}, {linux[9].command, raw}, darwin[1], darwin[2], darwin[3], darwin[4], {linux[9].command, raw}}
	fr, index := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	prior := selectedOwner{host: host, pid: "4242", generation: "prior-birth", argv: []string{"/opt/evener", "hub", "--config=/srv/selected.toml", "--addr=[::]:0"}, cwd: "/srv", cwdIdentity: "/srv", env: []string{"HOME=/home/dev"}, report: stopped, domainUID: "1000"}
	m.setSelectedRestart(host, prior, selectedRelaunch(prior), "release-selected", "")
	facts := selectedRestartFacts()
	facts.OS, facts.UID = "darwin", "1000"
	if _, err := m.RestartForOperation(t.Context(), host, facts, nil); !errors.Is(err, errOwnerEvidence) {
		t.Fatalf("changed stopped authority=%v", err)
	}
	if *index != len(commands) || m.pendingRestart(host.Name).selected == nil || len(fr.recordedStarts()) != 0 {
		t.Fatal("changed stopped authority mutated or lost pending intent")
	}
}

// These are source-derived launchctl/ps/lsof command boundaries, not Darwin
// execution. The separate Linux fixture covers services sharing an executable.
func darwinOwnerCommands(t *testing.T, damaged, successor bool) []ownerCommand {
	t.Helper()
	host, linux := ownerCommands(t)
	pid, boot, start := "4242", "boot-one", "Fri Oct 16 10:00:00 2026\n"
	if successor {
		pid, boot, start = "4343", "boot-two", "Fri Oct 16 10:00:01 2026\n"
	}
	report := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", boot)
	if successor {
		report.Running.PID = 4343
		report.Running.Version = "release-selected"
		report.Running.BoundAddr = "127.0.0.1:24519"
		report.Running.LocalURL = "http://127.0.0.1:24519"
		report.Running.LoopbackURL = report.Running.LocalURL
	}
	if damaged {
		report.State, report.Reason, report.Running = "unavailable", "unreachable", nil
	}
	definition := "pid = " + pid + "\nprogram = /opt/evener\nworking directory = /srv\narguments = {\n/opt/evener\nhub\n--config=/srv/selected.toml\n--addr=[::]:0\n}\nHOME => /home/dev\n"
	// The unavailable case must independently inspect the service definition
	// rather than trust a report PID.
	listing := pid + "\t0\tcom.example.evener-hub\n"
	commands := []ownerCommand{{linux[0].command, mustOwnerJSON(t, report)}, {"launchctl list", listing}, {launchdDefinitionRemote("1000", "com.example.evener-hub"), definition}, {"canonical", "/opt/evener\n"}, {"canonical", "/opt/evener\n"}, {linux[9].command, mustOwnerJSON(t, report)}, {"ps -p " + pid + " -o lstart=", start}, {"ps -o user= -p " + pid, "dev\n"}, {hubArgvRemote(pid), ""}, {"ps -p " + pid + " -ww -o command=", "/opt/evener hub --config=/srv/selected.toml --addr=[::]:0\n"}, {"lsof -a -p " + pid + " -d cwd -Fn", "p" + pid + "\nfcwd\nn/srv\n"}, {"canonical", "/opt/evener\n"}, {"canonical", "/opt/evener\n"}, {"ps -p " + pid + " -o lstart=", start}}
	return commands
}

func TestRemoteRestartDarwinRetainedWitness(t *testing.T) {
	host, _ := ownerCommands(t)
	commands := darwinOwnerCommands(t, false, false)
	// Learn capability from a validated healthy generation before metadata damage.
	commands = append(commands, darwinOwnerCommands(t, true, false)...)
	commands = append(commands, darwinOwnerCommands(t, true, false)...)
	commands = append(commands, replacementGateCommands(t, darwinOwnerCommands(t, true, false), true)...)
	commands = append(commands, identityRestartCommand("darwin"))
	commands = append(commands, darwinOwnerCommands(t, false, true)...)
	// No predecessor time exists, so return a freshly verified successor time.
	commands = append(commands, darwinOwnerCommands(t, false, true)...)
	fr, index := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	facts := selectedRestartFacts()
	facts.OS, facts.UID = "darwin", "1000"
	if _, err := m.resolveOwnedEndpoint(t.Context(), host, facts); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RestartForOperation(t.Context(), host, facts, nil); err != nil {
		t.Fatal(err)
	}
	if *index != len(commands) || m.pendingRestart(host.Name).active() || len(fr.recordedStarts()) != 0 {
		t.Fatal("Darwin no-report restart skipped evidence, retained pending intent or attached")
	}
}

func TestRemoteOwnerDarwinImageAssociation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		capability string
		capable    bool
	}{
		{name: "capable", capability: goodLaunchCheck, capable: true},
		{name: "old", capability: `{"protocol":"old","version":"old"}`},
		{name: "unsupported schema", capability: `{"protocol":"old","version":"old","hub_endpoint_schema":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, _ := ownerCommands(t)
			commands := darwinOwnerCommands(t, true, false)
			last := commands[len(commands)-1]
			commands = commands[:len(commands)-1]
			rootStat := "test ! -L /srv/state/hub.lock && test -f /srv/state/hub.lock && stat -f '%d %i' /srv/state/hub.lock"
			commands = append(commands, ownerCommand{rootStat, "16777234 777\n"}, ownerCommand{"lsof -a -p 4242 -F pftDi", "p4242\nf3\ntREG\nD0x1000012\ni777\n"}, ownerCommand{rootStat, "16777234 777\n"},
				ownerCommand{"canonical", "/opt/evener\n"}, ownerCommand{"stat -f '%d %i' /opt/evener", "16777234 12345\n"}, ownerCommand{"lsof -a -p 4242 -d txt -F Di", "p4242\nftxt\nD0x1000012\ni12345\n"}, ownerCommand{"/opt/evener launch-check --json", tc.capability}, ownerCommand{"stat -f '%d %i' /opt/evener", "16777234 12345\n"})
			if tc.capable {
				commands = append(commands, last)
			}
			fr, index := ownerRunner(t, commands)
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			facts := selectedRestartFacts()
			facts.OS, facts.UID = "darwin", "1000"
			_, err := m.resolveOwnedEndpoint(t.Context(), host, facts)
			if tc.capable && err != nil || !tc.capable && !errors.Is(err, ErrEndpointMigration) {
				t.Fatalf("image evidence=%v", err)
			}
			if *index != len(commands) || m.pendingRestart(host.Name).selected != nil {
				t.Fatal("image check mutated or skipped evidence")
			}
		})
	}
}

func TestRemoteOwnerServiceAssociationWithoutVisibleLock(t *testing.T) {
	host, base := ownerCommands(t)
	// Resolve a service PID from its current definition and process context, then
	// independently inspect that owner and refresh the supervisor association.
	commands := []ownerCommand{base[0], {"selected-lock", ""}, base[10], base[12], base[13]}
	commands = append(commands, base[4:10]...)
	commands = append(commands, ownerCommand{serviceLockVnodeRemote("/srv/state", "4242"), "match\n"}, base[11], base[14])
	commands = append(commands, base[2:17]...)
	commands = append(commands, base[10:15]...)
	fr, index := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	owner, err := m.resolveOwnedEndpoint(t.Context(), host, selectedRestartFacts())
	if err != nil || owner.pid != "4242" || *index != len(commands) {
		t.Fatalf("service authority without lock: pid=%s err=%v commands=%d/%d", owner.pid, err, *index, len(commands))
	}
}

func TestRemoteOwnerRejectsUnresolvedServiceAsAdhoc(t *testing.T) {
	host, commands := ownerCommands(t)
	commands = append(commands[:10], ownerCommand{systemctlListUnits, ""}, ownerCommand{systemctlListUnitsUser, ""}, ownerCommand{"cat /proc/4242/cgroup", "0::/system.slice/evener-hub-selected.service\n"})
	fr, _ := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); !errors.Is(err, errOwnerEvidence) {
		t.Fatal(err)
	}
	for _, run := range fr.recordedRuns() {
		if strings.Contains(strings.Join(run, " "), "kill --") {
			t.Fatal("unresolved service was killed ad-hoc")
		}
	}
}

func TestRemoteOwnerDarwinRejectsMismatchedSelectedDefinition(t *testing.T) {
	host, _ := ownerCommands(t)
	commands := darwinOwnerCommands(t, false, false)[:5]
	commands[2].output = strings.ReplaceAll(commands[2].output, "/srv/selected.toml", "/srv/other.toml")
	commands = append(commands,
		ownerCommand{ownerContext("/srv", []string{"HOME=/home/dev"}) + resolveDeployCommand("/srv/other.toml"), "/srv/other.toml\n"},
		ownerCommand{ownerContext("/srv", []string{"HOME=/home/dev"}) + "stat -Lc '%d %i' /srv/other.toml 2>/dev/null || stat -L -f '%d %i' /srv/other.toml", "1 200\n"},
		ownerCommand{ownerContext("/srv", []string{"HOME=/home/dev"}) + "stat -Lc '%d %i' /srv/selected.toml 2>/dev/null || stat -L -f '%d %i' /srv/selected.toml", "1 100\n"})
	fr, index := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	facts := selectedRestartFacts()
	facts.OS, facts.UID = "darwin", "1000"
	if _, err := m.RestartForOperation(t.Context(), host, facts, nil); !errors.Is(err, errOwnerEvidence) {
		t.Fatalf("mismatched definition=%v", err)
	}
	if *index != len(commands) || m.pendingRestart(host.Name).selected != nil {
		t.Fatal("mismatched selected service fell through to ad-hoc mutation")
	}
}
