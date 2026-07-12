//go:build serffuzz

package main

import "testing"

// FuzzServeSeedCoverage replays deterministic daemon workflows as part of the
// committed fuzz seed bank. Their provider boundary is scripted in-process.
func FuzzServeSeedCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		tests := []struct {
			name string
			fn   func(*testing.T)
		}{
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
