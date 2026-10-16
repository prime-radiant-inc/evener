package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// This stage deliberately tests the read-only owner boundary, not the old
// restart implementation. No fixture accepts a mutation or a guessed-port probe.
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
		{"canonical", "/opt/evener\n"},
		{"canonical", "/opt/evener\n"},
		{"readlink /proc/4242/cwd", "/srv\n"},
		{"systemctl list-units --type=service --all --no-legend --plain", "evener-hub-other.service loaded active running\nevener-hub-selected.service loaded active running\n"},
		{"systemctl --user list-units --type=service --all --no-legend --plain", "evener-hub-user.service loaded active running\n"},
		{"systemctl show --property=Id,MainPID,User,ExecStart,WorkingDirectory -- evener-hub-other.service", "Id=evener-hub-other.service\nMainPID=5151\nUser=dev\nExecStart={ path=/opt/evener ; argv[]=/opt/evener hub --config=/srv/other.toml ; ignore_errors=no ; }\nWorkingDirectory=/srv\n"},
		{"systemctl show --property=Id,MainPID,User,ExecStart,WorkingDirectory -- evener-hub-selected.service", "Id=evener-hub-selected.service\nMainPID=4242\nUser=dev\nExecStart={ path=/opt/evener ; argv[]=/opt/evener hub --config=/srv/selected.toml --addr=[::]:0 ; ignore_errors=no ; }\nWorkingDirectory=/srv\n"},
		{"systemctl --user show --property=Id,MainPID,User,ExecStart,WorkingDirectory -- evener-hub-user.service", "Id=evener-hub-user.service\nMainPID=6161\nUser=dev\nExecStart={ path=/opt/evener ; argv[]=/opt/evener hub --config=/srv/user.toml ; ignore_errors=no ; }\nWorkingDirectory=/srv\n"},
		{"/proc/4242/exe launch-check --json", goodLaunchCheck},
		{"generation", "kernel-boot\n12500\n"},
		{"selected-lock", "4242\n"},
	}
	return host, commands
}

func ownerRunner(t *testing.T, commands []ownerCommand) (*fakeRunner, *int) {
	index := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		if index >= len(commands) {
			t.Fatalf("unexpected extra command %v", argv)
		}
		got := strings.Join(argv[12:], " ")
		want := commands[index]
		switch want.command {
		case "selected-lock", "other-lock":
			root := "/srv/state"
			if want.command == "other-lock" {
				root = "/srv/other-state"
			}
			exact := "set -- $(stat -Lc '%d %i' " + root + "/hub.lock); [ \"$#\" -eq 2 ] || exit 1; dev=$1; ino=$2; major=$(( ((dev >> 8) & 4095) | ((dev >> 32) & 4294963200) )); minor=$(( (dev & 255) | ((dev >> 12) & 4294967040) )); key=$(printf '%02x:%02x:%s' \"$major\" \"$minor\" \"$ino\"); awk -v key=\"$key\" '$2 == \"FLOCK\" && $3 == \"ADVISORY\" && $4 == \"WRITE\" && $6 == key { print $5 }' /proc/locks"
			if got != exact {
				t.Fatalf("not a selected-root lock probe: %s", got)
			}
		case "generation":
			if got != "cat /proc/sys/kernel/random/boot_id && s=$(cat /proc/4242/stat) && s=${s##*) } && set -- $s && [ \"$#\" -ge 20 ] && shift 19 && printf '%s\\n' \"$1\"" {
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
	}{
		{"wrong record PID", 0, `"pid":4242`, `"pid":5151`},
		{"missing lock owner", 1, "4242", ""},
		{"ambiguous lock owner", 1, "4242", "4242\n5151"},
		{"wrong effective user", 3, "dev", "other"},
		{"wrong config", 4, "/srv/selected.toml", "/srv/other.toml"},
		{"wrong executable", 5, "/opt/evener", "/other/evener"},
		{"wrong service user", 11, "User=dev", "User=other"},
		{"wrong service config", 11, "/srv/selected.toml", "/srv/other.toml"},
		{"wrong service cwd", 11, "WorkingDirectory=/srv", "WorkingDirectory=/other"},
		{"unassociated supervisor", 11, "MainPID=4242", "MainPID=5151"},
		{"ambiguous definition", 11, " }", " } { path=/other ; }"},
		{"two associated services", 12, "MainPID=6161", "MainPID=4242"},
		{"changed start ticks", 14, "12500", "12501"},
		{"rebooted owner", 14, "kernel-boot", "other-boot"},
		{"changed lock owner", 15, "4242", "5151"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, commands := ownerCommands(t)
			if !strings.Contains(commands[tc.step].output, tc.old) {
				t.Fatal("invalid test input")
			}
			commands[tc.step].output = strings.ReplaceAll(commands[tc.step].output, tc.old, tc.replacement)
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
	fr, _ := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	got, err := m.resolveOwnedEndpoint(t.Context(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1})
	if err != nil || got.pid != "4242" || got.report.Running != nil {
		t.Fatalf("owner=%+v err=%v", got, err)
	}
}

func TestRemoteOwnerSelectsUserService(t *testing.T) {
	host, commands := ownerCommands(t)
	commands[11].output = strings.ReplaceAll(commands[11].output, "MainPID=4242", "MainPID=5151")
	commands[12].output = "Id=evener-hub-user.service\nMainPID=4242\nUser=\nExecStart={ path=/opt/evener ; argv[]=/opt/evener hub --config=/srv/selected.toml --addr=[::]:0 ; ignore_errors=no ; }\nWorkingDirectory=/srv\n"
	fr, _ := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	got, err := m.resolveOwnedEndpoint(t.Context(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1})
	if err != nil || got.supervisor != (supervisor{kind: supervisorSystemdUser, label: "evener-hub-user.service"}) {
		t.Fatalf("owner=%+v err=%v", got, err)
	}
}

func TestRemoteOwnerRevalidation(t *testing.T) {
	for _, change := range []string{"none", "generation", "root", "registration", "boot", "listener"} {
		t.Run(change, func(t *testing.T) {
			host, first := ownerCommands(t)
			_, second := ownerCommands(t)
			switch change {
			case "generation":
				second[2].output, second[14].output = "kernel-boot\n13000\n", "kernel-boot\n13000\n"
			case "root": // A different selected root cannot inherit authority from this snapshot.
				second[0].output = strings.ReplaceAll(second[0].output, "/srv/state", "/srv/other-state")
			case "boot":
				second[0].output = strings.ReplaceAll(second[0].output, "boot-one", "boot-two")
			case "listener":
				second[0].output = strings.ReplaceAll(second[0].output, "0.0.0.0:0", "[::]:0")
			}
			// Changing the root requires another independent lock lookup. Keep the
			// runner strict about the exact new root, not a default/old-port probe.
			if change == "root" {
				second[1].command, second[15].command = "other-lock", "other-lock"
			}
			fr, _ := ownerRunner(t, append(first, second...))
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			facts := Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1}
			prior, err := m.resolveOwnedEndpoint(t.Context(), host, facts)
			if err != nil {
				t.Fatal(err)
			}
			if change == "registration" {
				host.Generation++
			}
			_, err = m.revalidateOwnedEndpoint(t.Context(), host, facts, prior)
			if change == "none" || change == "listener" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, errOwnerEvidence) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
