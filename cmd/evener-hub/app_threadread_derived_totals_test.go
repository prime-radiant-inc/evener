package hub

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
)

// TestColdReadDerivesTotalsWithoutScan pins that the first daemonless read of a
// session with a current sidecar answers its token total and failed-tool-call
// count from the index, paying no transcript scan (#4667).
func TestColdReadDerivesTotalsWithoutScan(t *testing.T) {
	cfg, params, _ := seedLargePastThread(t, 200)

	original := pastDerivedTotalsScan
	scanCalls := 0
	pastDerivedTotalsScan = func(path string, fromEntryOrdinal int) (*appwire.EvenerUsage, int, error) {
		scanCalls++
		return original(path, fromEntryOrdinal)
	}
	defer func() { pastDerivedTotalsScan = original }()

	pastTranscriptCache = apptranscript.NewTurnCache()
	response, found, err := pastThreadReadResponse(context.Background(), cfg, params)
	if err != nil || !found {
		t.Fatalf("pastThreadReadResponse = %v, %v", err, found)
	}
	if scanCalls != 0 {
		t.Fatalf("cold read scanned the transcript %d time(s); the current sidecar should answer", scanCalls)
	}
	if response.Thread.Evener.Usage == nil || response.Thread.Evener.FailedToolCalls == nil {
		t.Fatalf("cold read did not stamp derived totals: usage=%v failures=%v",
			response.Thread.Evener.Usage, response.Thread.Evener.FailedToolCalls)
	}
}
