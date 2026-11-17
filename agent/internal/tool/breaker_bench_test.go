package tool

import (
	"strings"
	"testing"
)

// BenchmarkFailureLedger_RecordRepeatFailure measures the per-dispatch record
// path for a failing call. Before the exact-store dead state was removed, record
// maintained a failure streak on both the exact entry and the semantic entry,
// so it ran errorClass (a SHA-256) and TruncateRunes over the output twice per
// dispatch. Only the semantic entry's run is read now, so each of those runs
// once. The output is large enough that the duplicate work is visible.
func BenchmarkFailureLedger_RecordRepeatFailure(b *testing.B) {
	l := newFailureLedger()
	key := newDispatchKey("read_file", []byte(`{"path":"broken"}`), nil)
	output := strings.Repeat("error: connection refused while dialing the host; retry was not scheduled\n", 40)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		l.record(key, true, output)
	}
}

// BenchmarkFailureLedger_RecordLargeSuccessfulOutput measures the repetition
// fingerprint's cost on the shape that made it worth replacing: a multi-megabyte
// successful tool result, which every judged call hashes in full. The old
// fingerprint was a SHA-256 over the whole body; this one is maphash.
func BenchmarkFailureLedger_RecordLargeSuccessfulOutput(b *testing.B) {
	l := newFailureLedger()
	key := newDispatchKey("read_file", []byte(`{"path":"big"}`), nil)
	output := strings.Repeat("line of representative tool output content here\n", 64<<10)

	b.SetBytes(int64(len(output)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		l.record(key, false, output)
	}
}
