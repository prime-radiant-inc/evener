package sshconn

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestSelectedPIDGuardsRejectHostileValues(t *testing.T) {
	for _, pid := range []string{"", " ", "-9", "--", "4242; marker", "/etc/passwd", "42 42", "$(printf marker)", "0x10", "42\n"} {
		t.Run(pid, func(t *testing.T) {
			if _, err := numericPID(pid); !errors.Is(err, ErrRestart) {
				t.Fatalf("numericPID(%q)=%v", pid, err)
			}
			host := hostreg.Host{Name: "selected", SSH: "dev@remote"}
			fr := &fakeRunner{runFn: func(context.Context, []string, io.Reader) ([]byte, error) {
				t.Fatal("invalid PID reached external command")
				return nil, nil
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			if _, err := m.recoverHubArgvRaw(t.Context(), host, pid); !errors.Is(err, ErrRestart) {
				t.Fatalf("recoverHubArgvRaw(%q)=%v", pid, err)
			}
			if len(fr.recordedRuns()) != 0 || len(fr.recordedStarts()) != 0 {
				t.Fatal("invalid PID issued a remote command")
			}
		})
	}
	for _, pid := range []string{"0", "4242", "999999"} {
		if got, err := numericPID(pid); err != nil || got != pid {
			t.Fatalf("numericPID(%q)=(%q,%v)", pid, got, err)
		}
	}
	if got := hubArgvRemote("4242"); got != "if [ -r /proc/4242/cmdline ]; then cat /proc/4242/cmdline; fi" {
		t.Fatalf("proc command=%q", got)
	}
	if got := processAliveRemote("4242"); !strings.HasPrefix(got, "if kill -s 0 -- 4242 ") {
		t.Fatalf("drain command=%q", got)
	}
}

func TestSelectedRestartRejectsHostileLockPID(t *testing.T) {
	for _, prefix := range []string{"", "4242\n"} {
		for _, pid := range []string{"-9", "; marker", "4242; marker", "/etc/passwd", "$(printf marker)", "4242 4242", "--", "0"} {
			t.Run(prefix+pid, func(t *testing.T) {
				host, base := ownerCommands(t)
				lock := ownerCommand{"selected-lock", prefix + pid + "\n"}
				lockRunner, lockIndex := ownerRunner(t, []ownerCommand{lock})
				lockManager := newTestManager(t, testRegistry(t, host), lockRunner, Options{})
				if got, err := lockManager.selectedLockPID(t.Context(), host, "/srv/state"); !errors.Is(err, errOwnerEvidence) || got != "" || *lockIndex != 1 {
					t.Fatalf("hostile lock PID=(%q,%v), commands=%d", got, err, *lockIndex)
				}
				commands := []ownerCommand{base[0], {"selected-lock", prefix + pid + "\n"}, {systemctlListUnits, ""}, {systemctlListUnitsUser, ""}}
				fr, index := ownerRunner(t, commands)
				m := newTestManager(t, testRegistry(t, host), fr, Options{})
				if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); !errors.Is(err, errOwnerEvidence) {
					t.Fatalf("hostile selected lock=%v", err)
				}
				if *index != len(commands) || len(fr.recordedStarts()) != 0 || m.pendingRestart(host.Name).selected != nil {
					t.Fatalf("hostile PID consumed=%d/%d pending=%+v", *index, len(commands), m.pendingRestart(host.Name))
				}
			})
		}
	}
}

func TestSelectedRestartRefusesUnsafeRecoveredArgv(t *testing.T) {
	for _, tc := range []struct{ name, proc, ps string }{
		{"readable unusable proc", "/opt/evener\x00hub\x00--config\x00\x00", ""},
		{"ambiguous ps", "", "/opt/evener hub --config /srv/my hub.toml\n"},
		{"compound ps", "", "/opt/evener hub; printf marker\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, base := ownerCommands(t)
			commands := append([]ownerCommand{}, base[:5]...)
			commands[4].output = tc.proc
			if tc.ps != "" {
				commands = append(commands, ownerCommand{"ps -p 4242 -ww -o command=", tc.ps})
			}
			fr, index := ownerRunner(t, commands)
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); !errors.Is(err, ErrRestart) {
				t.Fatalf("unsafe argv=%v", err)
			}
			// The ordered script ends at refusal: even a read-only lossy fallback for
			// readable/unusable proc bytes, or any kill/relaunch, is an extra command.
			if *index != len(commands) || len(fr.recordedStarts()) != 0 || m.pendingRestart(host.Name).selected != nil {
				t.Fatalf("unsafe argv consumed=%d/%d pending=%+v", *index, len(commands), m.pendingRestart(host.Name))
			}
		})
	}
}

func TestSelectedRestartRefusesProbedLoginUserMismatch(t *testing.T) {
	host, base := ownerCommands(t)
	host.SSH = "remote"
	commands := append([]ownerCommand{}, base[:3]...)
	commands = append(commands, ownerCommand{"id -un", "dev\n"}, ownerCommand{"ps -o user= -p 4242", "other\n"}, ownerCommand{"id -u other", "1001\n"}, ownerCommand{"id -u dev", "1000\n"})
	fr, index := ownerRunner(t, commands)
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	if _, err := m.RestartForOperation(t.Context(), host, selectedRestartFacts(), nil); !errors.Is(err, ErrRestart) {
		t.Fatalf("probed owner mismatch=%v", err)
	}
	if *index != len(commands) || len(fr.recordedStarts()) != 0 || m.pendingRestart(host.Name).selected != nil {
		t.Fatalf("user mismatch consumed=%d/%d pending=%+v", *index, len(commands), m.pendingRestart(host.Name))
	}
}
