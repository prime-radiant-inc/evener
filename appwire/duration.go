package appwire

import "time"

// DurationMillis converts a duration to whole milliseconds for the wire.
//
// A positive duration shorter than one millisecond rounds up to 1 rather than
// truncating to 0. Zero is the value this contract and its clients reserve for
// "disabled" — a zero daemon idle timeout means retirement is off, and the Hub
// UI renders 0 as disabled — so truncating would report an armed daemon's
// sub-millisecond deadline as though retirement were off. Zero itself still
// maps to zero, and a negative duration is passed through unchanged; the
// daemon rejects a negative deadline before it reaches this conversion.
func DurationMillis(d time.Duration) int64 {
	if d > 0 && d < time.Millisecond {
		return 1
	}
	return d.Milliseconds()
}
