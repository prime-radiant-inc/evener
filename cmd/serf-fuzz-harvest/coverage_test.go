package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
)

func scenarioHarvestRunFailuresAndMain(t *testing.T) {
	var out, errOut bytes.Buffer
	if run([]string{"--bad"}, &out, &errOut) != 2 || run([]string{"--surface", "bad"}, &out, &errOut) != 2 {
		t.Fatal("parse failures")
	}
	t.Setenv(envvars.SERFFuzzCaptureEnv.Name, "")
	if run([]string{"--keep-values", "--surface", "sse"}, &out, &errOut) != 2 {
		t.Fatal("keep gate")
	}
	if run([]string{"--surface", "sse", "--log", filepath.Join(t.TempDir(), "missing", "log")}, &out, &errOut) != 2 {
		t.Fatal("log open")
	}
	oldCore := harvestCoreToolNames
	t.Cleanup(func() { harvestCoreToolNames = oldCore })
	harvestCoreToolNames = func() ([]string, error) { return nil, errors.New("tools") }
	if run([]string{"--surface", "toolargs", "--state-dir", t.TempDir()}, &out, &errOut) != 1 {
		t.Fatal("core names")
	}
	oldExit, oldArgs := osExit, os.Args
	t.Cleanup(func() { osExit, os.Args = oldExit, oldArgs })
	os.Args = []string{"harvest", "--surface", "sse", "--state-dir", t.TempDir(), "--dry-run"}
	got := -1
	osExit = func(c int) { got = c }
	main()
	if got != 0 {
		t.Fatalf("exit=%d", got)
	}
}

func scenarioMixedSurfaceFixtures(t *testing.T) {
	d := t.TempDir()
	out := t.TempDir()
	r := newRunner(out, NewEmitter(false, 32768), nil)
	san := &Sanitizer{}
	method := appwire.Methods[0].Name
	frames := []string{"bad", `{"frame":""}`, marshalLine(t, recordedFrame{Frame: "bad"}), marshalLine(t, recordedFrame{Frame: `{"method":"x"}`}), marshalLine(t, recordedFrame{Frame: `{"method":"unknown","params":{}}`}), marshalLine(t, recordedFrame{Frame: `{"method":"` + method + `","params":{}}`})}
	app := filepath.Join(d, "app.jsonl")
	mustHarvestWrite(t, app, strings.Join(frames, "\n")+"\n")
	harvestAppwire(r, san, []string{app})
	http := filepath.Join(d, "http.jsonl")
	mustHarvestWrite(t, http, "bad\n"+marshalLine(t, recordedHTTPRequest{Method: "POST", Path: "/health"})+"\n"+marshalLine(t, recordedHTTPRequest{Method: "GET", Path: "/health"})+"\n")
	harvestHTTP(r, []string{http})
	api := filepath.Join(d, "api.jsonl")
	entries := []string{"bad", marshalLine(t, llm.APIRawLogEntry{Mode: "sync"}), marshalLine(t, llm.APIRawLogEntry{Mode: "stream", Provider: "anthropic", ResponseBody: "data: {\"type\":\"x\"}\n\n"}), marshalLine(t, llm.APIRawLogEntry{Mode: "stream", Provider: "google", ResponseBody: "data: {}\n\n"}), marshalLine(t, llm.APIRawLogEntry{Mode: "stream", Provider: "openai-compatible", ResponseBody: "data: {}\n\n"}), marshalLine(t, llm.APIRawLogEntry{Mode: "stream", Provider: "unknown", ResponseBody: "{}"})}
	mustHarvestWrite(t, api, strings.Join(entries, "\n")+"\n")
	harvestSSE(r, san, []string{api})
	entry := transcript.Entry{Kind: "entry", Turn: schema.Turn{Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "x"}, {Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{Name: "unknown"}}, {Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{Name: "known", Arguments: nil}}}}}}
	tr := filepath.Join(d, "tr.jsonl")
	mustHarvestWrite(t, tr, "bad\n"+marshalLine(t, map[string]string{"kind": "header"})+"\n"+marshalLine(t, entry)+"\n")
	harvestToolArgs(r, san, []string{tr}, []string{"known"})
	jobs := filepath.Join(d, "jobs.jsonl")
	mustHarvestWrite(t, jobs, "bad\n{\"kind\":\"x\"}\n")
	harvestJobs(r, san, []string{jobs})
	if r.stat("sse").scanned == 0 || r.stat("http").scanned == 0 || r.stat("appwire").scanned == 0 {
		t.Fatalf("stats=%v", r.stats)
	}
}

func scenarioSanitizerRemainingPrimitiveBranches(t *testing.T) {
	for _, n := range []int{0, 1, 9, 65} {
		if placeholder(n) == "" && n != 0 {
			t.Errorf("placeholder %d", n)
		}
	}
	if scrubNumber(json.Number("1e2")) != "0.0" || scrubNumber(json.Number("1")) != "0" {
		t.Fatal("numbers")
	}
	if _, ok := scrubJSONString("bad"); ok {
		t.Fatal("bad json string")
	}
	for _, raw := range [][]byte{[]byte("data: bad\r\n"), []byte("data:\n"), []byte("data: [DONE]\n"), []byte("event: x\n"), []byte("tail")} {
		if _, err := scrubSSE(raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, seg := range []string{"x\r\n", "x\n", "x"} {
		line, term := splitTerminator(seg)
		if line != "x" || len(term) > 2 {
			t.Fatalf("split %q=%q,%q", seg, line, term)
		}
	}
	if shannonEntropy("") != 0 {
		t.Fatal("empty entropy")
	}
}

func scenarioGitleaksScanOutcomes(t *testing.T) {
	oldLook, oldCmd := gitleaksLookPath, gitleaksCommandContext
	t.Cleanup(func() { gitleaksLookPath, gitleaksCommandContext = oldLook, oldCmd })
	var stderr bytes.Buffer
	gitleaksLookPath = func(string) (string, error) { return "", errors.New("missing") }
	if clean, avail := gitleaksScan(".", &stderr); clean || avail {
		t.Fatal("missing")
	}
	gitleaksLookPath = func(string) (string, error) { return "/bin/true", nil }
	if clean, avail := gitleaksScan(".", &stderr); !clean || !avail {
		t.Fatal("clean")
	}
	gitleaksLookPath = func(string) (string, error) { return "/bin/false", nil }
	if clean, avail := gitleaksScan(".", &stderr); clean || !avail {
		t.Fatal("leak")
	}
	gitleaksLookPath = func(string) (string, error) { return "/missing", nil }
	if clean, avail := gitleaksScan(".", &stderr); clean || avail {
		t.Fatal("run error")
	}
}

func scenarioHarvestLeakExitAndSmallHelpers(t *testing.T) {
	oldDiscover := harvestDiscoverSources
	t.Cleanup(func() { harvestDiscoverSources = oldDiscover })
	d := t.TempDir()
	api := filepath.Join(d, "api.jsonl")
	mustHarvestWrite(t, api, marshalLine(t, llm.APIRawLogEntry{Mode: "stream", Provider: "unknown", ResponseBody: `{"x":"Zk9q3SxV1pLmTtRwYbNcHgJdFeUoIa72Qw0PzXyB"}`})+"\n")
	harvestDiscoverSources = func(string) (recordedSources, error) { return recordedSources{sse: []string{api}}, nil }
	t.Setenv(envvars.SERFFuzzCaptureEnv.Name, "yes")
	var out, errOut bytes.Buffer
	if got := run([]string{"--keep-values", "--surface", "sse", "--state-dir", d, "--dry-run"}, &out, &errOut); got != 1 {
		t.Fatalf("leak exit=%d %q", got, errOut.String())
	}
	if _, err := parseSurfaces(", ,"); err == nil {
		t.Fatal("empty surfaces")
	}
	if got, err := parseSurfaces("sse,,"); err != nil || !got["sse"] {
		t.Fatal("empty part")
	}
	r := newRunner(t.TempDir(), NewEmitter(true, 10), nil)
	r.logf("discard")
}

func scenarioEmitterWriteFileFailure(t *testing.T) {
	e := NewEmitter(false, 100)
	d := t.TempDir()
	encoded := encodeBytesSeed([]byte("x"))
	sum := sha256.Sum256(encoded)
	p := filepath.Join(d, hex.EncodeToString(sum[:]))
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.EmitBytes(d, []byte("x")); err == nil {
		t.Fatal("write file")
	}
}

func scenarioHarvesterInjectedSanitizeAndEmitFailures(t *testing.T) {
	oldProcess := sanitizerProcess
	t.Cleanup(func() { sanitizerProcess = oldProcess })
	calls := 0
	sanitizerProcess = func(_ *Sanitizer, raw []byte, _ bool) ([]byte, error) {
		calls++
		if calls%2 == 0 {
			return nil, errors.New("sanitize")
		}
		return raw, nil
	}
	d := t.TempDir()
	method := appwire.Methods[0].Name
	app := filepath.Join(d, "app")
	mustHarvestWrite(t, app, marshalLine(t, recordedFrame{Frame: `{"method":"` + method + `","params":{}}`})+"\n")
	r := newRunner(t.TempDir(), NewEmitter(false, 100), nil)
	harvestAppwire(r, &Sanitizer{}, []string{app})
	if r.stat("appwire").skipped == 0 {
		t.Fatal("appwire sanitize")
	}
	sanitizerProcess = func(_ *Sanitizer, raw []byte, _ bool) ([]byte, error) { return raw, nil }
	badRoot := filepath.Join(t.TempDir(), "file")
	mustHarvestWrite(t, badRoot, "x")
	r = newRunner(badRoot, NewEmitter(false, 100), &bytes.Buffer{})
	harvestAppwire(r, &Sanitizer{}, []string{app})
	http := filepath.Join(d, "http")
	mustHarvestWrite(t, http, marshalLine(t, recordedHTTPRequest{Method: "GET", Path: "/health"})+"\n")
	harvestHTTP(r, []string{http})
	entry := transcript.Entry{Kind: "entry", Turn: schema.Turn{Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{Name: "known", Arguments: []byte(`{}`)}}}}}}
	tr := filepath.Join(d, "tr")
	mustHarvestWrite(t, tr, marshalLine(t, entry)+"\n")
	harvestToolArgs(r, &Sanitizer{}, []string{tr}, []string{"known"})
}

func scenarioReverseHTTPNoMatchAndBadQuery(t *testing.T) {
	if _, _, ok := reverseMapHTTP("GET", "not-absolute", ""); ok {
		t.Fatal("match")
	}
	if _, _, ok := reverseMapHTTP("GET", "/doc/file", "%zz"); ok {
		t.Fatal("query")
	}
}

func scenarioForEachJSONLineOpenAndEmpty(t *testing.T) {
	if err := forEachJSONLine(filepath.Join(t.TempDir(), "missing"), func([]byte) {}); err == nil {
		t.Fatal("open")
	}
	p := filepath.Join(t.TempDir(), "x")
	mustHarvestWrite(t, p, "\n{}\n")
	n := 0
	if err := forEachJSONLine(p, func([]byte) { n++ }); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func scenarioRemainingFilesystemAndGateBranches(t *testing.T) {
	oldWalk, oldAbs := harvestWalkDir, harvestAbs
	t.Cleanup(func() { harvestWalkDir, harvestAbs = oldWalk, oldAbs })
	boom := errors.New("boom")
	harvestWalkDir = func(_ string, fn fs.WalkDirFunc) error { _ = fn("x", nil, boom); return boom }
	if _, err := discoverSources("x"); !errors.Is(err, boom) {
		t.Fatalf("discover=%v", err)
	}
	harvestAbs = func(string) (string, error) { return "", boom }
	if isPersonalStateDir("x") {
		t.Fatal("personal abs")
	}
	calls := 0
	harvestAbs = func(s string) (string, error) {
		calls++
		if calls == 2 {
			return "", boom
		}
		return s, nil
	}
	if isPersonalStateDir("x") {
		t.Fatal("default abs")
	}
	oldRoot, oldBase, oldDir := harvestDefaultStateRoot, harvestResolveStateBase, harvestIsDir
	t.Cleanup(func() { harvestDefaultStateRoot, harvestResolveStateBase, harvestIsDir = oldRoot, oldBase, oldDir })
	harvestAbs = oldAbs
	harvestDefaultStateRoot = func() string { return "/same" }
	harvestResolveStateBase = func(string) string { return "/same" }
	harvestIsDir = func(string) bool { return false }
	if got := defaultStateDirs(); len(got) != 1 {
		t.Fatalf("dedupe=%v", got)
	}
	harvestIsDir = func(string) bool { return true }
	if got := defaultStateDirs(); len(got) != 2 {
		t.Fatalf("subdir=%v", got)
	}
	harvestAbs = func(string) (string, error) { return "x", boom }
	if got := defaultStateDirs(); len(got) == 0 {
		t.Fatal("abs fallback")
	}
	harvestAbs = oldAbs
	oldDetect := gateDetectSecret
	t.Cleanup(func() { gateDetectSecret = oldDetect })
	gateDetectSecret = func([]byte, bool) string { return "forced" }
	d := t.TempDir()
	p := filepath.Join(d, "http")
	mustHarvestWrite(t, p, marshalLine(t, recordedHTTPRequest{Method: "GET", Path: "/health"})+"\n")
	r := newRunner(t.TempDir(), NewEmitter(true, 100), nil)
	harvestHTTP(r, []string{p})
	if r.stat("http").leaks != 1 {
		t.Fatal("http gate")
	}
}

func scenarioRunLogAndPersonalKeepNote(t *testing.T) {
	oldDiscover, oldAbs := harvestDiscoverSources, harvestAbs
	t.Cleanup(func() { harvestDiscoverSources, harvestAbs = oldDiscover, oldAbs })
	harvestDiscoverSources = func(string) (recordedSources, error) { return recordedSources{}, nil }
	harvestAbs = func(string) (string, error) { return "/same", nil }
	t.Setenv(envvars.SERFStateDir.Name, "")
	t.Setenv(envvars.SERFFuzzCaptureEnv.Name, "yes")
	var out, errOut bytes.Buffer
	log := filepath.Join(t.TempDir(), "log")
	if got := run([]string{"--keep-values", "--surface", "sse", "--state-dir", "personal", "--dry-run", "--log", log}, &out, &errOut); got != 0 || !strings.Contains(errOut.String(), "ignored for personal") {
		t.Fatalf("got=%d err=%q", got, errOut.String())
	}
	if _, err := os.Stat(log); err != nil {
		t.Fatal(err)
	}
}

func scenarioToolArgsSecondDecodeAndSanitizeFailure(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "tr")
	mustHarvestWrite(t, p, "{\"kind\":\"entry\",\"turn\":[]}\n"+marshalLine(t, transcript.Entry{Kind: "entry", Turn: schema.Turn{Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{Name: "known", Arguments: []byte(`{}`)}}}}}})+"\n")
	old := sanitizerProcess
	t.Cleanup(func() { sanitizerProcess = old })
	sanitizerProcess = func(*Sanitizer, []byte, bool) ([]byte, error) { return nil, errors.New("bad") }
	r := newRunner(t.TempDir(), NewEmitter(true, 100), nil)
	harvestToolArgs(r, &Sanitizer{}, []string{p}, []string{"known"})
	if r.stat("toolargs").skipped != 1 {
		t.Fatalf("stats=%+v", r.stat("toolargs"))
	}
}

func marshalLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func mustHarvestWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func scenarioHarvestRunInjectedOutcomes(t *testing.T) {
	oldDiscover, oldCore, oldScan := harvestDiscoverSources, harvestCoreToolNames, harvestGitleaksScan
	t.Cleanup(func() {
		harvestDiscoverSources, harvestCoreToolNames, harvestGitleaksScan = oldDiscover, oldCore, oldScan
	})
	var out, errOut bytes.Buffer
	harvestDiscoverSources = func(string) (recordedSources, error) { return recordedSources{}, errors.New("discover") }
	if got := run([]string{"--surface", "sse", "--state-dir", "x", "--dry-run"}, &out, &errOut); got != 0 || !strings.Contains(errOut.String(), "discover") {
		t.Fatalf("discover=%d %q", got, errOut.String())
	}
	harvestDiscoverSources = func(string) (recordedSources, error) { return recordedSources{}, nil }
	harvestCoreToolNames = func() ([]string, error) { return []string{"communicate"}, nil }
	for _, scan := range []struct {
		clean, available bool
		want             int
	}{{false, false, 0}, {false, true, 1}, {true, true, 0}} {
		harvestGitleaksScan = func(string, io.Writer) (bool, bool) { return scan.clean, scan.available }
		out.Reset()
		errOut.Reset()
		got := run([]string{"--surface", strings.Join(allSurfaces, ","), "--state-dir", t.TempDir(), "--out-root", t.TempDir()}, &out, &errOut)
		if got != scan.want {
			t.Errorf("scan=%+v got=%d", scan, got)
		}
	}
}

func scenarioRunnerFailureAccountingAndHelpers(t *testing.T) {
	var log bytes.Buffer
	r := newRunner(t.TempDir(), NewEmitter(false, 3), &log)
	st := r.stat("x")
	r.logf("hello %s", "x")
	if _, ok := r.scrub(st, &Sanitizer{}, []byte("not json"), false); ok || st.skipped != 1 {
		t.Fatal("scrub skip")
	}
	secret := []byte(`{"x":"Zk9q3SxV1pLmTtRwYbNcHgJdFeUoIa72Qw0PzXyB"}`)
	if _, ok := r.scrub(st, &Sanitizer{keepValues: true}, secret, false); ok || st.leaks != 1 {
		t.Fatalf("scrub leak=%+v", st)
	}
	oldDetect := gateDetectSecret
	t.Cleanup(func() { gateDetectSecret = oldDetect })
	gateDetectSecret = func([]byte, bool) string { return "forced" }
	if _, ok := r.gateString(st, "suffix"); ok {
		t.Fatal("gate leak")
	}
	r.recordEmit(st, statusWritten)
	r.recordEmit(st, statusDryRun)
	r.recordEmit(st, statusOversized)
	r.emitBytesTo(st, []byte("long"), filepath.Join(t.TempDir(), "d"))
	badParent := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(badParent, []byte("x"), 0o600)
	r.emitBytesTo(st, []byte("x"), filepath.Join(badParent, "d"))
	if !strings.Contains(r.summary(), "x: scanned") || !strings.Contains(log.String(), "emit error") {
		t.Fatalf("summary/log %q %q", r.summary(), log.String())
	}
	if got := providerTargetDirs(r, "openai", []byte(`{"choices":[]}`)); len(got) != 1 {
		t.Fatalf("chat dirs=%v", got)
	}
	if !isChatCompletionsStream([]byte("chat.completion")) {
		t.Fatal("chat detection")
	}
}
