package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/launchconfig"
	"primeradiant.com/serf/internal/credentials"
)

func FuzzSpawnCredentialOrchestrationPass4(f *testing.F) {
	for i := range byte(12) {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		root := t.TempDir()
		store, err := credentials.LoadStore(filepath.Join(root, "credentials.toml"))
		if err != nil {
			t.Fatal(err)
		}
		_ = store.Set("openrouter", "stored-key")

		// Exercise the no-config type map, including explicit launch-env
		// clearing, no-auth providers, aliases, and unknown providers.
		for _, tc := range []struct {
			provider string
			env      []string
		}{
			{"", nil}, {"openrouter", []string{"OPENROUTER_API_KEY="}},
			{"OpenRouter", []string{"OPENROUTER_API_KEY=from-launch"}},
			{"ollama", nil}, {"google", []string{"GOOGLE_API_KEY=g"}},
			{"unknown", nil}, {"openai-compatible", []string{"OPENAI_COMPATIBLE_BASE_URL= http://local/v1 "}},
		} {
			_ = validateProviderCredentials(tc.provider, store, tc.env, "")
		}
		_ = validateProviderCredentials("openrouter", nil, nil, "")

		cfgDir := t.TempDir()
		cfgPath := filepath.Join(cfgDir, "providers.toml")
		configs := []string{
			"not = [valid",
			"schema = 1\ndefault = \"inline\"\n[instances.inline]\ntype = \"openai\"\napi_key = \"inline-key\"\n",
			"schema = 1\ndefault = \"local\"\n[instances.local]\ntype = \"openai\"\napi_style = \"chat-completions\"\nbase_url = \"http://local/v1\"\n",
			"schema = 1\ndefault = \"local\"\n[instances.local]\ntype = \"openai\"\napi_style = \"chat-completions\"\n",
			"schema = 1\ndefault = \"work\"\n[instances.work]\ntype = \"openai\"\n",
			"schema = 1\ndefault = \"router\"\n[instances.router]\ntype = \"openrouter\"\n",
		}
		if err := os.WriteFile(cfgPath, []byte(configs[int(mode)%len(configs)]), 0o600); err != nil {
			t.Fatal(err)
		}
		env := []string{"XDG_STATE_HOME=" + root, "OPENAI_COMPATIBLE_BASE_URL=http://env/v1", "OPENROUTER_API_KEY=router-key"}
		for _, provider := range []string{"inline", "local", "work", "router", "absent"} {
			_ = validateProviderCredentials(provider, store, env, cfgPath)
		}
		_ = validateProviderCredentials("openrouter", store, nil, filepath.Join(root, "missing.toml"))

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
		_ = authopenai.SaveAuth(stateDir, "openai", validRecord(authopenai.AuthSourceOAuth, time.Now().Add(time.Hour), "refresh"))
		_ = openAIInstanceOAuthUsable(stateDir, "work")
		_ = openAIStoredOAuthUsable([]string{"XDG_STATE_HOME=" + root})
		_ = validateProviderCredentials("work", store, []string{"XDG_STATE_HOME=" + root, "OPENAI_API_KEY="}, cfgPath)
		_ = openAIInstanceOAuthUsable(filepath.Join(root, "missing"), "work")
		_ = openAICompatibleBaseURLInEnv(nil)
		_ = providerCredentialInEnv("openrouter", nil)

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
		h := HubSpawner{Cfg: Config{SpawnTimeout: time.Second}, SerfBinary: bin, RunDir: runDir, Creds: store}
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
