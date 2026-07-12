//go:build serffuzz

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

type coverageAdapter struct{}

func (coverageAdapter) Name() string { return "OpenAI" }
func (coverageAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Message: llm.Assistant("done"), Finish: llm.FinishReason{Reason: llm.FinishReasonStop}}, nil
}
func (coverageAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// FuzzFluencyCoverage is the offline whole-command union seed. Its executable
// fixtures are local scripts and every provider client is empty or fake.
func FuzzFluencyCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		oldArgs := os.Args
		oldLoadClient := runnerLoadClient
		t.Cleanup(func() { runnerLoadClient = oldLoadClient })
		os.Args = []string{"serf-fluency", "help"}
		main()
		os.Args = oldArgs

		t.Setenv(envvars.SERFFluencyModel.Name, "x/y")
		_ = defaultModel()
		t.Setenv(envvars.SERFFluencyModel.Name, "")
		t.Setenv(envvars.SERFModel.Name, "a/b")
		_ = defaultModel()

		_ = run([]string{"catalog", "--bad-flag"})
		_ = run([]string{"run", "--bad-flag"})
		_ = run([]string{"run", "--harness", "bad"})
		file := filepath.Join(t.TempDir(), "file")
		mustWrite(t, file, "x")
		_ = run([]string{"run", "--out", filepath.Join(file, "child")})
		_ = run([]string{"run", "--probes-dir", filepath.Join(t.TempDir(), "missing"), "--out", t.TempDir(), "--serf-bin", file})

		_, _ = buildSerf(filepath.Join(file, "child"))
		_, _ = catalogTools("unknown/model")

		profile := provider.NewOpenAIProfile("m")
		_, _ = runnerApplyFastCheapModel(profile, "openai/cheap", llm.NewClient())
		client := llm.NewClient()
		client.Register(coverageAdapter{})
		_ = runnerClientHasProvider(client, "openai")
		_, _ = runnerInitialProfile(providercfg.Config{}, cmdutil.ModelRef{Provider: "missing", Model: "m"})

		restore := maybeClearOpenAIAPIKey(false)
		restore()
		t.Setenv(envvars.OpenAIAPIKey.Name, "secret")
		restore = maybeClearOpenAIAPIKey(true)
		restore()

		work := filepath.Join(file, "child")
		_ = materializeFixture(work, fixtureSpec{})
		_ = materializeFixture(t.TempDir(), fixtureSpec{Files: map[string]string{"a/b": "x"}})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var stdout, stderr bytes.Buffer
		_ = runCLIProbe(ctx, runConfig{serfBin: file, model: "p/m"}, probeFile{}, probeResult{}, &stdout, &stderr)

		cfg := runConfig{model: "p/m", harness: "cli", outDir: filepath.Join(t.TempDir(), "out"), serfBin: file, timeout: time.Second}
		_ = runProbe(cfg, probeFile{ID: "runtime"}, 1, nil)

		badOut := filepath.Join(t.TempDir(), "out-file")
		mustWrite(t, badOut, "x")
		_ = writeProbeResult(badOut, probeResult{Probe: "p", Repetition: 1})
		_ = writeSummary(badOut, nil)

		fakeClient := llm.NewClient()
		fakeClient.Register(coverageAdapter{})
		fakeCfg := providercfg.Config{Default: "OpenAI", Instances: []providercfg.InstanceConfig{{Name: "OpenAI", Type: "openai", APIStyle: providercfg.StyleResponses}}}
		runnerLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
			return fakeClient, fakeCfg, true, nil
		}
		t.Setenv(envvars.XDGConfigHome.Name, t.TempDir())
		liveRes := probeResult{StateDir: filepath.Join(t.TempDir(), "state"), WorkDir: t.TempDir()}
		var liveOut, liveErr bytes.Buffer
		_ = runLiveProbe(context.Background(), runConfig{model: "OpenAI/m", reasoningEffort: "low"}, probeFile{Prompt: "hello"}, &liveRes, &liveOut, &liveErr)
	})
}
