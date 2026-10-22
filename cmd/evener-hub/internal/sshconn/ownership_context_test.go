package sshconn

import (
	"errors"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestRemoteStoppedUserEnvironmentInheritsManager(t *testing.T) {
	host := hostreg.Host{Name: "selected", SSH: "dev@remote"}
	commands := []ownerCommand{
		{"systemctl --user show --property=User,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- evener-hub.service", "User=dev\nEnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\nSetLoginEnvironment=yes\n"},
		{"getent passwd dev | cut -d: -f6", "/home/dev\n"},
		{"values=$(systemctl --user show-environment) || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter, "XDG_STATE_HOME=/service/state\x00EVENER_HUB_LISTEN_ADDR=127.0.0.1:0\x00"},
		{"values=$(systemctl --user show --property=Environment --value -- evener-hub.service) || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter, "EVENER_HUB_LISTEN_ADDR=[::]:0\x00"},
	}
	fr, index := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	env, err := m.stoppedServiceEnvironment(t.Context(), host, supervisor{kind: supervisorSystemdUser, label: "evener-hub.service"}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(env, []string{"EVENER_HUB_LISTEN_ADDR=[::]:0", "HOME=/home/dev", "XDG_STATE_HOME=/service/state"}) || *index != len(commands) {
		t.Fatalf("env=%q commands=%d", env, *index)
	}
}

func TestRemoteOwnerNarrowEnvironmentFilters(t *testing.T) {
	t.Run("Linux process environment", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("process environment probe requires Linux /proc")
		}
		cmd := exec.Command("/bin/sh", "-c", ownerEnvironmentRemote("$$"))
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/private/home", "EVENER_HUB_LISTEN_ADDR=127.0.0.1:0", "PRIVATE_TEST_SECRET=must-not-cross-boundary"}
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		env, err := decodeOwnerEnvironment(out)
		if err != nil || !slices.Equal(env, []string{"EVENER_HUB_LISTEN_ADDR=127.0.0.1:0", "HOME=/private/home"}) {
			t.Fatalf("narrow process filter returned unexpected fields, error=%v", err)
		}
	})
	t.Run("systemd serialization", func(t *testing.T) {
		cmd := exec.Command("/bin/sh", "-c", narrowEnvironmentFilter)
		cmd.Stdin = strings.NewReader(`HOME=/private/home "EVENER_HUB_LISTEN_ADDR=[::]:0" "PRIVATE_TEST_SECRET=must not cross boundary"`)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		env, err := decodeOwnerEnvironment(out)
		if err != nil || !slices.Equal(env, []string{"EVENER_HUB_LISTEN_ADDR=[::]:0", "HOME=/private/home"}) {
			t.Fatalf("narrow systemd filter returned unexpected fields, error=%v", err)
		}
	})
}

// A damaged generation cannot inherit a cached witness from a reused PID.
func TestRemoteRestartGenerationCapability(t *testing.T) {
	host, first := ownerCommands(t)
	_, second := ownerCommands(t)
	damaged := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:19371", "boot-one")
	damaged.State, damaged.Reason, damaged.Running = "unavailable", "unreachable", nil
	second[0].output = mustOwnerJSON(t, damaged)
	second[2].output, second[16].output = "kernel-boot\n14000\n", "kernel-boot\n14000\n"
	second = append(second[:16], ownerCommand{"/proc/4242/exe launch-check --json", `{"protocol":"old","version":"old"}`})
	commands := first
	commands = append(commands, second...)
	fr, index := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	if _, err := m.resolveOwnedEndpoint(t.Context(), host, selectedRestartFacts()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); !errors.Is(err, ErrEndpointMigration) {
		t.Fatalf("reused old generation restart=%v", err)
	}
	if *index != len(commands) || m.pendingRestart(host.Name).selected != nil || len(fr.recordedStarts()) != 0 {
		t.Fatal("reused generation gained mutation authority")
	}
}
