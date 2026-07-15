//go:build serffuzz

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/doctor"
	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/cmdutil"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

type coverageAdapter struct{}

func (coverageAdapter) Name() string { return "openai" }
func (coverageAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{coverageToolCall("communicate", `{"message":"done","end_turn":true}`)}}, Finish: llm.FinishReason{Reason: llm.FinishReasonToolCalls}}, nil
}

type coverageAppendFile struct {
	writeErr error
	closeErr error
}

func (f coverageAppendFile) Write(p []byte) (int, error) { return 0, f.writeErr }
func (f coverageAppendFile) Close() error                { return f.closeErr }

type coverageKickProcessor struct {
	out   string
	err   error
	timer chan<- time.Time
}

func (p coverageKickProcessor) ProcessInputKind(context.Context, string, []agent.ImageAttachment, agent.EntryKind) (string, error) {
	if p.timer != nil {
		p.timer <- time.Time{}
	}
	return p.out, p.err
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
		oldExit := exitProcess
		oldNewSession := runnerNewSession
		oldAttach := runnerAttachAPILogger
		oldMarshalEvent := runnerMarshalEvent
		oldReadTranscript := runnerReadTranscript
		oldMarshalProbe := runnerMarshalProbeResult
		oldOpenAppend := runnerOpenResultAppend
		oldMarshalSummary := runnerMarshalSummary
		t.Cleanup(func() {
			runnerLoadClient = oldLoadClient
			exitProcess = oldExit
			runnerNewSession = oldNewSession
			runnerAttachAPILogger = oldAttach
			runnerMarshalEvent = oldMarshalEvent
			runnerReadTranscript = oldReadTranscript
			runnerMarshalProbeResult = oldMarshalProbe
			runnerOpenResultAppend = oldOpenAppend
			runnerMarshalSummary = oldMarshalSummary
		})
		os.Args = []string{"serf-fluency", "help"}
		main()
		os.Args = oldArgs
		exitProcess = func(int) {}
		os.Args = []string{"serf-fluency", "bogus"}
		main()
		os.Args = oldArgs
		exitProcess = oldExit
		for _, args := range [][]string{nil, {"bogus"}, {"-h"}, {"--help"}} {
			_ = run(args)
		}

		t.Setenv(envvars.SERFFluencyModel.Name, "x/y")
		_ = defaultModel()
		t.Setenv(envvars.SERFFluencyModel.Name, "")
		t.Setenv(envvars.SERFModel.Name, "a/b")
		_ = defaultModel()

		_ = run([]string{"catalog", "--bad-flag"})
		_ = run([]string{"run", "--bad-flag"})
		_ = run([]string{"run", "--harness", "bad"})
		_ = run([]string{"run", "--repetitions", "0"})
		file := filepath.Join(t.TempDir(), "file")
		mustWrite(t, file, "x")
		_ = run([]string{"run", "--out", filepath.Join(file, "child")})
		_ = run([]string{"run", "--probes-dir", filepath.Join(t.TempDir(), "missing"), "--out", t.TempDir(), "--serf-bin", file})

		_, _ = buildSerf(filepath.Join(file, "child"))
		_, _ = catalogTools("unknown/model")
		_, _ = catalogTools("openai/gpt-5.4-mini")
		runnerNewSession = func(*llm.Client, *provider.Profile, execenv.ExecutionEnvironment, agent.SessionConfig) (*agent.Session, error) {
			return nil, errors.New("new session")
		}
		_, _ = catalogTools("openai/gpt-5.4-mini")
		runnerNewSession = oldNewSession
		_ = runCatalog([]string{"--model", "openai/gpt-5.4-mini"})
		_ = runCatalog([]string{"--model", "openai/gpt-5.4-mini", "--json"})
		_ = runCatalog([]string{"--model", "bad"})
		_, _ = catalogTools("bad")
		oldStdout := os.Stdout
		full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		os.Stdout = full
		_ = runCatalog([]string{"--model", "openai/gpt-5.4-mini", "--json"})
		os.Stdout = oldStdout
		_ = full.Close()
		tmpFile := filepath.Join(t.TempDir(), "tmp-file")
		mustWrite(t, tmpFile, "x")
		t.Setenv("TMPDIR", tmpFile)
		_, _ = catalogTools("openai/gpt-5.4-mini")
		t.Setenv("TMPDIR", "")

		profile := provider.NewOpenAIProfile("m")
		_, _ = runnerApplyFastCheapModel(profile, "openai/cheap", llm.NewClient())
		client := llm.NewClient()
		client.Register(coverageAdapter{})
		_ = runnerClientHasProvider(client, "openai")
		_ = runnerClientHasProvider(client, "OPENAI")
		_ = runnerClientHasProvider(nil, "openai")
		_, _ = runnerInitialProfile(providercfg.Config{}, cmdutil.ModelRef{Provider: "missing", Model: "m"})
		_, _ = runnerInitialProfile(providercfg.Config{}, cmdutil.ModelRef{Provider: "openai", Model: "m"})
		_, _ = runnerApplyFastCheapModel(nil, "x", nil)
		_, _ = runnerApplyFastCheapModel(profile, " ", nil)
		_, _ = runnerApplyFastCheapModel(profile, "other/cheap", llm.NewClient())
		_, _ = runnerApplyFastCheapModel(profile, "openai/cheap", client)

		restore := maybeClearOpenAIAPIKey(false)
		restore()
		t.Setenv(envvars.OpenAIAPIKey.Name, "secret")
		restore = maybeClearOpenAIAPIKey(true)
		restore()
		_ = os.Unsetenv(envvars.OpenAIAPIKey.Name)
		restore = maybeClearOpenAIAPIKey(true)
		restore()

		work := filepath.Join(file, "child")
		_ = materializeFixture(work, fixtureSpec{})
		_ = materializeFixture(t.TempDir(), fixtureSpec{Files: map[string]string{"a/b": "x"}})
		_ = materializeFixture(t.TempDir(), fixtureSpec{Files: map[string]string{"../escape": "x"}})
		fixtureFile := filepath.Join(t.TempDir(), "fixture-file")
		mustWrite(t, fixtureFile, "x")
		_ = materializeFixture(filepath.Join(fixtureFile, "child"), fixtureSpec{})
		fixtureWork := t.TempDir()
		mustWrite(t, filepath.Join(fixtureWork, "blocked"), "x")
		_ = materializeFixture(fixtureWork, fixtureSpec{Files: map[string]string{"blocked/child": "x"}})
		if err := os.Mkdir(filepath.Join(fixtureWork, "as-dir"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = materializeFixture(fixtureWork, fixtureSpec{Files: map[string]string{"as-dir": "x"}})

		probes := t.TempDir()
		mustWrite(t, filepath.Join(probes, "ignored.txt"), "x")
		mustWrite(t, filepath.Join(probes, "b.yml"), "schema: 1\nid: beta\nprompt: b\n")
		mustWrite(t, filepath.Join(probes, "a.yaml"), "schema: 1\nid: alpha\nprompt: a\n")
		_, _ = loadProbes(probes, "all")
		_, _ = loadProbes(probes, "beta")
		badProbes := t.TempDir()
		mustWrite(t, filepath.Join(badProbes, "bad.yaml"), "schema: [\n")
		_, _ = loadProbes(badProbes, "all")
		mustWrite(t, filepath.Join(badProbes, "bad.yaml"), "schema: 1\n")
		_, _ = loadProbes(badProbes, "all")
		_, _ = loadProbes(filepath.Join(probes, "missing"), "all")
		if err := os.Symlink(filepath.Join(probes, "missing-target"), filepath.Join(probes, "broken.yaml")); err != nil {
			t.Fatal(err)
		}
		_, _ = loadProbes(probes, "all")

		_, _, _ = parseEvents([]byte(strings.Join([]string{
			"noise", "", "{bad",
			`{"kind":"TOOL_CALL_START","data":{"tool_name":"shell"}}`,
			`{"kind":"TOOL_CALL_START","data":{}}`,
			`{"kind":"TOOL_CALL_END","data":{"tool_name":"shell","error":"boom"}}`,
			`{"kind":"TOOL_CALL_END","data":{"tool_name":"shell"}}`,
			`{"kind":"COMMUNICATE","data":{"message":"done now"}}`,
			`{"kind":"COMMUNICATE","data":{}}`,
		}, "\n")))
		_ = formatCounts(nil)
		_ = formatCounts(map[string]int{"z": 2, "a": 1})
		_ = safeName("")
		_ = safeName(" a/b c! ")
		for _, text := range []string{"insufficient_quota", "rate limit", "429", "no provider", "unknown provider", "api key", "ordinary"} {
			_ = looksInfraError(text)
		}
		_ = unavailableFinding(probeFile{}, nil)
		_ = unavailableFinding(probeFile{Skip: map[string]string{"if_unavailable": "shell"}}, map[string]bool{"shell": true})
		_ = unavailableFinding(probeFile{Skip: map[string]string{"if_unavailable": "shell"}}, nil)

		expectWork := t.TempDir()
		mustWrite(t, filepath.Join(expectWork, "artifact.txt"), "hello world")
		yes, no := true, false
		expectProbe := probeFile{Expect: expectSpec{
			Calls: []expectedCall{{Tool: "read", Min: 2}, {Tool: "write"}}, ForbiddenCalls: []string{"shell"},
			Artifacts:     []artifactExpect{{Path: "artifact.txt", Exists: &yes, Contains: "missing"}, {Path: "missing.txt", Exists: &no}, {Path: "absent.txt", Contains: "x"}},
			FinalContains: []string{"absent", "done"},
		}}
		expectRes := probeResult{FinalOutput: "final", CommunicateMessages: []string{"done via message"}, CanonicalToolCounts: map[string]int{"read": 1, "shell": 1}, ModelToolCounts: map[string]int{"read": 3}, ToolErrors: map[string]int{"read": 1, "zero": 0}}
		_ = evaluateExpectations(expectWork, expectProbe, expectRes)
		_ = resultContains(expectRes, "final")
		_ = resultContains(expectRes, "done")
		_ = resultContains(expectRes, "never")
		_ = evaluateExpectations(expectWork, probeFile{Expect: expectSpec{Artifacts: []artifactExpect{{Path: "artifact.txt", Contains: "world"}}}}, probeResult{})
		_ = evaluateExpectations(expectWork, probeFile{Expect: expectSpec{Artifacts: []artifactExpect{{Path: "artifact.txt", Exists: &no}}}}, probeResult{})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var stdout, stderr bytes.Buffer
		_ = runCLIProbe(ctx, runConfig{serfBin: file, model: "p/m"}, probeFile{}, probeResult{}, &stdout, &stderr)
		bin := filepath.Join(t.TempDir(), "fake-serf")
		mustWrite(t, bin, "#!/bin/sh\nprintf 'ok final\\n'\nprintf '%s\\n' '{\"kind\":\""+string(events.EventToolCallStart)+"\",\"data\":{\"tool_name\":\"read\"}}' >&2\n")
		if err := os.Chmod(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		stdout.Reset()
		stderr.Reset()
		cliCfg := runConfig{serfBin: bin, model: "openai/m", fastCheapModel: "openai/cheap", systemPromptAppend: []string{"a", " ", "b"}, reasoningEffort: "low", clearOpenAIAPIKey: true}
		_ = runCLIProbe(context.Background(), cliCfg, probeFile{Prompt: "p"}, probeResult{WorkDir: t.TempDir(), StateDir: t.TempDir()}, &stdout, &stderr)
		_ = cliProbeArgs(cliCfg, probeFile{Prompt: "p"}, probeResult{WorkDir: "w", StateDir: "s"})

		cfg := runConfig{model: "p/m", harness: "cli", outDir: filepath.Join(t.TempDir(), "out"), serfBin: file, timeout: time.Second}
		_ = runProbe(cfg, probeFile{ID: "runtime"}, 1, nil)
		cfg = runConfig{model: "openai/m", harness: "cli", outDir: filepath.Join(t.TempDir(), "out"), serfBin: bin, timeout: time.Second, reasoningEffort: "low"}
		passProbe := probeFile{ID: "pass", Prompt: "hello", Fixture: fixtureSpec{Files: map[string]string{"a.txt": "a"}}, Expect: expectSpec{Calls: []expectedCall{{Tool: "read"}}, FinalContains: []string{"ok"}}}
		_ = runProbe(cfg, passProbe, 1, map[string]bool{})
		_ = runProbe(cfg, probeFile{ID: "skip", Skip: map[string]string{"if_unavailable": "missing"}}, 1, nil)
		_ = runProbe(cfg, probeFile{ID: "fixture", Fixture: fixtureSpec{Files: map[string]string{"../bad": "x"}}}, 1, nil)
		failBin := filepath.Join(t.TempDir(), "fail-serf")
		mustWrite(t, failBin, "#!/bin/sh\nprintf 'rate limit' >&2\nexit 1\n")
		if err := os.Chmod(failBin, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg.serfBin = failBin
		_ = runProbe(cfg, probeFile{ID: "infra"}, 1, nil)
		plainFailBin := filepath.Join(t.TempDir(), "plain-fail-serf")
		mustWrite(t, plainFailBin, "#!/bin/sh\nexit 1\n")
		if err := os.Chmod(plainFailBin, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg.serfBin = plainFailBin
		_ = runProbe(cfg, probeFile{ID: "runtime-failure"}, 1, nil)
		cancelCfg := cfg
		cancelCfg.timeout = 0
		_ = runProbe(cancelCfg, probeFile{ID: "timeout"}, 1, nil)

		badOut := filepath.Join(t.TempDir(), "out-file")
		mustWrite(t, badOut, "x")
		_ = writeProbeResult(badOut, probeResult{Probe: "p", Repetition: 1})
		_ = writeSummary(badOut, nil)
		resultDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(resultDir, "results.jsonl"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = writeProbeResult(resultDir, probeResult{Probe: "p", Repetition: 1})
		fullDir := t.TempDir()
		if err := os.Symlink("/dev/full", filepath.Join(fullDir, "results.jsonl")); err != nil {
			t.Fatal(err)
		}
		_ = writeProbeResult(fullDir, probeResult{Probe: "p", Repetition: 1})
		runnerMarshalProbeResult = func(probeResult, string, string) ([]byte, error) { return nil, errors.New("marshal") }
		_ = writeProbeResult(t.TempDir(), probeResult{Probe: "p", Repetition: 1})
		runnerMarshalProbeResult = oldMarshalProbe
		runnerOpenResultAppend = func(string, int, os.FileMode) (resultAppendFile, error) {
			return coverageAppendFile{writeErr: errors.New("write"), closeErr: errors.New("close")}, nil
		}
		_ = writeProbeResult(t.TempDir(), probeResult{Probe: "p", Repetition: 1})
		runnerOpenResultAppend = oldOpenAppend
		runnerMarshalSummary = func(any, string, string) ([]byte, error) { return nil, errors.New("marshal") }
		_ = writeSummary(t.TempDir(), nil)
		runnerMarshalSummary = oldMarshalSummary
		goodOut := t.TempDir()
		persisted := probeResult{Schema: 1, Probe: "a/b", Model: "m", Repetition: 2, Status: "passed"}
		_ = writeProbeResult(goodOut, persisted)
		_ = writeSummary(goodOut, []probeResult{persisted})

		state := t.TempDir()
		old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		const childID = "02wMz5Txv1C3Hut0M8GCeB"
		const parentID = "02wMz5Txv2enqVTitaig6F"
		const parentedID = "02wMz5Txv3fQm7JtYx4a8P"
		const newID = "02wMz5Txv4gRz8KuZb5c9Q"
		const oldID = "02wMz5Txv5hSa9LvAc6d0R"
		for _, meta := range []schema.SessionMeta{{ID: childID, IsSubagent: true, ParentSessionID: parentID, CreatedAt: old.Add(-time.Hour)}, {ID: parentedID, ParentSessionID: parentID, CreatedAt: old.Add(-time.Hour)}, {ID: newID, CreatedAt: old.Add(time.Hour)}, {ID: oldID, CreatedAt: old}} {
			if err := schema.SaveSessionMeta(state, meta); err != nil {
				t.Fatal(err)
			}
		}
		mustWrite(t, filepath.Join(state, "sessions", "invalid.meta.json"), "{")
		_, _ = rootSessionID(state)
		_, _ = rootSessionID(t.TempDir())
		_, _ = rootSessionID("[")
		if err := os.Mkdir(filepath.Join(state, "sessions", "unreadable.meta.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, _ = rootSessionID(state)
		writeCoverageTranscript(t, state, "root", []schema.Turn{schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{coverageToolCall("read", `{}`)}})})
		writeCoverageTranscript(t, state, "child", []schema.Turn{schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{coverageToolCall("read", `{}`), coverageToolCall("shell", `{}`)}})})
		_, _ = allTranscriptToolCounts(state)
		runnerReadTranscript = func(string, string, doctor.TranscriptOpts) (doctor.TranscriptResult, error) {
			return doctor.TranscriptResult{}, errors.New("transcript")
		}
		_, _ = allTranscriptToolCounts(state)
		runnerReadTranscript = oldReadTranscript
		_, _ = allTranscriptToolCounts("[")
		mustWrite(t, filepath.Join(state, "sessions", ".transcript.jsonl"), "bad")
		mustWrite(t, filepath.Join(state, "sessions", "bad.transcript.jsonl"), "bad")
		_, _ = allTranscriptToolCounts(state)

		suiteDir := t.TempDir()
		suiteProbes := filepath.Join(suiteDir, "probes")
		mustWrite(t, filepath.Join(suiteProbes, "probe.yaml"), "schema: 1\nid: local\nprompt: hello\nexpect:\n  final_contains: [ok]\n")
		suiteOut := filepath.Join(suiteDir, "out")
		_ = runSuite([]string{"--model", "openai/gpt-5.4-mini", "--probes-dir", suiteProbes, "--out", suiteOut, "--serf-bin", bin, "--repetitions", "2"})
		_ = runSuite([]string{"--model", "openai/gpt-5.4-mini", "--probes-dir", suiteProbes, "--probe", "missing", "--out", filepath.Join(suiteDir, "none"), "--serf-bin", bin})
		_ = runSuite([]string{"--model", "bad", "--probes-dir", suiteProbes, "--out", filepath.Join(suiteDir, "bad-model"), "--serf-bin", bin})
		resultErrorOut := filepath.Join(suiteDir, "result-error")
		if err := os.MkdirAll(filepath.Join(resultErrorOut, "local", "rep-01", "result.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = runSuite([]string{"--model", "openai/gpt-5.4-mini", "--probes-dir", suiteProbes, "--out", resultErrorOut, "--serf-bin", bin})
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		chdir := t.TempDir()
		if err := os.Chdir(chdir); err != nil {
			t.Fatal(err)
		}
		_ = runSuite([]string{"--model", "openai/gpt-5.4-mini", "--probes-dir", suiteProbes, "--serf-bin", bin})
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
		buildOut := t.TempDir()
		repoRoot := filepath.Clean(filepath.Join(cwd, "..", "..", "..", ".."))
		if err := os.Chdir(repoRoot); err != nil {
			t.Fatal(err)
		}
		_, _ = buildSerf(buildOut)
		_ = runSuite([]string{"--model", "openai/gpt-5.4-mini", "--probes-dir", suiteProbes, "--out", filepath.Join(suiteDir, "built"), "--build"})
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
		path := os.Getenv("PATH")
		t.Setenv("PATH", "")
		_, _ = buildSerf(t.TempDir())
		t.Setenv("PATH", path)
		_ = runSuite([]string{"--model", "openai/gpt-5.4-mini", "--probes-dir", suiteProbes, "--out", t.TempDir(), "--build"})

		fakeClient := llm.NewClient()
		fakeClient.Register(coverageAdapter{})
		fakeCfg := providercfg.Config{Default: "openai", Instances: []providercfg.InstanceConfig{{Name: "openai", Type: "openai", APIStyle: providercfg.StyleResponses}}}
		runnerLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
			return fakeClient, fakeCfg, true, nil
		}
		liveProbeCfg := runConfig{model: "openai/m", harness: "live", outDir: filepath.Join(t.TempDir(), "live-out"), timeout: time.Second, reasoningEffort: "low"}
		_ = runProbe(liveProbeCfg, probeFile{ID: "live", Prompt: "hello"}, 1, nil)
		t.Setenv(envvars.SERFFluencyModel.Name, "")
		t.Setenv(envvars.SERFModel.Name, "")
		_ = defaultModel()
		t.Setenv(envvars.XDGConfigHome.Name, tmpFile)
		_ = runLiveProbe(context.Background(), runConfig{model: "openai/m", reasoningEffort: "low"}, probeFile{}, &probeResult{}, &bytes.Buffer{}, &bytes.Buffer{})
		t.Setenv(envvars.XDGConfigHome.Name, t.TempDir())
		liveRes := probeResult{StateDir: filepath.Join(t.TempDir(), "state"), WorkDir: t.TempDir()}
		var liveOut, liveErr bytes.Buffer
		runnerMarshalEvent = func(any) ([]byte, error) { return nil, errors.New("marshal event") }
		_ = runLiveProbe(context.Background(), runConfig{model: "OpenAI/m", reasoningEffort: "low"}, probeFile{Prompt: "hello"}, &liveRes, &liveOut, &liveErr)
		runnerMarshalEvent = oldMarshalEvent
		if err := runLiveProbe(context.Background(), runConfig{model: "OpenAI/m", reasoningEffort: "low", postTurnWait: time.Nanosecond}, probeFile{Prompt: "hello"}, &liveRes, &liveOut, &liveErr); err != nil {
			t.Logf("successful live probe setup: %v", err)
		}
		_ = runLiveProbe(context.Background(), runConfig{model: "bad", reasoningEffort: "low"}, probeFile{}, &liveRes, &liveOut, &liveErr)
		runnerLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
			return nil, providercfg.Config{}, false, errors.New("load client")
		}
		_ = runLiveProbe(context.Background(), runConfig{model: "openai/m", reasoningEffort: "low"}, probeFile{}, &liveRes, &liveOut, &liveErr)
		runnerLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
			return fakeClient, fakeCfg, true, nil
		}
		badLogRes := probeResult{StateDir: filepath.Join(tmpFile, "child"), WorkDir: t.TempDir()}
		_ = runLiveProbe(context.Background(), runConfig{model: "openai/m", reasoningEffort: "low"}, probeFile{}, &badLogRes, &liveOut, &liveErr)
		_ = runLiveProbe(context.Background(), runConfig{model: "missing/m", reasoningEffort: "low"}, probeFile{}, &liveRes, &liveOut, &liveErr)
		_ = runLiveProbe(context.Background(), runConfig{model: "openai/m", fastCheapModel: "missing/cheap", reasoningEffort: "low"}, probeFile{}, &liveRes, &liveOut, &liveErr)
		_ = runLiveProbe(context.Background(), runConfig{model: "openai/m", reasoningEffort: "invalid"}, probeFile{}, &liveRes, &liveOut, &liveErr)
		runnerAttachAPILogger = func(*llm.Client, string, io.Writer) (func() error, error) { return nil, errors.New("attach") }
		_ = runLiveProbe(context.Background(), runConfig{model: "openai/m", reasoningEffort: "low"}, probeFile{}, &liveRes, &liveOut, &liveErr)
		runnerAttachAPILogger = oldAttach
		canceled, cancelLive := context.WithCancel(context.Background())
		cancelLive()
		_ = runLiveProbe(canceled, runConfig{model: "openai/m", reasoningEffort: "low", postTurnWait: time.Second}, probeFile{Prompt: "hello"}, &liveRes, &liveOut, &liveErr)

		kickQueue := make(chan liveKick, 1)
		liveKickSubmitter(kickQueue)("continue")
		liveNotifySubmitter(kickQueue)()
		trySubmitLiveKick(kickQueue, liveKick{})
		appendLiveOutput(&bytes.Buffer{}, "")
		timerDone := make(chan time.Time, 1)
		timerDone <- time.Time{}
		_ = runLiveKickLoop(context.Background(), timerDone, make(chan liveKick), coverageKickProcessor{}, &bytes.Buffer{})
		loopCanceled, loopCancel := context.WithCancel(context.Background())
		loopCancel()
		_ = runLiveKickLoop(loopCanceled, make(chan time.Time), make(chan liveKick), coverageKickProcessor{}, &bytes.Buffer{})
		kickTimer := make(chan time.Time, 1)
		kicks := make(chan liveKick, 1)
		kicks <- liveKick{input: "x"}
		_ = runLiveKickLoop(context.Background(), kickTimer, kicks, coverageKickProcessor{out: "continued", timer: kickTimer}, &bytes.Buffer{})
		errorKicks := make(chan liveKick, 1)
		errorKicks <- liveKick{input: "x"}
		_ = runLiveKickLoop(context.Background(), make(chan time.Time), errorKicks, coverageKickProcessor{err: errors.New("kick")}, &bytes.Buffer{})
	})
}

func writeCoverageTranscript(t *testing.T, stateDir, sid string, turns []schema.Turn) {
	t.Helper()
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sid+".transcript.jsonl"), transcript.Header{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func coverageToolCall(name, args string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_" + name, Name: name, Arguments: json.RawMessage(args)}}
}
