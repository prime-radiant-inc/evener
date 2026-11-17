package appoverlay

import (
	"bytes"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// The streamed-delta probe of issue #6542: one 4 KiB text delta applied to a
// stream slot that already holds heldBytes, with the overlay, the slot and its
// buffer built outside the measured region. The buffer is sized to hold the
// delta exactly, so its growth is never charged to the delta and the only
// allocations left are the ones the delta's own path makes.
//
// heldBytes sweeps the sizes the issue measured: empty, a typical reply, and
// the largest reply its table covered.
func BenchmarkOverlayTextDelta(b *testing.B) {
	for _, held := range []struct {
		name  string
		bytes int
	}{
		{"held_0KB", 0},
		{"held_100KB", 100 << 10},
		{"held_252KB", 252 << 10},
	} {
		b.Run(held.name, func(b *testing.B) {
			const delta = 4 << 10
			o, s := streamedSlot(held.bytes, delta)
			ev := events.New(events.AssistantTextDeltaData{Delta: string(bytes.Repeat([]byte("d"), delta))})
			b.ReportAllocs()
			for b.Loop() {
				s.text = s.text[:held.bytes]
				changes := o.Event(ev)
				if len(changes) != 1 {
					b.Fatalf("changes = %d, want one frame per delta", len(changes))
				}
				benchChangeSink = changes[0]
			}
		})
	}
}

// BenchmarkOverlayOutputDelta is the same probe for a running call's output,
// which appendOutput answers with the same one-frame delta.
func BenchmarkOverlayOutputDelta(b *testing.B) {
	for _, held := range []struct {
		name  string
		bytes int
	}{
		{"held_0KB", 0},
		{"held_100KB", 100 << 10},
		{"held_252KB", 252 << 10},
	} {
		b.Run(held.name, func(b *testing.B) {
			const delta = 4 << 10
			o := newOverlay()
			b.Cleanup(o.Close)
			o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
			o.Event(events.New(events.ToolCallStartData{ToolName: "bash", CallID: "c_1"}))
			s := o.slots["tool:call:c_1"]
			s.output = slices.Grow(bytes.Repeat([]byte("x"), held.bytes), delta)[:held.bytes]
			ev := events.New(events.ToolCallOutputDeltaData{CallID: "c_1", Delta: string(bytes.Repeat([]byte("d"), delta))})
			b.ReportAllocs()
			for b.Loop() {
				s.output = s.output[:held.bytes]
				changes := o.Event(ev)
				if len(changes) != 1 {
					b.Fatalf("changes = %d, want one frame per delta", len(changes))
				}
				benchChangeSink = changes[0]
			}
		})
	}
}

// BenchmarkOverlayPreviewDelta is the same probe for a communicate preview,
// the other branch Overlay.Event reaches appendText from.
func BenchmarkOverlayPreviewDelta(b *testing.B) {
	for _, held := range []struct {
		name  string
		bytes int
	}{
		{"held_0KB", 0},
		{"held_100KB", 100 << 10},
		{"held_252KB", 252 << 10},
	} {
		b.Run(held.name, func(b *testing.B) {
			const delta = 4 << 10
			o := newOverlay()
			b.Cleanup(o.Close)
			o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
			o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_1"}))
			s := o.slots["preview:c_1"]
			s.text = slices.Grow(bytes.Repeat([]byte("x"), held.bytes), delta)[:held.bytes]
			ev := events.New(events.CommunicatePreviewDeltaData{CallID: "c_1", Delta: string(bytes.Repeat([]byte("d"), delta))})
			b.ReportAllocs()
			for b.Loop() {
				s.text = s.text[:held.bytes]
				changes := o.Event(ev)
				if len(changes) != 1 {
					b.Fatalf("changes = %d, want one frame per delta", len(changes))
				}
				benchChangeSink = changes[0]
			}
		})
	}
}

// streamedSlot returns an overlay holding one open stream whose text already
// holds heldBytes, with room for one delta on top of it so the delta itself
// never grows the buffer.
func streamedSlot(heldBytes, deltaBytes int) (*Overlay, *slot) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "s"}))
	s := o.slots["stream:r_1/1:agentMessage"]
	// One append of the held text: growing a fresh buffer to heldBytes+delta
	// here keeps its cost out of the measured region, and truncating back to
	// heldBytes leaves the capacity to take the delta without reallocating.
	s.text = slices.Grow(bytes.Repeat([]byte("x"), heldBytes), deltaBytes)[:heldBytes]
	return o, s
}

// benchChangeSink keeps the returned frame alive, as a real caller's commit
// does.
var benchChangeSink Change
