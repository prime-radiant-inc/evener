package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// Exact ordered external-process commands for selected ownership and restart.
// No fixture accepts a guessed-port probe or credential read.
type ownerCommand struct{ command, output string }

func ownerCommands(t *testing.T) (hostreg.Host, []ownerCommand) {
	host := hostreg.Host{Name: "selected", SSH: "dev@remote", EvenerPath: "/opt/evener", ConfigPath: "/srv/selected.toml"}
	report := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
	report.HubStateRoot = "/srv/state"
	report.Running.HubStateRoot = report.HubStateRoot
	commands := []ownerCommand{
		{"/opt/evener hub endpoint --json --config /srv/selected.toml", mustOwnerJSON(t, report)},
		{"selected-lock", "4242\n"},
		{"generation", "kernel-boot\n12500\n"},
		{"ps -o user= -p 4242", "dev\n"},
		{"if [ -r /proc/4242/cmdline ]; then cat /proc/4242/cmdline; fi", "/opt/evener\x00hub\x00--config=/srv/selected.toml\x00--addr=[::]:0\x00"},
		{"readlink /proc/4242/cwd", "/srv\n"},
		{"canonical", "/opt/evener\n"},
		{"canonical", "/opt/evener\n"},
		{ownerEnvironmentRemote("4242"), "HOME=/home/dev\x00"},
		{"CDPATH='' cd -P -- /srv || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; export HOME=/home/dev; /opt/evener hub endpoint --json --config /srv/selected.toml --addr '[::]:0'", mustOwnerJSON(t, report)},
		{"systemctl list-units --type=service --all --no-legend --plain", "evener-hub-other.service loaded active running\nevener-hub-selected.service loaded active running\n"},
		{"systemctl --user list-units --type=service --all --no-legend --plain", "evener-hub-user.service loaded active running\n"},
		{"systemctl show --property=Id,MainPID,User,ExecStart,WorkingDirectory -- evener-hub-other.service", "Id=evener-hub-other.service\nMainPID=5151\nUser=dev\nExecStart={ path=/opt/evener ; argv[]=/opt/evener hub --config=/srv/other.toml ; ignore_errors=no ; }\nWorkingDirectory=/srv\n"},
		{"systemctl show --property=Id,MainPID,User,ExecStart,WorkingDirectory -- evener-hub-selected.service", "Id=evener-hub-selected.service\nMainPID=4242\nUser=dev\nExecStart={ path=/opt/evener ; argv[]=/opt/evener hub --config=/srv/selected.toml --addr=[::]:0 ; ignore_errors=no ; }\nWorkingDirectory=/srv\n"},
		{"systemctl --user show --property=Id,MainPID,User,ExecStart,WorkingDirectory -- evener-hub-user.service", "Id=evener-hub-user.service\nMainPID=6161\nUser=dev\nExecStart={ path=/opt/evener ; argv[]=/opt/evener hub --config=/srv/user.toml ; ignore_errors=no ; }\nWorkingDirectory=/srv\n"},
		{serviceDefinitionRemote(supervisor{kind: supervisorSystemd, label: "evener-hub-selected.service"}), "1234 567\n"},
		{"generation", "kernel-boot\n12500\n"},
		{"selected-lock", "4242\n"},
	}
	return host, commands
}

func ownerRunner(t *testing.T, commands []ownerCommand) (*fakeRunner, *int) {
	index := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		got := strings.Join(argv[12:], " ")
		got = strings.Replace(got, " launch-check --protocol "+appwire.ProtocolVersion+" --json", " launch-check --json", 1)
		if got == "/opt/evener launch-check --json" && (index >= len(commands) || commands[index].command != got) {
			return launchContract("release-selected"), nil
		}
		if index >= len(commands) {
			t.Fatalf("unexpected extra command %v", argv)
		}
		want := commands[index]
		switch want.command {
		case "selected-lock", "other-lock":
			root := "/srv/state"
			if want.command == "other-lock" {
				root = "/srv/other-state"
			}
			exact := "lock=" + root + "/hub.lock; identity=$(test ! -L \"$lock\" && test -f \"$lock\" && stat -Lc '%d %i' \"$lock\") || exit; set -- $identity; [ \"$#\" -eq 2 ] || exit 1; " +
				"dev=$1; ino=$2; major=$(( ((dev >> 8) & 4095) | ((dev >> 32) & 4294963200) )); minor=$(( (dev & 255) | ((dev >> 12) & 4294967040) )); key=$(printf '%02x:%02x:%s' \"$major\" \"$minor\" \"$ino\"); " +
				"for attempt in 1 2 3; do current=$(test ! -L \"$lock\" && test -f \"$lock\" && stat -Lc '%d %i' \"$lock\") || exit; [ \"$current\" = \"$identity\" ] || exit 1; " +
				"owners=$(awk -v key=\"$key\" '$2 == \"FLOCK\" && $3 == \"ADVISORY\" && $4 == \"WRITE\" && $6 == key { print $5 }' /proc/locks && printf '.') || exit; owners=${owners%.}; " +
				"current=$(test ! -L \"$lock\" && test -f \"$lock\" && stat -Lc '%d %i' \"$lock\") || exit; [ \"$current\" = \"$identity\" ] || exit 1; if [ -n \"$owners\" ]; then printf '%s' \"$owners\"; exit; fi; done"
			if got != exact {
				t.Fatalf("not a selected-root lock probe: %s", got)
			}
		case "generation", "generation-next":
			pid := "4242"
			if want.command == "generation-next" {
				pid = "4343"
			}
			if got != "cat /proc/sys/kernel/random/boot_id && s=$(cat /proc/"+pid+"/stat) && s=${s##*) } && set -- $s && [ \"$#\" -ge 20 ] && shift 19 && printf '%s\\n' \"$1\"" {
				t.Fatalf("not a process generation probe: %s", got)
			}
		case "canonical":
			if got != resolveDeployCommand("/opt/evener") {
				t.Fatalf("not selected executable canonicalization: %s", got)
			}
		default:
			if got != want.command {
				t.Fatalf("command %d = %s, want %s", index, got, want.command)
			}
		}
		index++
		return []byte(want.output), nil
	}}
	return fr, &index
}

func TestRemoteOwnerSelectedEndpoint(t *testing.T) {
	host, commands := ownerCommands(t)
	fr, index := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	got, err := m.resolveOwnedEndpoint(t.Context(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.pid != "4242" || got.supervisor != (supervisor{kind: supervisorSystemd, label: "evener-hub-selected.service"}) || got.generation != "kernel-boot:12500" || got.report.Running.BootID != "boot-one" || *index != len(commands) {
		t.Fatalf("owner=%+v commands=%d/%d", got, *index, len(commands))
	}
}

// TestRemoteOwnerAcceptsCandidateProofAfterEmptyGlobalRead drives the full
// resolver through the fallback: the global lock read is empty, the running
// endpoint's own fdinfo proves it holds the lock, and every later generation,
// process and revalidation check still runs.
func TestRemoteOwnerAcceptsCandidateProofAfterEmptyGlobalRead(t *testing.T) {
	host, base := ownerCommands(t)
	commands := []ownerCommand{base[0], {"selected-lock", ""}, {selectedLockCandidateRemote("/srv/state", "4242"), "4242\n"}}
	commands = append(commands, base[2:]...)
	fr, index := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	owner, err := m.resolveOwnedEndpoint(t.Context(), host, selectedRestartFacts())
	if err != nil || owner.pid != "4242" || *index != len(commands) {
		t.Fatalf("candidate proof owner=%+v err=%v commands=%d/%d", owner, err, *index, len(commands))
	}
}

func mustOwnerJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRemoteOwnerImageCapability(t *testing.T) {
	for _, tc := range []struct {
		name, output      string
		runErr, errorWant error
	}{
		{"capable", goodLaunchCheck, nil, nil},
		{"old image", `{"protocol":"old","version":"old"}`, nil, ErrEndpointMigration},
		{"unsupported image", `{"protocol":"other","version":"other","hub_endpoint_schema":2}`, nil, ErrEndpointMigration},
		{"damaged response", `{`, nil, errOwnerEvidence},
		{"null response", `null`, nil, errOwnerEvidence},
		{"empty response", `{}`, nil, errOwnerEvidence},
		{"unreadable image", "", errors.New("proc access denied"), errOwnerEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := hostreg.Host{Name: "selected", SSH: "dev@remote"}
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				if got := strings.Join(argv[12:], " "); got != "/proc/4242/exe launch-check --json" {
					return nil, fmt.Errorf("unexpected command %s", got)
				}
				return []byte(tc.output), tc.runErr
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			err := m.probeOwnerImageCapability(t.Context(), host, "4242")
			if !errors.Is(err, tc.errorWant) {
				t.Fatalf("err=%v want=%v", err, tc.errorWant)
			}
			if errors.Is(tc.errorWant, errOwnerEvidence) && errors.Is(err, ErrEndpointMigration) {
				t.Fatalf("missing evidence became legacy: %v", err)
			}
		})
	}
}

func TestRemoteOwnerCancellationWinsCompletedProbe(t *testing.T) {
	host := hostreg.Host{Name: "selected", SSH: "dev@remote"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fr := &fakeRunner{runFn: func(context.Context, []string, io.Reader) ([]byte, error) {
		cancel()
		return []byte(goodLaunchCheck), nil
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	if err := m.probeOwnerImageCapability(ctx, host, "4242"); !errors.Is(err, context.Canceled) || errors.Is(err, ErrEndpointMigration) {
		t.Fatalf("err=%v", err)
	}
}

func TestRemoteOwnerRejectsWrongAssociation(t *testing.T) {
	for _, tc := range []struct {
		name             string
		step             int
		old, replacement string
		emptyGlobalRead  bool
	}{
		{name: "wrong record PID", step: 0, old: `"pid":4242`, replacement: `"pid":5151`},
		{name: "missing lock owner", step: 1, old: "4242", replacement: "", emptyGlobalRead: true},
		{name: "ambiguous lock owner", step: 1, old: "4242", replacement: "4242\n5151"},
		{name: "wrong effective user", step: 3, old: "dev", replacement: "other"},
		{name: "wrong config", step: 4, old: "/srv/selected.toml", replacement: "/srv/other.toml"},
		{name: "wrong executable", step: 6, old: "/opt/evener", replacement: "/other/evener"},
		{name: "wrong service user", step: 13, old: "User=dev", replacement: "User=other"},
		{name: "wrong service config", step: 13, old: "/srv/selected.toml", replacement: "/srv/other.toml"},
		{name: "wrong service cwd", step: 13, old: "WorkingDirectory=/srv", replacement: "WorkingDirectory=/other"},
		{name: "ambiguous definition", step: 13, old: " }", replacement: " } { path=/other ; }"},
		{name: "two associated services", step: 14, old: "MainPID=6161", replacement: "MainPID=4242"},
		{name: "changed start ticks", step: 16, old: "12500", replacement: "12501"},
		{name: "rebooted owner", step: 16, old: "kernel-boot", replacement: "other-boot"},
		{name: "changed lock owner", step: 17, old: "4242", replacement: "5151"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, commands := ownerCommands(t)
			if !strings.Contains(commands[tc.step].output, tc.old) {
				t.Fatal("invalid test input")
			}
			commands[tc.step].output = strings.ReplaceAll(commands[tc.step].output, tc.old, tc.replacement)
			if tc.step == 1 {
				// A completed empty global read is supplemented by the
				// per-process candidate proof; an ambiguous read is not.
				extra := []ownerCommand{}
				if tc.emptyGlobalRead {
					extra = append(extra, ownerCommand{selectedLockCandidateRemote("/srv/state", "4242"), ""})
				}
				commands = append(commands[:2], append(extra, ownerCommand{systemctlListUnits, ""}, ownerCommand{systemctlListUnitsUser, ""})...)
			}
			if tc.name == "wrong config" {
				commands = append(commands[:9], ownerCommand{ownerContext("/srv", []string{"HOME=/home/dev"}) + resolveDeployCommand("/srv/other.toml"), "/srv/other.toml\n"})
				commands = append(commands,
					ownerCommand{ownerContext("/srv", []string{"HOME=/home/dev"}) + "stat -Lc '%d %i' /srv/other.toml 2>/dev/null || stat -L -f '%d %i' /srv/other.toml", "1 200\n"},
					ownerCommand{ownerContext("/srv", []string{"HOME=/home/dev"}) + "stat -Lc '%d %i' /srv/selected.toml 2>/dev/null || stat -L -f '%d %i' /srv/selected.toml", "1 100\n"})
			}
			switch tc.name {
			case "wrong effective user":
				commands = append(commands[:4], ownerCommand{"id -u other", "1001\n"}, ownerCommand{"id -u dev", "1000\n"})
			case "wrong service user":
				commands = append(commands[:14], ownerCommand{"id -u other", "1001\n"}, ownerCommand{"id -u dev", "1000\n"})
			case "wrong service cwd":
				commands = append(commands[:14], ownerCommand{"CDPATH='' cd -P -- /other && pwd -P", "/other\n"}, ownerCommand{"CDPATH='' cd -P -- /srv && pwd -P", "/srv\n"})
			}
			fr, _ := ownerRunner(t, commands)
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			_, err := m.resolveOwnedEndpoint(t.Context(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1})
			if !errors.Is(err, errOwnerEvidence) {
				t.Fatalf("err=%v", err)
			}
			if len(fr.recordedStarts()) != 0 {
				t.Fatal("owner selection started a process")
			}
		})
	}
}

func TestRemoteOwnerDamagedMetadataStillSelectsOwner(t *testing.T) {
	host, commands := ownerCommands(t)
	report := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
	report.State, report.Reason, report.Running = "unavailable", "lock_held_without_record", nil
	commands[0].output = mustOwnerJSON(t, report)
	commands = append(commands[:16], append([]ownerCommand{{"/proc/4242/exe launch-check --json", goodLaunchCheck}}, commands[16:]...)...)
	fr, _ := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	got, err := m.resolveOwnedEndpoint(t.Context(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1})
	if err != nil || got.pid != "4242" || got.report.Running != nil {
		t.Fatalf("owner=%+v err=%v", got, err)
	}
}

func TestRemoteOwnerSelectsUserService(t *testing.T) {
	host, commands := ownerCommands(t)
	commands[13].output = strings.ReplaceAll(commands[13].output, "MainPID=4242", "MainPID=5151")
	commands[14].output = "Id=evener-hub-user.service\nMainPID=4242\nUser=\nExecStart={ path=/opt/evener ; argv[]=/opt/evener hub --config=/srv/selected.toml --addr=[::]:0 ; ignore_errors=no ; }\nWorkingDirectory=/srv\n"
	commands[15].command = serviceDefinitionRemote(supervisor{kind: supervisorSystemdUser, label: "evener-hub-user.service"})
	fr, _ := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	got, err := m.resolveOwnedEndpoint(t.Context(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1})
	if err != nil || got.supervisor != (supervisor{kind: supervisorSystemdUser, label: "evener-hub-user.service"}) {
		t.Fatalf("owner=%+v err=%v", got, err)
	}
}
