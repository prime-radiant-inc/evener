package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
)

func clientWith(resp llm.Response, err error) *llm.Client {
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "fake", resp: resp, err: err})
	return c
}

func TestLLMCallMainParserBranches(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := llmcallMain([]string{"-h"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if err := llmcallMain([]string{"--bad"}, &out, &errOut); err == nil {
		t.Fatal("bad flag")
	}
	if err := llmcallMain([]string{"--provider", "fake", "--model", "m", "--timeout", "bad", "p"}, &out, &errOut); err == nil {
		t.Fatal("timeout")
	}
	t.Setenv(envvars.LLMProvider.Name, "")
	t.Setenv(envvars.SERFProvider.Name, "")
	t.Setenv(envvars.LLMModel.Name, "")
	t.Setenv(envvars.SERFModel.Name, "")
	if err := llmcallMain([]string{"p"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("provider=%v", err)
	}
	t.Setenv(envvars.LLMProvider.Name, "fake")
	if err := llmcallMain([]string{"p"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("model=%v", err)
	}
	// A pipe exercises the no-argument stdin path deterministically.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString("from stdin\n")
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; _ = r.Close() })
	if err := llmcallMain(nil, &out, &errOut); err == nil {
		t.Fatal("stdin prompt reached client setup without a client")
	}
	// Empty pipe traverses usage/no-prompt.
	r2, w2, _ := os.Pipe()
	_ = w2.Close()
	os.Stdin = r2
	if err := llmcallMain(nil, &out, &errOut); err == nil || !strings.Contains(err.Error(), "no prompt") {
		t.Fatalf("prompt=%v", err)
	}
}

func TestLLMCallProfilesAndOptions(t *testing.T) {
	var out, errOut bytes.Buffer
	cpu := filepath.Join(t.TempDir(), "cpu.pprof")
	trace := filepath.Join(t.TempDir(), "trace.out")
	// Provider lookup will fail after both profilers have started and deferred stops run.
	err := llmcallMain([]string{"--provider", "missing", "--model", "m", "--cpu-profile", cpu, "--trace", trace, "--timeout", "1s", "p"}, &out, &errOut)
	if err == nil {
		t.Fatal("provider failure")
	}
	for _, p := range []string{cpu, trace} {
		if info, e := os.Stat(p); e != nil || info.Size() == 0 {
			t.Errorf("profile %s info=%v err=%v", p, info, e)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing", "profile")
	if err := llmcallMain([]string{"--cpu-profile", missing, "p"}, &out, &errOut); err == nil {
		t.Fatal("cpu profile start")
	}
	if err := llmcallMain([]string{"--trace", missing, "p"}, &out, &errOut); err == nil {
		t.Fatal("trace start")
	}
}

func TestRunLLMCallRemainingErrorsAndOptions(t *testing.T) {
	base := llmCallConfig{prompt: "p", provider: "fake", model: "m", format: "text", client: clientWith(llm.Response{}, nil)}
	if err := runLLMCall(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	bad := base
	bad.client = nil
	oldNew := newLLMClientFromEnv
	t.Cleanup(func() { newLLMClientFromEnv = oldNew })
	newLLMClientFromEnv = func(...llm.EnvOption) (*llm.Client, error) { return nil, errors.New("setup") }
	if err := runLLMCall(context.Background(), bad); err == nil {
		t.Fatal("client setup")
	}
	bad = base
	bad.systemText = "x"
	bad.systemFile = "y"
	if err := runLLMCall(context.Background(), bad); err == nil {
		t.Fatal("system conflict")
	}
	bad = base
	bad.metadata = []string{"bad"}
	if err := runLLMCall(context.Background(), bad); err == nil {
		t.Fatal("metadata")
	}
	bad = base
	bad.schema = "missing"
	if err := runLLMCall(context.Background(), bad); err == nil {
		t.Fatal("schema read")
	}
	bad = base
	bad.schema = writeSchema(t)
	bad.client = clientWith(llm.Response{}, errors.New("complete"))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runLLMCall(canceled, bad); err == nil {
		t.Fatal("object generation")
	}
	toolResp := llm.Response{Model: "m", Provider: "fake", Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: `{"ok":true}`}, {Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "1", Name: "x"}}}}}
	bad.client = clientWith(toolResp, nil)
	if err := runLLMCall(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "tool calls") {
		t.Fatalf("object tools=%v", err)
	}
	bad = base
	bad.client = clientWith(llm.Response{}, errors.New("complete"))
	if err := runLLMCall(canceled, bad); err == nil {
		t.Fatal("generate")
	}
	bad = base
	bad.format = "json"
	bad.client = clientWith(llm.Response{Model: "m", Provider: "fake", Message: llm.Assistant("bad")}, nil)
	if err := runLLMCall(context.Background(), bad); err == nil {
		t.Fatal("json parse")
	}
	good := base
	good.verbose = true
	good.noSystem = true
	good.temperature = 1
	good.topP = .5
	good.maxTokens = 4
	good.reasoningEffort = " low "
	good.metadata = []string{"k=v"}
	good.stopSequences = []string{"s"}
	good.webSearch = true
	good.client = clientWith(llm.Response{}, nil)
	var out, errOut bytes.Buffer
	good.stdout = &out
	good.stderr = &errOut
	if err := runLLMCall(context.Background(), good); err != nil || !strings.Contains(errOut.String(), "[model]") {
		t.Fatalf("options=%v %q", err, errOut.String())
	}
}

func writeSchema(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(p, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLLMCallMainExit(t *testing.T) {
	oldEntry, oldExit, oldArgs := llmcallEntry, osExit, os.Args
	t.Cleanup(func() { llmcallEntry, osExit, os.Args = oldEntry, oldExit, oldArgs })
	llmcallEntry = func([]string, io.Writer, io.Writer) error { return errors.New("x") }
	got := -1
	osExit = func(c int) { got = c }
	os.Args = []string{"llmcall"}
	main()
	if got != 1 {
		t.Fatalf("exit=%d", got)
	}
}
