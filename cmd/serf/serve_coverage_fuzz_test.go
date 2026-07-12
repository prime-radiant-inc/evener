//go:build serffuzz

package main

import (
	"errors"
	"io"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

func testServeLLMClientErrors(t *testing.T) {
	oldLoad, oldAttach := serveLoadClient, serveAttachAPILogger
	t.Cleanup(func() { serveLoadClient, serveAttachAPILogger = oldLoad, oldAttach })

	serveLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
		return nil, providercfg.Config{}, false, errors.New("load")
	}
	if _, _, _, _, err := newServeLLMClient(t.TempDir(), io.Discard); err == nil {
		t.Fatal("expected load error")
	}

	serveLoadClient = func(...llm.EnvOption) (*llm.Client, providercfg.Config, bool, error) {
		return llm.NewClient(), providercfg.Config{}, false, nil
	}
	serveAttachAPILogger = func(*llm.Client, string, io.Writer) (func() error, error) {
		return nil, errors.New("attach")
	}
	if _, _, _, _, err := newServeLLMClient(t.TempDir(), io.Discard); err == nil {
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
			{"profile materialized", TestBuildInitialProfile_MaterializedInstance},
			{"bare model", TestRunServe_BareModelRejected},
			{"missing model", TestRunServe_MissingModel},
			{"env help", TestPrintServeEnvVars_IncludesOpenAIResponsesContinuation},
			{"api log", TestServeClient_APILogWritesJSONL},
			{"resume missing", TestRunServe_ResumeNonexistent},
			{"status empty", TestAgentToServerDetailedStatus_Empty},
			{"status partial", TestAgentToServerDetailedStatus_Partial},
			{"usage zero", TestSerfUsageFromLLM_ZeroReturnsNil},
			{"usage totals", TestSerfUsageFromLLM_MapsTotals},
			{"usage cache", TestSerfUsageFromLLM_NonZeroCacheReadOnlyStillReturns},
			{"cheap same", TestApplyFastCheapModel_SameProviderOverridesCheapModel},
			{"cheap cross", TestApplyFastCheapModel_CrossProviderWhenRegistered},
			{"cheap rejected", TestApplyFastCheapModel_CrossProviderRejectedWhenNotRegistered},
			{"cheap bare", TestApplyFastCheapModel_BareModelKeepsActiveProvider},
			{"cheap blank", TestApplyFastCheapModel_BlankKeepsDefault},
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
