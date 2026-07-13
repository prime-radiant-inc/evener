package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/launchconfig"
	"primeradiant.com/serf/internal/credentials"
	"primeradiant.com/serf/rendezvous"
)

func fuzzExecutable(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "serf")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func FuzzSpawnLiveContracts(f *testing.F) {
	for i := byte(0); i < 24; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, mode byte) {
		ctx := context.Background()
		dir := t.TempDir()
		now := time.Now()
		entry := rendezvous.Entry{PID: 77, Address: "127.0.0.1:1", StartedAt: now.Add(time.Second)}
		_, _ = rendezvous.Write(dir, entry)
		_, _ = WaitForRendezvous(ctx, dir, 77)
		staleCtx, cancel := context.WithCancel(ctx)
		cancel()
		_, _ = WaitForRendezvous(staleCtx, dir, 77, WithStartedAfter(now.Add(2*time.Second)))
		exited := make(chan error, 1)
		if mode%2 == 0 {
			exited <- errors.New("exit")
		} else {
			exited <- nil
		}
		_, _ = waitForRendezvousOrExit(ctx, dir, 999, exited)

		var b tailBuffer
		b.limit = int(mode % 5)
		_, _ = b.Write([]byte("abcdef"))
		_, _ = b.Write([]byte("gh"))
		_ = b.String()
		_ = launchFailureError("launch", errors.New("boom secret"), []string{"", "detail", "boom secret"}[mode%3])
		_ = redactEnvSecrets("abcdefgh normal", []string{"API_KEY=abcdefgh", "PLAIN=normal", "TOKEN=x"})
		_ = isSensitiveEnvKey([]string{"key", "token", "secret", "password", "credential", "plain"}[mode%6])
		_, _ = envLookup([]string{"A=1", "A=2", "broken"}, "A")
		_ = envToMap([]string{"A=1", "broken"})

		r := launchconfig.Resolved{}
		r.Effective.SystemPromptMode = "inline"
		r.Effective.SystemPromptText = "system"
		r.Effective.SystemPromptAppendMode = "inline"
		r.Effective.SystemPromptAppendText = "append"
		_, _, _ = prepareResolvedForSpawn(dir, r)
		_, _, _ = prepareResolvedForSpawn("", r)
		_, _, _ = prepareResolvedForSpawn(filepath.Join(dir, "file"), r)
		plain := launchconfig.Resolved{}
		_, cleanup, _ := prepareResolvedForSpawn("", plain)
		cleanup()

		replay := 3
		r.Effective.AppReplaySize = &replay
		sreq := hubcore.SpawnRequest{WorkingDir: dir, StateDir: dir, RunDir: dir, AppReplaySize: 2, Resolved: r}
		rreq := hubcore.ResumeRequest{SessionID: "old", WorkingDir: dir, StateDir: dir, RunDir: dir, AppReplaySize: 2, Resolved: r}
		_ = buildSpawnArgs(sreq)
		_ = buildResumeArgs(rreq)
		_ = resolveSerfLaunchStateDir(dir, map[string]string{})
		_ = resolveSerfLaunchStateDir(dir, nil)

		store, _ := credentials.LoadStore(filepath.Join(dir, "credentials.toml"))
		for _, provider := range []string{"", "unknown", "openai", "openai-compatible", "ollama"} {
			_ = validateProviderCredentials(provider, store, []string{"OPENAI_API_KEY=abcdefgh", "OPENAI_COMPATIBLE_BASE_URL=http://x"}, "")
			_ = providerCredentialInEnv(provider, []string{"OPENAI_API_KEY=abcdefgh"})
		}
		_ = openAICompatibleBaseURLInEnv([]string{"OPENAI_COMPATIBLE_BASE_URL=http://x"})
		_ = openAIStoredOAuthUsable([]string{"SERF_STATE_DIR=" + dir})
		_ = openAIStateDirFromLaunchEnv(nil)
		_ = openAIInstanceOAuthUsable(dir, "missing")

		responses := []string{
			`printf '{"protocol":"` + appwire.ProtocolVersion + `"}'`,
			`printf '{"protocol":"wrong"}'`, `printf 'bad'`, `echo supersecret; exit 2`, `exit 2`,
			`printf '{"protocol":"` + appwire.ProtocolVersion + `","models":[{"provider":" p ","model":" m "},{"provider":"","model":"x"}],"diagnostics":[{"message":" hi ","provider":" p "},{"message":" "}]}'`,
		}
		bin := fuzzExecutable(t, responses[int(mode)%len(responses)])
		_, _ = listSerfLaunchModelContract(ctx, bin, []string{"API_KEY=supersecret"})
		_ = validateSerfLaunchContract(ctx, bin, " model ", []string{"API_KEY=supersecret"})
		_ = validateSerfLaunchContract(ctx, bin, "", nil)
		_, _ = listSerfLaunchModelContract(ctx, filepath.Join(dir, "missing"), nil)
		_ = validateSerfLaunchContract(ctx, filepath.Join(dir, "missing"), "", nil)

		bad := filepath.Join(dir, "missing")
		_, _ = SpawnDaemon(ctx, bad, dir, hubcore.SpawnRequest{}, time.Millisecond)
		_, _ = ResumeDaemon(ctx, bad, dir, hubcore.ResumeRequest{}, time.Millisecond)

		liveDir := t.TempDir()
		live := fuzzExecutable(t, `
if [ "$1" = "launch-check" ]; then
  case " $* " in *" --models "*) printf '{"protocol":"`+appwire.ProtocolVersion+`","models":[{"provider":"openai","model":"gpt"}]}' ;; *) printf '{"protocol":"`+appwire.ProtocolVersion+`"}' ;; esac
  exit 0
fi
rd=""
while [ $# -gt 0 ]; do if [ "$1" = "--run-dir" ]; then shift; rd="$1"; fi; shift; done
mkdir -p "$rd"
printf '{"pid":%s,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}' "$$" > "$rd/$$.json"
sleep 1
`)
		h := HubSpawner{SerfBinary: live, RunDir: liveDir}
		_, _ = h.ListLaunchModels(ctx)
		_, _ = h.ListLaunchModelContract(ctx)
		_, _ = h.ListLaunchModelContractForWorkingDir(ctx, dir)
		baseResolved := launchconfig.Resolved{}
		_, _ = h.Spawn(ctx, hubcore.SpawnRequest{WorkingDir: dir, StateDir: t.TempDir(), Resolved: baseResolved})
		_, _ = h.Resume(ctx, hubcore.ResumeRequest{SessionID: "old", WorkingDir: dir, StateDir: t.TempDir(), Resolved: baseResolved})
		_, _ = SpawnDaemon(ctx, live, liveDir, hubcore.SpawnRequest{RunDir: liveDir}, time.Second)
		_, _ = ResumeDaemon(ctx, live, liveDir, hubcore.ResumeRequest{RunDir: liveDir}, time.Second)

		exitBin := fuzzExecutable(t, `echo failed >&2; exit 3`)
		_, _ = SpawnDaemon(ctx, exitBin, liveDir, hubcore.SpawnRequest{}, time.Second)
		_, _ = ResumeDaemon(ctx, exitBin, liveDir, hubcore.ResumeRequest{}, time.Second)
		timeoutBin := fuzzExecutable(t, `sleep 2`)
		_, _ = SpawnDaemon(ctx, timeoutBin, liveDir, hubcore.SpawnRequest{}, time.Millisecond)
		_, _ = ResumeDaemon(ctx, timeoutBin, liveDir, hubcore.ResumeRequest{}, time.Millisecond)
	})
}
