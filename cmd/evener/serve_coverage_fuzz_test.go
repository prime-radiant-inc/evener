//go:build evenerfuzz

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/llm"
)

func testServeLLMClientErrors(t *testing.T) {
	oldLoad := serveLoadClient
	t.Cleanup(func() { serveLoadClient = oldLoad })

	loadErr := errors.New("load")
	serveLoadClient = func(string) (*llm.Client, error) { return nil, loadErr }
	if _, _, err := newUnloggedServeLLMClient(t.TempDir(), io.Discard); !errors.Is(err, loadErr) {
		t.Fatalf("newUnloggedServeLLMClient error = %v, want %v", err, loadErr)
	}

	serveLoadClient = func(string) (*llm.Client, error) { return llm.NewClient(), nil }
	client, closeClient, err := newUnloggedServeLLMClient(t.TempDir(), io.Discard)
	if err != nil {
		t.Fatalf("newUnloggedServeLLMClient: %v", err)
	}
	if err := closeClient(); err != nil {
		t.Fatalf("closeClient: %v", err)
	}

	badStateDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badStateDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write bad state file: %v", err)
	}
	if _, _, err := serveAttachSessionAPILogger(client, badStateDir, io.Discard, false); err == nil {
		t.Fatal("expected attach error for file state dir")
	}

	attachErr := errors.New("attach")
	d := exactServeDeps(t)
	closed := false
	d.newClient = func(string, io.Writer) (*llm.Client, func() error, error) {
		return llm.NewClient(), func() error { closed = true; return nil }, nil
	}
	d.attachAPILogger = func(*llm.Client, string, io.Writer, bool) (func(string) error, func() error, error) {
		return nil, nil, attachErr
	}
	if err := runServeWithDeps(exactServeArgs(t), d); !errors.Is(err, attachErr) {
		t.Fatalf("runServeWithDeps error = %v, want %v", err, attachErr)
	}
	if !closed {
		t.Fatal("expected client closer to run when attachAPILogger fails")
	}
}

// FuzzServeSeedCoverage replays deterministic daemon workflows as part of the
// committed fuzz seed bank. Their provider boundary is scripted in-process.
func FuzzServeSeedCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		tests := []struct {
			name string
			fn   func(*testing.T)
		}{
			{"client errors", testServeLLMClientErrors},
			{"profile config", TestBuildInitialProfile_ConfigPath},
			{"profile schema", TestBuildInitialProfile_ConfigPathInvalidOutputSchema},
			{"profile unknown", TestBuildInitialProfile_UnknownInstanceError},
			{"profile curated instance", TestBuildInitialProfile_CuratedInstance},
			{"bare model", TestRunServe_BareModelRejected},
			{"missing model", TestRunServe_MissingModel},
			{"env help", TestPrintServeEnvVars_IncludesOpenAIResponsesContinuation},
			{"api log", TestServeClient_APILogWritesJSONL},
			{"resume missing", TestRunServe_ResumeNonexistent},
			{"status empty", TestAgentToServerDetailedStatus_Empty},
			{"status partial", TestAgentToServerDetailedStatus_Partial},
			{"status stable delegates", TestAgentToServerDetailedStatus_DelegatesLossless},
			{"usage zero", TestEvenerUsageFromLLM_ZeroReturnsNil},
			{"usage totals", TestEvenerUsageFromLLM_MapsTotals},
			{"usage cache", TestEvenerUsageFromLLM_NonZeroCacheReadOnlyStillReturns},
			{"cheap same", TestApplyFastCheapModel_SameProviderOverridesCheapModel},
			{"cheap cross", TestApplyFastCheapModel_CrossProviderWhenRegistered},
			{"cheap rejected", TestApplyFastCheapModel_CrossProviderRejectedWhenNotRegistered},
			{"cheap bare", TestApplyFastCheapModel_BareModelKeepsActiveProvider},
			{"cheap blank", TestApplyFastCheapModel_BlankUsesPrimaryModel},
			{"noninteractive", TestRunServeNonInteractiveFlagReachesTheSession},
			{"shutdown waits", TestRunServeShutdownWaitsForInFlightInput},
			{"goal", TestServeGoal_DaemonPathEndToEnd},
		}
		for _, tc := range tests {
			t.Run(tc.name, tc.fn)
		}
	})
}
