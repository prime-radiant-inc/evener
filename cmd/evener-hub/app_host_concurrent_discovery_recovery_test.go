package hub

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// Plan's ungated Preflight can discover another installation while a separately
// admitted deployment acquires ownership of its original target. Exercise that
// interleaving at the SSH boundary, without seeding the manager's pending state.
func TestHostRestartPreservesTargetDuringConcurrentDiscovery(t *testing.T) {
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	for _, state := range []string{"missing", "nonexecutable", "usable", "configured", "no-owner"} {
		t.Run(state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildB, "", "snapshot"
				base := &restartRecoveryRunner{diskVersion: restartBuildA, runningVersion: restartBuildA, startedAt: restartStartA}
				files := newTwoInstallRecoveryRunner(t, base)
				remote := &discoveryBarrierRunner{Runner: files, entered: make(chan struct{}), resume: make(chan struct{})}
				releaseDiscovery := sync.OnceFunc(func() { close(remote.resume) })
				defer releaseDiscovery()
				host := deployTestHost()
				if state != "configured" {
					host.EvenerPath = ""
				}
				m, manager, store, host, _ := publicRecoveryHubForHost(t, base, remote, restartBuildB, host)
				if _, err := manager.Ensure(t.Context(), host.Name); err != nil {
					t.Fatal(err)
				}
				plan, err := m.Plan(t.Context(), appwire.HostPlanParams{Name: host.Name})
				if err != nil {
					t.Fatal(err)
				}
				original := files.files[deployTestHost().EvenerPath]
				if err := os.Remove(original); err != nil {
					t.Fatal(err)
				}
				type outcome struct {
					facts sshconn.Preflight
					err   error
				}
				result := make(chan outcome, 1)
				go func() {
					facts, err := manager.Preflight(t.Context(), host)
					result <- outcome{facts: facts, err: err}
				}()
				select {
				case <-remote.entered: // No pending effect existed when discovery began.
				case early := <-result:
					t.Fatalf("preflight did not reach default discovery: %+v", early)
				}
				if state != "no-owner" {
					// Restore A for the admitted deployment's independent target checks.
					if err := os.WriteFile(original, []byte(restartBuildA), 0o700); err != nil {
						t.Fatal(err)
					}
					deploy, err := m.Deploy(t.Context(), appwire.HostDeployParams{Name: host.Name, Token: plan.Token, OperationID: "own-original-during-discovery"})
					if err != nil {
						t.Fatal(err)
					}
					m.cfg.opsWG.Wait()
					assertRestartRecoveryRecord(t, store, deploy.ID, hostops.StateFailed)
					assertPublicRecoveryEffects(t, base, 1, 1, 0, restartBuildA, restartStartA)
					switch state {
					case "missing", "configured":
						err = os.Remove(original)
					case "nonexecutable":
						err = os.Chmod(original, 0o600)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				releaseDiscovery()
				observed := <-result
				if observed.err != nil {
					t.Fatal(observed.err)
				}
				if state == "no-owner" {
					if !observed.facts.LaunchCheckKnown || observed.facts.Version != "second-installation" || observed.facts.ExecutableMissing {
						t.Errorf("unowned default discovery: %+v", observed.facts)
					}
					assertPublicRecoveryEffects(t, base, 0, 0, 0, restartBuildA, restartStartA)
				} else {
					if state == "usable" {
						if !observed.facts.LaunchCheckKnown || observed.facts.Version != restartBuildA || observed.facts.ExecutableMissing {
							t.Errorf("discovery returned facts from unowned installation: %+v", observed.facts)
						}
					} else if !observed.facts.ExecutableMissing || observed.facts.LaunchCheckKnown || observed.facts.Version != "" {
						t.Errorf("missing owned installation replaced by default facts: %+v", observed.facts)
					}
					restart, err := m.Restart(t.Context(), appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "recover-owned-original"})
					if err != nil {
						t.Fatal(err)
					}
					m.cfg.opsWG.Wait()
					assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateComplete)
					assertPublicRecoveryEffects(t, base, 2, 2, 1, restartBuildB, restartStartB)
					data, err := os.ReadFile(original)
					if err != nil || string(data) != restartBuildB {
						t.Errorf("original installation: data=%q err=%v", data, err)
					}
				}
				data, err := os.ReadFile(files.files[recoveryDefaultExecutable])
				if err != nil || string(data) != "second-installation" {
					t.Errorf("second installation changed: data=%q err=%v", data, err)
				}
			})
		})
	}
}

type discoveryBarrierRunner struct {
	sshconn.Runner
	entered, resume chan struct{}
	once            sync.Once
}

func (r *discoveryBarrierRunner) Run(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	if strings.Contains(strings.Join(argv, " "), `"${HOME-}/.local/bin/evener"`) {
		r.once.Do(func() {
			close(r.entered)
			select {
			case <-r.resume:
			case <-ctx.Done():
			}
		})
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return r.Runner.Run(ctx, argv, stdin)
}
