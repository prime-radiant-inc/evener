package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

const recoveryDefaultExecutable = "/home/ops/.local/bin/evener"

func recoveryCommandExit(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != code {
		t.Fatalf("command-exit fixture: %v", err)
	}
	return &sshconn.RunError{Err: err}
}

func TestHostRestartRepairsFailedLaunchCheck(t *testing.T) {
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	commandFailure := recoveryCommandExit(t, 2)
	for _, scenario := range []struct {
		name             string
		failure          error
		postWriteFailure bool
		refuseRepair     bool
	}{
		{name: "repair", failure: commandFailure},
		{name: "post-write-contract-gate", failure: commandFailure, postWriteFailure: true},
		{name: "ssh-exit", failure: recoveryCommandExit(t, 255), refuseRepair: true},
		{name: "spawn", failure: errors.New("process did not start"), refuseRepair: true},
		{name: "cancelled", failure: context.Canceled, refuseRepair: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildB, "", "snapshot"
				runner := &restartRecoveryRunner{diskVersion: restartBuildA, runningVersion: restartBuildA, startedAt: restartStartA}
				m, manager, store, host, _ := publicRecoveryHub(t, runner, restartBuildB)
				if _, err := manager.Ensure(t.Context(), host.Name); err != nil {
					t.Fatal(err)
				}
				plan, err := m.Plan(t.Context(), appwire.HostPlanParams{Name: host.Name})
				if err != nil {
					t.Fatal(err)
				}
				deploy, err := m.Deploy(t.Context(), appwire.HostDeployParams{Name: host.Name, Token: plan.Token, OperationID: "interrupted-before-contract-failure"})
				if err != nil {
					t.Fatal(err)
				}
				m.cfg.opsWG.Wait()
				assertRestartRecoveryRecord(t, store, deploy.ID, hostops.StateFailed)
				assertPublicRecoveryEffects(t, runner, 1, 1, 0, restartBuildA, restartStartA)
				runner.mu.Lock()
				runner.launchError, runner.keepLaunchError = scenario.failure, scenario.postWriteFailure
				runner.mu.Unlock()
				buildinfo.GitSHA = restartBuildA // The pending request still owns B.
				params := appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "repair-failed-contract"}
				restart, err := m.Restart(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				m.cfg.opsWG.Wait()
				if scenario.postWriteFailure || scenario.refuseRepair {
					assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateFailed)
					writes := 2
					if scenario.refuseRepair {
						writes = 1
					}
					assertPublicRecoveryEffects(t, runner, writes, writes, 0, restartBuildA, restartStartA)
					runner.mu.Lock()
					runner.launchError = nil
					runner.mu.Unlock()
					params.OperationID = "retry-verified-contract"
					restart, err = m.Restart(t.Context(), params)
					if err != nil {
						t.Fatal(err)
					}
					m.cfg.opsWG.Wait()
				}
				assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateComplete)
				assertPublicRecoveryEffects(t, runner, 2, 2, 1, restartBuildB, restartStartB)
			})
		})
	}
}

func TestHostRestartRejectsFailedLaunchCheckWithoutDeployment(t *testing.T) {
	runner := &restartRecoveryRunner{diskVersion: restartBuildB, runningVersion: restartBuildA, startedAt: restartStartA, launchError: recoveryCommandExit(t, 2)}
	m, manager, store, host, _ := publicRecoveryHub(t, runner, restartBuildB)
	ch, err := manager.Ensure(t.Context(), host.Name)
	if err != nil {
		t.Fatalf("compatible Connect despite disk command failure: %v", err)
	}
	var live appwire.HostRunningResponse
	if err := ch.Client().Request(t.Context(), appwire.MethodEvenerHostRunning, appwire.HostRunningParams{}, &live); err != nil {
		t.Fatal(err)
	}
	if live.BuildRevision != restartBuildA || ch.Preflight().LaunchCheckKnown || ch.Preflight().Version != "" {
		t.Fatalf("live and disk identity: live=%+v disk=%+v", live, ch.Preflight())
	}
	restart, err := m.Restart(t.Context(), appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "no-authorized-installation"})
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.opsWG.Wait()
	assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateFailed)
	assertPublicRecoveryEffects(t, runner, 0, 0, 0, restartBuildA, restartStartA)
}

func TestHostRestartPreservesMissingDeploymentTarget(t *testing.T) {
	// Not parallel: the controller build stamp is process-wide.
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	for _, state := range []string{"missing", "nonexecutable"} {
		for _, recovery := range []string{"restart", "automatic"} {
			t.Run(state+"/"+recovery, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildB, "", "snapshot"
					base := &restartRecoveryRunner{diskVersion: restartBuildA, runningVersion: restartBuildA, startedAt: restartStartA}
					remote := newTwoInstallRecoveryRunner(t, base)
					host := deployTestHost()
					host.EvenerPath = ""
					m, manager, store, host, events := publicRecoveryHubForHost(t, base, remote, restartBuildB, host)
					ch, err := manager.Ensure(t.Context(), host.Name)
					if err != nil {
						t.Fatal(err)
					}
					waitPublicRecoveryEvent(t, events, sshconn.EventAttached)
					plan, err := m.Plan(t.Context(), appwire.HostPlanParams{Name: host.Name})
					if err != nil {
						t.Fatal(err)
					}
					deploy, err := m.Deploy(t.Context(), appwire.HostDeployParams{Name: host.Name, Token: plan.Token, OperationID: "interrupted-original-target"})
					if err != nil {
						t.Fatal(err)
					}
					m.cfg.opsWG.Wait()
					assertRestartRecoveryRecord(t, store, deploy.ID, hostops.StateFailed)
					assertPublicRecoveryEffects(t, base, 1, 1, 0, restartBuildA, restartStartA)
					original := remote.files[deployTestHost().EvenerPath]
					if state == "missing" {
						err = os.Remove(original)
					} else {
						err = os.Chmod(original, 0o600)
					}
					if err != nil {
						t.Fatal(err)
					}
					if recovery == "restart" {
						restart, err := m.Restart(t.Context(), appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "repair-original-target"})
						if err != nil {
							t.Fatal(err)
						}
						m.cfg.opsWG.Wait()
						assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateComplete)
					} else {
						if err := ch.Close(); err != nil {
							t.Fatal(err)
						}
						waitPublicRecoveryEvent(t, events, sshconn.EventAttached)
					}
					assertPublicRecoveryEffects(t, base, 2, 2, 1, restartBuildB, restartStartB)
					for target, want := range map[string]string{deployTestHost().EvenerPath: restartBuildB, recoveryDefaultExecutable: "second-installation"} {
						data, err := os.ReadFile(remote.files[target])
						if err != nil || string(data) != want {
							t.Errorf("installation %s: data=%q err=%v, want %q", target, data, err, want)
						}
					}
				})
			})
		}
	}
}

// The fixture owns two real files, with separate contents and executable bits.
// Only SSH/process execution is replaced, not recovery or deployment logic.
type twoInstallRecoveryRunner struct {
	*restartRecoveryRunner
	files map[string]string
}

func newTwoInstallRecoveryRunner(t *testing.T, base *restartRecoveryRunner) *twoInstallRecoveryRunner {
	t.Helper()
	r := &twoInstallRecoveryRunner{restartRecoveryRunner: base, files: map[string]string{}}
	for target, contents := range map[string]string{deployTestHost().EvenerPath: restartBuildA, recoveryDefaultExecutable: "second-installation"} {
		file := filepath.Join(t.TempDir(), "evener")
		if err := os.WriteFile(file, []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
		r.files[target] = file
	}
	return r
}

func (r *twoInstallRecoveryRunner) Run(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	command := strings.Join(argv, " ")
	target := deployTestHost().EvenerPath
	if strings.Contains(command, recoveryDefaultExecutable) {
		target = recoveryDefaultExecutable
	}
	switch {
	case strings.Contains(command, `"${HOME-}/.local/bin/evener"`):
		return []byte(recoveryDefaultExecutable + "\n"), nil
	case strings.Contains(command, "if test -x"), strings.Contains(command, "if command -v"):
		return nil, exec.CommandContext(ctx, "sh", "-c", `test -x "$1"`, "probe", r.files[target]).Run()
	case strings.Contains(command, "if test -e"):
		return nil, exec.CommandContext(ctx, "sh", "-c", `test -e "$1" || test -L "$1"`, "probe", r.files[target]).Run()
	case strings.Contains(command, " hub endpoint --json"):
		if strings.Contains(command, "/proc/") && strings.Contains(command, "/exe hub endpoint --json") {
			return r.restartRecoveryRunner.Run(ctx, argv, stdin)
		}
		if err := exec.CommandContext(ctx, "sh", "-c", `test -x "$1"`, "probe", r.files[target]).Run(); err != nil {
			return nil, err
		}
		return r.restartRecoveryRunner.Run(ctx, argv, stdin)
	case strings.Contains(command, "launch-check"):
		if err := exec.CommandContext(ctx, "sh", "-c", `test -x "$1"`, "probe", r.files[target]).Run(); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(r.files[target])
		if err != nil {
			return nil, err
		}
		return fmt.Appendf(nil, `{"hub_endpoint_schema":1,"protocol":%q,"version":%q,"launch_flags":["api-log"]}`, appwire.ProtocolVersion, string(data)), nil
	case strings.Contains(command, "test -d"), strings.Contains(command, "evener_resolve"):
		return []byte(target + "\n"), nil
	case strings.Contains(command, "cat >"):
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
		out, err := r.restartRecoveryRunner.Run(ctx, argv, strings.NewReader(string(data)))
		if err != nil {
			return out, err
		}
		if err := os.WriteFile(r.files[target], data, 0o700); err != nil {
			return nil, err
		}
		return out, os.Chmod(r.files[target], 0o700)
	default:
		return r.restartRecoveryRunner.Run(ctx, argv, stdin)
	}
}
