package hub

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// Keep the allowlist exact. Execute the received command, not a canned response,
// so production's framing guard and parameter expansion see the fixture values.
const hostFixtureEnvironmentProbe = `case "${HOME-}${XDG_STATE_HOME-}${XDG_CONFIG_HOME-}" in *'
'*) exit 1 ;; esac; printf '%s\n' "HOME=${HOME-}" "XDG_STATE_HOME=${XDG_STATE_HOME-}" "XDG_CONFIG_HOME=${XDG_CONFIG_HOME-}"`

func runHostFixtureEnvironmentProbe(ctx context.Context, command string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Env = env
	return cmd.Output()
}

func TestHostRestartFixtureEnvironmentProbe(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"unset", []string{}, "HOME=\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"},
		{"values", []string{"HOME=/fixture/home", "XDG_STATE_HOME=/fixture/state", "XDG_CONFIG_HOME=/fixture/config"}, "HOME=/fixture/home\nXDG_STATE_HOME=/fixture/state\nXDG_CONFIG_HOME=/fixture/config\n"},
		{"pathname-bytes", []string{"HOME=/fixture/home \t\r", "XDG_STATE_HOME=/fixture/state ", "XDG_CONFIG_HOME=/fixture/config "}, "HOME=/fixture/home \t\r\nXDG_STATE_HOME=/fixture/state \nXDG_CONFIG_HOME=/fixture/config \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runHostFixtureEnvironmentProbe(t.Context(), hostFixtureEnvironmentProbe, tc.env)
			if err != nil || string(out) != tc.want {
				t.Fatalf("probe output=%q err=%v, want %q", out, err, tc.want)
			}
		})
	}
	for _, key := range []string{"HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
		t.Run(key+"-newline", func(t *testing.T) {
			out, err := runHostFixtureEnvironmentProbe(t.Context(), hostFixtureEnvironmentProbe, []string{key + "=/fixture/path\n"})
			if err == nil || len(out) != 0 {
				t.Fatalf("newline pathname escaped framing guard: output=%q err=%v", out, err)
			}
		})
	}
}

func TestHostRestartFixtureEnvironmentProbeAllowlists(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		runner     sshconn.Runner
	}{
		{"restart-recovery", "HOME=/home/ops\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n", &restartRecoveryRunner{}},
		{"probe", "HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n", &probeFakeRunner{}},
		{"attached-update", "HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n", &attachedUpdateRunner{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := append(make([]string, 12), hostFixtureEnvironmentProbe)
			out, err := tc.runner.Run(t.Context(), argv, nil)
			if err != nil || string(out) != tc.want {
				t.Fatalf("fixture output=%q err=%v, want %q", out, err, tc.want)
			}
			// A known command with an extra command is not an allowlisted probe.
			argv[12] += "; printf unexpected"
			if _, err := tc.runner.Run(t.Context(), argv, nil); err == nil || !strings.Contains(err.Error(), "unexpected remote command") {
				t.Fatalf("unknown command lost strict refusal: %v", err)
			}
		})
	}
}

func TestHostRestartFixtureDirectoryProbe(t *testing.T) {
	runner := &restartRecoveryRunner{}
	argv := append(make([]string, 12), "CDPATH='' cd -P -- /home/ops && pwd -P")
	out, err := runner.Run(t.Context(), argv, nil)
	if err != nil || string(out) != "/home/ops\n" {
		t.Fatalf("physical fixture cwd=%q err=%v", out, err)
	}
	argv[12] = "CDPATH='' cd -P -- /home/other && pwd -P"
	if _, err := runner.Run(t.Context(), argv, nil); err == nil || !strings.Contains(err.Error(), "unexpected remote command") {
		t.Fatalf("different cwd lost strict refusal: %v", err)
	}
}
