package hub

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// The past-enrichment reader stamps its own error `daemonless` before the live
// branch sees it (pastThreadItemReadResponseOwned -> daemonlessReadError). If
// the live stamp only rewrites a plain ErrorData, that already-stamped
// HistoryReadErrorData passes through untouched and the daemonless lie the fix
// exists to remove survives it.
func TestAlreadyStampedEnrichmentErrorTakesTheLiveIdentity(t *testing.T) {
	past := daemonlessReadError(appwire.TranscriptHistoryFailed(7))
	stamped := liveEnrichmentReadError(past, "4")
	var wireErr appwire.WireError
	if !errors.As(stamped, &wireErr) {
		t.Fatalf("stamped error = %T %v, want appwire.WireError", stamped, stamped)
	}
	data, ok := wireErr.Data.(appwire.HistoryReadErrorData)
	if !ok {
		t.Fatalf("stamped data = %#v, want HistoryReadErrorData", wireErr.Data)
	}
	if data.BootGeneration != "4" {
		t.Fatalf("stamped boot generation = %q, want the live answer's own 4", data.BootGeneration)
	}
	if data.EvenerErrorInfo != appwire.ErrorTranscriptHistoryFailed {
		t.Fatalf("stamped info = %q, want the history diagnostic preserved", data.EvenerErrorInfo)
	}
}
