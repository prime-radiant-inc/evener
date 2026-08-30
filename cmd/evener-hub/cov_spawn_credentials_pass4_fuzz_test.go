package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/llm/registry"
)

func FuzzSpawnCredentialOrchestrationPass4(f *testing.F) {
	for i := range byte(12) {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		root := t.TempDir()

		// Walk the spawn gate over every registry shape: nothing named, a
		// credential-less curated provider, an instance on each auth scheme,
		// and a name nothing declares.
		stateDir := authopenai.DefaultStateDirWithStateHome(root)
		validRecord := func(source string, expiry time.Time, refresh string) authopenai.AuthRecord {
			return authopenai.AuthRecord{
				Version: 1, Provider: "openai", Source: source,
				ObtainedAt: time.Now().Add(-time.Hour), TokenType: "Bearer",
				AccessToken: "access", RefreshToken: refresh, Expiry: expiry,
			}
		}
		records := []authopenai.AuthRecord{
			validRecord(authopenai.AuthSourceOAuth, time.Now().Add(time.Hour), "refresh"),
			validRecord(authopenai.AuthSourceOAuth, time.Now().Add(-time.Hour), "refresh"),
			validRecord(authopenai.AuthSourceOAuth, time.Now().Add(-time.Hour), ""),
			validRecord(authopenai.AuthSourceEnv, time.Now().Add(time.Hour), "refresh"),
		}
		if err := authopenai.SaveAuth(stateDir, "work", records[int(mode)%len(records)]); err != nil {
			t.Fatal(err)
		}
		instanceSets := []map[string]registry.Provider{
			nil,
			{"inline": {Base: "openai", APIKey: "inline-key"}},
			{"local": {Base: "openai-compatible", Transport: registry.Transport{BaseURL: "http://local/v1", Auth: registry.AuthNone}}},
			{"local": {Base: "openai-compatible", Transport: registry.Transport{BaseURL: "http://local/v1", Auth: registry.AuthOptionalBearer}}},
			{"work": {Base: "openai-codex"}},
			{"router": {Base: "openrouter"}},
		}
		gate := newSpawnGateRegistry(t, stateDir,
			map[string]string{"OPENROUTER_API_KEY": "router-key"},
			instanceSets[int(mode)%len(instanceSets)])
		for _, provider := range []string{"", "inline", "local", "work", "router", "absent", "openrouter", "ollama"} {
			_ = validateProviderCredentials(provider, gate)
		}
		_ = validateProviderCredentials("openrouter", nil)
		_ = validateProviderCredentials("openrouter", hubcore.NewProviderRegistry(nil))

		// The executable makes the launch contract either succeed or fail. On
		// success it also publishes rendezvous for Spawn/Resume.
		body := strings.ReplaceAll(`
if [ "$1" = "launch-check" ]; then
  if [ "${FAIL_CHECK:-}" = "1" ]; then echo check-failed >&2; exit 2; fi
  printf '{"protocol":"PROTOCOL"}'
  exit 0
fi
rd=""
while [ $# -gt 0 ]; do if [ "$1" = "--run-dir" ]; then shift; rd="$1"; fi; shift; done
mkdir -p "$rd"
printf '{"pid":%s,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}' "$$" > "$rd/$$.json"
`, "PROTOCOL", appwire.ProtocolVersion)
		bin := fuzzExecutable(t, body)
		runDir := t.TempDir()
		h := HubSpawner{Cfg: Config{SpawnTimeout: time.Second}, EvenerBinary: bin, RunDir: runDir, Registry: gate}
		replay := 9
		resolved := launchconfig.Resolved{}
		resolved.Effective.AppReplaySize = &replay
		resolved.Effective.Env = map[string]string{"OPENROUTER_API_KEY": "launch-key"}
		spawn := hubcore.SpawnRequest{Provider: "openrouter", WorkingDir: root, Resolved: resolved}
		resume := hubcore.ResumeRequest{Provider: "openrouter", SessionID: "old", WorkingDir: root, Resolved: resolved}
		switch mode % 3 {
		case 0:
			spawn.Resolved.Effective.Env["FAIL_CHECK"] = "1"
			resume.Resolved.Effective.Env["FAIL_CHECK"] = "1"
		case 1:
			spawn.Resolved.Effective.Env["OPENROUTER_API_KEY"] = ""
			resume.Resolved.Effective.Env["OPENROUTER_API_KEY"] = ""
		}
		_, _ = h.Spawn(context.Background(), spawn)
		_, _ = h.Resume(context.Background(), resume)

		// Resume skips credential validation when the persisted provider is used.
		resume.Provider = ""
		resume.Resolved.Effective.Env = map[string]string{}
		_, _ = h.Resume(context.Background(), resume)
		if strings.Contains(string(mode), "impossible") {
			t.Fatal("unreachable")
		}
	})
}
