package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestRunRoutesLocalSubcommands(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}, {"bogus"}} {
		err := run(args)
		if len(args) == 0 && err == nil {
			t.Fatal("empty args returned nil")
		}
		if len(args) > 0 && args[0] != "bogus" && err != nil {
			t.Fatalf("run(%v): %v", args, err)
		}
		if len(args) > 0 && args[0] == "bogus" && err == nil {
			t.Fatal("unknown subcommand returned nil")
		}
	}
	if err := run([]string{"catalog", "--model", "bad"}); err == nil {
		t.Fatal("bad catalog model returned nil")
	}
	if err := run([]string{"run", "--repetitions", "0"}); err == nil {
		t.Fatal("bad run repetitions returned nil")
	}
}

func TestSplitModelRefAndNames(t *testing.T) {
	for _, bad := range []string{"", "provider", "/model", "provider/"} {
		if _, _, err := splitModelRef(bad); err == nil {
			t.Fatalf("splitModelRef(%q) returned nil", bad)
		}
	}
	p, m, err := splitModelRef(" openai/model/name ")
	if err != nil || p != "openai" || m != "model/name" {
		t.Fatalf("split = %q/%q, %v", p, m, err)
	}
	if got := safeName(" a/b c! "); got != "a-b-c-" {
		t.Fatalf("safeName = %q", got)
	}
	if got := formatCounts(nil); got != "-" {
		t.Fatalf("empty counts = %q", got)
	}
	if got := formatCounts(map[string]int{"z": 2, "a": 1}); got != "a:1,z:2" {
		t.Fatalf("counts = %q", got)
	}
}

func TestLoadProbesAndFixtures(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "ignored.txt"), "x")
	mustWrite(t, filepath.Join(dir, "b.yml"), "schema: 1\nid: beta\nprompt: b\n")
	mustWrite(t, filepath.Join(dir, "a.yaml"), "schema: 1\nid: alpha\nprompt: a\n")
	probes, err := loadProbes(dir, "all")
	if err != nil || len(probes) != 2 || probes[0].ID != "alpha" {
		t.Fatalf("loadProbes = %#v, %v", probes, err)
	}
	probes, err = loadProbes(dir, "beta")
	if err != nil || len(probes) != 1 || probes[0].ID != "beta" {
		t.Fatalf("filtered probes = %#v, %v", probes, err)
	}

	bad := t.TempDir()
	mustWrite(t, filepath.Join(bad, "bad.yaml"), "schema: [\n")
	if _, err := loadProbes(bad, "all"); err == nil {
		t.Fatal("invalid YAML returned nil")
	}
	mustWrite(t, filepath.Join(bad, "bad.yaml"), "schema: 1\n")
	if _, err := loadProbes(bad, "all"); err == nil {
		t.Fatal("missing id returned nil")
	}
	if _, err := loadProbes(filepath.Join(dir, "missing"), "all"); err == nil {
		t.Fatal("missing dir returned nil")
	}

	work := filepath.Join(t.TempDir(), "work")
	if err := materializeFixture(work, fixtureSpec{Files: map[string]string{"nested/a.txt": "hello"}}); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(work, "nested", "a.txt")); string(body) != "hello" {
		t.Fatalf("fixture body = %q", body)
	}
	if err := materializeFixture(work, fixtureSpec{Files: map[string]string{"../escape": "x"}}); err == nil {
		t.Fatal("escaping fixture returned nil")
	}
}

func TestParseEventsAndExpectations(t *testing.T) {
	data := strings.Join([]string{
		"noise", "", "{bad",
		`{"kind":"TOOL_CALL_START","data":{"tool_name":"shell"}}`,
		`{"kind":"TOOL_CALL_END","data":{"tool_name":"shell","error":"boom"}}`,
		`{"kind":"COMMUNICATE","data":{"message":"done now"}}`,
	}, "\n")
	counts, errs, messages := parseEvents([]byte(data))
	if counts["shell"] != 1 || errs["shell"] != 1 || len(messages) != 1 {
		t.Fatalf("parsed = %v %v %v", counts, errs, messages)
	}

	work := t.TempDir()
	mustWrite(t, filepath.Join(work, "artifact.txt"), "hello world")
	yes, no := true, false
	probe := probeFile{Expect: expectSpec{
		Calls:          []expectedCall{{Tool: "read", Min: 2}, {Tool: "write"}},
		ForbiddenCalls: []string{"shell"},
		Artifacts: []artifactExpect{
			{Path: "artifact.txt", Exists: &yes, Contains: "missing"},
			{Path: "missing.txt", Exists: &no},
		},
		FinalContains: []string{"absent", "done"},
	}}
	res := probeResult{
		FinalOutput:         "final",
		CommunicateMessages: []string{"done via message"},
		CanonicalToolCounts: map[string]int{"read": 1, "shell": 1},
		ModelToolCounts:     map[string]int{"read": 3},
		ToolErrors:          map[string]int{"read": 1, "zero": 0},
	}
	findings := evaluateExpectations(work, probe, res)
	if len(findings) != 5 {
		t.Fatalf("findings = %#v", findings)
	}
	if !resultContains(res, "done") || resultContains(res, "never") {
		t.Fatal("resultContains mismatch")
	}
}

func TestAvailabilityProfilesAndInfraClassification(t *testing.T) {
	probe := probeFile{Skip: map[string]string{"if_unavailable": "shell"}}
	if got := unavailableFinding(probe, map[string]bool{"shell": true}); got != nil {
		t.Fatalf("available finding = %#v", got)
	}
	if got := unavailableFinding(probe, nil); got == nil || got.Detail != "shell" {
		t.Fatalf("unavailable finding = %#v", got)
	}
	if got := unavailableFinding(probeFile{}, nil); got != nil {
		t.Fatalf("empty skip finding = %#v", got)
	}
	for _, text := range []string{"insufficient_quota", "RATE LIMIT", "429", "no provider", "unknown provider", "API KEY"} {
		if !looksInfraError(text) {
			t.Fatalf("looksInfraError(%q) = false", text)
		}
	}
	if looksInfraError("ordinary failure") {
		t.Fatal("ordinary failure classified as infra")
	}

	p := provider.NewOpenAIProfile("model")
	if got, err := runnerApplyFastCheapModel(nil, "x", nil); err != nil || got != nil {
		t.Fatalf("nil profile = %v, %v", got, err)
	}
	if got, err := runnerApplyFastCheapModel(p, " ", nil); err != nil || got != p {
		t.Fatalf("blank model = %v, %v", got, err)
	}
	if _, err := runnerApplyFastCheapModel(p, "other/cheap", llm.NewClient()); err == nil {
		t.Fatal("missing cheap provider returned nil")
	}
	if runnerClientHasProvider(nil, "x") {
		t.Fatal("nil client has provider")
	}
}

func TestRootSessionAndResultPersistence(t *testing.T) {
	state := t.TempDir()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	for _, meta := range []schema.SessionMeta{
		{ID: "child", IsSubagent: true, CreatedAt: old.Add(-time.Hour)},
		{ID: "new", CreatedAt: newer},
		{ID: "old", CreatedAt: old},
	} {
		if err := schema.SaveSessionMeta(state, meta); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(state, "sessions", "invalid.meta.json"), "{")
	if id, err := rootSessionID(state); err != nil || id != "old" {
		t.Fatalf("rootSessionID = %q, %v", id, err)
	}
	if _, err := rootSessionID(t.TempDir()); err == nil {
		t.Fatal("empty root session returned nil")
	}

	out := t.TempDir()
	res := probeResult{Schema: 1, Probe: "a/b", Model: "m", Repetition: 2, Status: "passed"}
	if err := writeProbeResult(out, res); err != nil {
		t.Fatal(err)
	}
	if err := writeSummary(out, []probeResult{res}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(out, "a-b", "rep-02", "result.json"),
		filepath.Join(out, "results.jsonl"), filepath.Join(out, "summary.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
}

func TestRunCLIProbe(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fake-serf")
	mustWrite(t, bin, "#!/bin/sh\nprintf 'ok\\n'\nprintf '%s\\n' '{\"kind\":\""+string(events.EventCommunicate)+"\",\"data\":{\"message\":\"hi\"}}' >&2\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := runCLIProbe(context.Background(), runConfig{serfBin: bin, model: "openai/m", reasoningEffort: "low"}, probeFile{Prompt: "p"}, probeResult{WorkDir: t.TempDir(), StateDir: t.TempDir()}, &stdout, &stderr)
	if err != nil || !strings.Contains(stdout.String(), "ok") || !strings.Contains(stderr.String(), "COMMUNICATE") {
		t.Fatalf("runCLIProbe = %q %q %v", stdout.String(), stderr.String(), err)
	}
}

func TestCatalogAndSuiteOffline(t *testing.T) {
	tools, err := catalogTools("openai/gpt-5.4-mini")
	if err != nil || len(tools) == 0 {
		t.Fatalf("catalogTools = %d, %v", len(tools), err)
	}
	if err := runCatalog([]string{"--model", "openai/gpt-5.4-mini"}); err != nil {
		t.Fatal(err)
	}
	if err := runCatalog([]string{"--model", "openai/gpt-5.4-mini", "--json"}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	probes := filepath.Join(dir, "probes")
	mustWrite(t, filepath.Join(probes, "probe.yaml"), "schema: 1\nid: local\nprompt: hello\nexpect:\n  final_contains: [ok]\n")
	bin := filepath.Join(dir, "fake-serf")
	mustWrite(t, bin, "#!/bin/sh\nprintf 'ok\\n'\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	err = runSuite([]string{"--model", "openai/gpt-5.4-mini", "--probes-dir", probes, "--out", out, "--serf-bin", bin, "--repetitions", "2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runSuite([]string{"--model", "openai/gpt-5.4-mini", "--probes-dir", probes, "--probe", "missing", "--out", filepath.Join(dir, "none"), "--serf-bin", bin}); err == nil {
		t.Fatal("empty selection returned nil")
	}
}

func TestRunProbeOfflineStates(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-serf")
	mustWrite(t, bin, "#!/bin/sh\nprintf 'ok final\\n'\nprintf '%s\\n' '{\"kind\":\"TOOL_CALL_START\",\"data\":{\"tool_name\":\"read\"}}' >&2\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := runConfig{model: "openai/m", harness: "cli", outDir: filepath.Join(dir, "out"), serfBin: bin, timeout: time.Second, reasoningEffort: "low"}
	probe := probeFile{ID: "pass", Prompt: "hello", Fixture: fixtureSpec{Files: map[string]string{"a.txt": "a"}}, Expect: expectSpec{Calls: []expectedCall{{Tool: "read"}}, FinalContains: []string{"ok"}}}
	if res := runProbe(cfg, probe, 1, map[string]bool{}); res.Status != "passed" {
		t.Fatalf("pass result = %#v", res)
	}

	skip := probeFile{ID: "skip", Skip: map[string]string{"if_unavailable": "missing"}}
	if res := runProbe(cfg, skip, 1, nil); res.Status != "skipped_unavailable" {
		t.Fatalf("skip result = %#v", res)
	}

	badFixture := probeFile{ID: "fixture", Fixture: fixtureSpec{Files: map[string]string{"../bad": "x"}}}
	if res := runProbe(cfg, badFixture, 1, nil); res.Error == "" || len(res.Findings) != 1 {
		t.Fatalf("fixture result = %#v", res)
	}

	failBin := filepath.Join(dir, "fail-serf")
	mustWrite(t, failBin, "#!/bin/sh\nprintf 'rate limit' >&2\nexit 1\n")
	if err := os.Chmod(failBin, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.serfBin = failBin
	if res := runProbe(cfg, probeFile{ID: "infra"}, 1, nil); res.Status != "blocked_infra" {
		t.Fatalf("infra result = %#v", res)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
