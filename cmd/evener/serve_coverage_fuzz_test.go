//go:build evenerfuzz

package main

import (
	"errors"
	"io"
	"testing"

	"primeradiant.com/evener/llm"
)

func testServeLLMClientErrors(t *testing.T) {
	oldLoad, oldAttach := serveLoadClient, serveAttachAPILogger
	t.Cleanup(func() { serveLoadClient, serveAttachAPILogger = oldLoad, oldAttach })

	serveLoadClient = func(string) (*llm.Client, error) { return nil, errors.New("load") }
	if _, _, err := newServeLLMClient(t.TempDir(), io.Discard); err == nil {
		t.Fatal("expected load error")
	}

	serveLoadClient = func(string) (*llm.Client, error) { return llm.NewClient(), nil }
	serveAttachAPILogger = func(*llm.Client, string, io.Writer) (func() error, error) {
		return nil, errors.New("attach")
	}
	if _, _, err := newServeLLMClient(t.TempDir(), io.Discard); err == nil {
		t.Fatal("expected attach error")
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
			{"client providers", TestClientHasProvider},
			{"noninteractive", TestRunServeNonInteractiveFlagControlsPromptAddendum},
			{"shutdown waits", TestRunServeShutdownWaitsForInFlightInput},
			{"goal", TestServeGoal_TUIPathEndToEnd},
		}
		for _, tc := range tests {
			t.Run(tc.name, tc.fn)
		}
	})
}
