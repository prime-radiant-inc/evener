package agent

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// A tool call's output reaches a consumer twice: once as TOOL_CALL_OUTPUT_DELTA
// events and once on the TOOL_CALL_END that settles it. The end event's copy is
// the one that can be bounded without losing anything a live consumer does not
// already hold -- the deltas carry every byte, so the end event carries only the
// excerpt a reader that missed them renders.
//
// Measuring both per call makes the duplication visible as a ratio, so the
// benchmark states what the change removes rather than requiring a second run
// against another revision.
func BenchmarkToolCallOutputWireBytes(b *testing.B) {
	for _, size := range toolWireSizes {
		b.Run(size.name, func(b *testing.B) {
			text := toolWireText(size.bytes)
			b.ReportAllocs()
			b.ResetTimer()
			var deltaBytes, endBytes int
			for i := 0; i < b.N; i++ {
				deltaBytes, endBytes = toolCallWireBytes(text)
			}
			b.StopTimer()
			b.ReportMetric(float64(deltaBytes), "delta-bytes/call")
			b.ReportMetric(float64(endBytes), "end-bytes/call")
			if deltaBytes > 0 {
				b.ReportMetric(100*float64(endBytes)/float64(deltaBytes), "%of-deltas")
			}
		})
	}
}

// BenchmarkToolCallEndWireBytes measures the end event alone, so the bound's
// effect reads on its own line rather than as a difference of two totals.
func BenchmarkToolCallEndWireBytes(b *testing.B) {
	for _, size := range toolWireSizes {
		b.Run(size.name, func(b *testing.B) {
			text := toolWireText(size.bytes)
			b.ReportAllocs()
			b.ResetTimer()
			var endBytes int
			for i := 0; i < b.N; i++ {
				endBytes = toolCallEndWireBytes(text)
			}
			b.StopTimer()
			b.ReportMetric(float64(endBytes), "end-bytes/call")
		})
	}
}

var toolWireSizes = []struct {
	name  string
	bytes int
}{
	{name: "1KB", bytes: 1 << 10},
	{name: "20KB", bytes: 20 << 10},
	{name: "50KB", bytes: 50 << 10},
	{name: "256KB", bytes: 256 << 10},
}

func toolWireText(n int) string {
	full := make([]byte, n)
	for i := range full {
		full[i] = 'x'
	}
	return string(full)
}

// toolCallEndWireBytes is the encoded size of the event that settles a call
// whose full output was text.
func toolCallEndWireBytes(text string) int {
	excerpt, fullBytes := events.BoundToolField(text)
	encoded, err := json.Marshal(events.ToolCallEndData{
		ToolName: "read_file", CallID: "call_abc", ArgumentsJSON: `{"path":"/tmp/x"}`,
		Output: excerpt, OutputBytes: fullBytes,
	})
	if err != nil {
		panic(err)
	}
	return len(encoded)
}

// toolCallWireBytes is one call's emitted JSON: the delta events the emitter
// splits text into, and the end event that settles it.
func toolCallWireBytes(text string) (deltaBytes, endBytes int) {
	const chunk = 4000
	for i := 0; i < len(text); i += chunk {
		j := min(i+chunk, len(text))
		encoded, err := json.Marshal(events.ToolCallOutputDeltaData{ToolName: "read_file", CallID: "call_abc", Delta: text[i:j]})
		if err != nil {
			panic(err)
		}
		deltaBytes += len(encoded)
	}
	return deltaBytes, toolCallEndWireBytes(text)
}

// BenchmarkToolCallEndUnboundedWireBytes is the contrast the bound is measured
// against: the same event carrying the whole output, encoded for every size, so
// the bounded figure above can be compared without checking out another
// revision. It is a benchmark rather than an assertion because it states a cost,
// not a contract.
func BenchmarkToolCallEndUnboundedWireBytes(b *testing.B) {
	for _, size := range toolWireSizes {
		b.Run(size.name, func(b *testing.B) {
			text := toolWireText(size.bytes)
			b.ReportAllocs()
			b.ResetTimer()
			var endBytes int
			for i := 0; i < b.N; i++ {
				encoded, err := json.Marshal(events.ToolCallEndData{
					ToolName: "read_file", CallID: "call_abc", ArgumentsJSON: `{"path":"/tmp/x"}`,
					Output: text,
				})
				if err != nil {
					b.Fatal(err)
				}
				endBytes = len(encoded)
			}
			b.StopTimer()
			b.ReportMetric(float64(endBytes), "end-bytes/call")
			b.ReportMetric(float64(len(text)), "output-bytes")
			b.ReportMetric(float64(endBytes)/float64(len(text)), "wire-per-output-byte")
		})
	}
}
