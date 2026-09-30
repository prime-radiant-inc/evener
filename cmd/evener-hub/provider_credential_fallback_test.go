package hub

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/rendezvous"
)

func TestHubLaunchCredentialFallbackWithProjectOverrides(t *testing.T) {
	for _, retained := range []bool{false, true} {
		state := "healthy"
		if retained {
			state = "retained"
		}
		t.Run(state, func(t *testing.T) {
			for _, override := range []string{"empty", "whitespace", "explicit"} {
				t.Run(override, func(t *testing.T) {
					for _, resume := range []bool{false, true} {
						mode := "spawn"
						if resume {
							mode = "resume"
						}
						t.Run(mode, func(t *testing.T) {
							f := retainedProviderFixture(t, retainedProviderConfig)
							if !retained {
								if err := os.WriteFile(f.tomlPath, []byte(retainedProviderConfig), 0o600); err != nil {
									t.Fatal(err)
								}
								if err := f.ctl.reg.Reload(); err != nil {
									t.Fatal(err)
								}
							}
							sibling := filepath.Join(filepath.Dir(f.tomlPath), "credentials.toml")
							fallback, err := credentials.LoadStore(sibling)
							if err != nil {
								t.Fatal(err)
							}
							if err := fallback.Set("work", "fallback-fixture-key"); err != nil {
								t.Fatal(err)
							}
							projectPath := filepath.Join(t.TempDir(), "credentials.toml")
							project, err := credentials.LoadStore(projectPath)
							if err != nil {
								t.Fatal(err)
							}
							if err := project.Set("work", "project-fixture-key"); err != nil {
								t.Fatal(err)
							}
							value, wantKey := "", "fallback-fixture-key"
							switch override {
							case "whitespace":
								value = " \t "
							case "explicit":
								value = projectPath
								wantKey = "project-fixture-key"
							}
							root := t.TempDir()
							runDir := filepath.Join(root, "run")
							binary := filepath.Join(root, "evener")
							writeFakeEvener(t, binary, retainedProviderDaemonScript)
							removed := make(chan string, 1)
							oldRemove := spawnRemoveAll
							spawnRemoveAll = func(path string) error {
								err := oldRemove(path)
								if strings.HasPrefix(filepath.Base(path), "retained-providers-") {
									removed <- path
								}
								return err
							}
							t.Cleanup(func() { spawnRemoveAll = oldRemove })
							h := &HubSpawner{Cfg: DefaultConfig(), Registry: f.ctl.reg, ProvidersConfigPath: f.tomlPath, CredentialsPath: f.credsPath, RunDir: runDir, EvenerBinary: binary}
							resolved := launchconfig.Resolved{Effective: launchconfig.Layer{Model: "work/house", Env: map[string]string{"EVENER_CREDENTIALS_CONFIG": value}}}
							var entry rendezvous.Entry
							if resume {
								entry, err = h.Resume(t.Context(), hubcore.ResumeRequest{SessionID: "01JFALLBACK", Provider: "work", StateDir: f.stateDir, WorkingDir: root, Resolved: resolved})
							} else {
								entry, err = h.Spawn(t.Context(), hubcore.SpawnRequest{Provider: "work", StateDir: f.stateDir, WorkingDir: root, Resolved: resolved})
							}
							if err != nil {
								t.Fatal(err)
							}
							child, err := os.FindProcess(entry.PID)
							if err != nil {
								t.Fatal(err)
							}
							t.Cleanup(func() {
								_ = child.Kill()
								if retained {
									select {
									case dir := <-removed:
										if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
											t.Error("child did not remove retained directory")
										}
									case <-time.After(10 * time.Second):
										t.Error("child snapshot cleanup did not finish")
									}
								}
							})
							raw, err := os.ReadFile(filepath.Join(runDir, "child-env"))
							if err != nil {
								t.Fatal(err)
							}
							for entry := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
								key, value, ok := strings.Cut(entry, "=")
								if ok {
									t.Setenv(key, value)
								}
							}
							r, _, err := cmdutil.LoadRegistry(registry.WithOffline(true), registry.WithoutCache(), registry.WithStateRoot(t.TempDir()))
							if err != nil {
								t.Fatal(err)
							}
							got, err := r.Resolve("work/house")
							if err != nil {
								t.Fatal(err)
							}
							if got.Credential.Source != "store" || got.Credential.Value != wantKey {
								t.Fatalf("child lost effective credential store: source=%s path=%s", got.Credential.Source, cmdutil.CredentialsPath())
							}
						})
					}
				})
			}
		})
	}
}
