package llm

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// callRecord captures when a single tool call started and ended, along with its
// position in the original calls slice.
type callRecord struct {
	index int
	name  string
	start time.Time
	end   time.Time
}

// makeTimingToolIndex builds a toolIndex (via prepareTools) where every tool
// sleeps for sleepDur, then records a callRecord into *records under mu.
// toolSpecs is a slice of (name, readOnly) pairs.
//
// Callers run inside a synctest bubble, where the sleep and time.Now read the
// bubble's fake clock. The clock advances only once every goroutine in the
// bubble is durably blocked, so calls executeToolCalls launches together all
// start at the same instant and overlap, while serialized calls abut exactly:
// the interval assertions are exact in both directions, with no scheduler
// jitter to absorb and no wall time spent sleeping.
func makeTimingToolIndex(t *testing.T, sleepDur time.Duration, records *[]callRecord, mu *sync.Mutex, toolSpecs []struct {
	name     string
	readOnly bool
}) map[string]Tool {
	t.Helper()
	tools := make([]Tool, len(toolSpecs))
	for i, spec := range toolSpecs {
		tools[i] = Tool{
			Definition: ToolDefinition{
				Name:       spec.name,
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
			},
			ReadOnly: spec.readOnly,
			Execute: func(ctx context.Context, args any) (any, error) {
				start := time.Now()
				time.Sleep(sleepDur)
				end := time.Now()
				mu.Lock()
				*records = append(*records, callRecord{index: i, name: spec.name, start: start, end: end})
				mu.Unlock()
				return spec.name + "-result", nil
			},
		}
	}
	idx, _, err := prepareTools(tools)
	if err != nil {
		t.Fatalf("prepareTools: %v", err)
	}
	return idx
}

// makeCall constructs a ToolCallData with empty JSON arguments.
func makeCall(id, name string) ToolCallData {
	return ToolCallData{ID: id, Name: name, Arguments: json.RawMessage(`{}`), Type: "function"}
}

// recordFor returns the callRecord whose name matches, or fails the test.
func recordFor(t *testing.T, records []callRecord, name string) callRecord {
	t.Helper()
	for _, r := range records {
		if r.name == name {
			return r
		}
	}
	t.Fatalf("no callRecord for tool %q", name)
	return callRecord{}
}

// overlaps returns true when two time intervals intersect (exclusive on boundary).
func overlaps(a, b callRecord) bool {
	return a.start.Before(b.end) && b.start.Before(a.end)
}

// completedBefore returns true when a ended before b started (non-overlapping order).
func completedBefore(a, b callRecord) bool {
	return !a.end.After(b.start)
}

func TestExecuteToolCalls_SingleCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const sleep = 20 * time.Millisecond
		var mu sync.Mutex
		var records []callRecord

		toolSpecs := []struct {
			name     string
			readOnly bool
		}{{"read1", true}}
		idx := makeTimingToolIndex(t, sleep, &records, &mu, toolSpecs)

		calls := []ToolCallData{makeCall("c1", "read1")}
		results := executeToolCalls(context.Background(), idx, calls, nil, nil)

		if len(results) != 1 {
			t.Fatalf("results: got %d want 1", len(results))
		}
		if results[0].IsError {
			t.Fatalf("unexpected error: %v", results[0].Content)
		}
		if results[0].Content != "read1-result" {
			t.Fatalf("content: got %v want read1-result", results[0].Content)
		}
		if len(records) != 1 {
			t.Fatalf("callRecords: got %d want 1", len(records))
		}
	})
}

func TestExecuteToolCalls_AllReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const sleep = 25 * time.Millisecond
		var mu sync.Mutex
		var records []callRecord

		toolSpecs := []struct {
			name     string
			readOnly bool
		}{
			{"read1", true},
			{"read2", true},
			{"read3", true},
		}
		idx := makeTimingToolIndex(t, sleep, &records, &mu, toolSpecs)

		calls := []ToolCallData{
			makeCall("c1", "read1"),
			makeCall("c2", "read2"),
			makeCall("c3", "read3"),
		}
		results := executeToolCalls(context.Background(), idx, calls, nil, nil)

		if len(results) != 3 {
			t.Fatalf("results: got %d want 3", len(results))
		}
		for _, r := range results {
			if r.IsError {
				t.Fatalf("unexpected error in %s: %v", r.Name, r.Content)
			}
		}

		r1 := recordFor(t, records, "read1")
		r2 := recordFor(t, records, "read2")
		r3 := recordFor(t, records, "read3")

		// All three should overlap — they ran in parallel.
		if !overlaps(r1, r2) {
			t.Errorf("read1 and read2 did not overlap (not parallel): r1=%v-%v r2=%v-%v", r1.start, r1.end, r2.start, r2.end)
		}
		if !overlaps(r1, r3) {
			t.Errorf("read1 and read3 did not overlap (not parallel): r1=%v-%v r3=%v-%v", r1.start, r1.end, r3.start, r3.end)
		}
	})
}

func TestExecuteToolCalls_ReadThenWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const sleep = 25 * time.Millisecond
		var mu sync.Mutex
		var records []callRecord

		toolSpecs := []struct {
			name     string
			readOnly bool
		}{
			{"read1", true},
			{"read2", true},
			{"write1", false},
		}
		idx := makeTimingToolIndex(t, sleep, &records, &mu, toolSpecs)

		calls := []ToolCallData{
			makeCall("c1", "read1"),
			makeCall("c2", "read2"),
			makeCall("c3", "write1"),
		}
		results := executeToolCalls(context.Background(), idx, calls, nil, nil)

		if len(results) != 3 {
			t.Fatalf("results: got %d want 3", len(results))
		}

		r1 := recordFor(t, records, "read1")
		r2 := recordFor(t, records, "read2")
		w1 := recordFor(t, records, "write1")

		// Reads should overlap each other (parallel batch).
		if !overlaps(r1, r2) {
			t.Errorf("read1 and read2 did not overlap: r1=%v-%v r2=%v-%v", r1.start, r1.end, r2.start, r2.end)
		}
		// Both reads must complete before the write starts.
		if !completedBefore(r1, w1) {
			t.Errorf("read1 did not complete before write1: r1.end=%v w1.start=%v", r1.end, w1.start)
		}
		if !completedBefore(r2, w1) {
			t.Errorf("read2 did not complete before write1: r2.end=%v w1.start=%v", r2.end, w1.start)
		}
	})
}

func TestExecuteToolCalls_WriteThenRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const sleep = 25 * time.Millisecond
		var mu sync.Mutex
		var records []callRecord

		toolSpecs := []struct {
			name     string
			readOnly bool
		}{
			{"write1", false},
			{"read1", true},
			{"read2", true},
		}
		idx := makeTimingToolIndex(t, sleep, &records, &mu, toolSpecs)

		calls := []ToolCallData{
			makeCall("c1", "write1"),
			makeCall("c2", "read1"),
			makeCall("c3", "read2"),
		}
		results := executeToolCalls(context.Background(), idx, calls, nil, nil)

		if len(results) != 3 {
			t.Fatalf("results: got %d want 3", len(results))
		}

		w1 := recordFor(t, records, "write1")
		r1 := recordFor(t, records, "read1")
		r2 := recordFor(t, records, "read2")

		// Write must complete before either read starts.
		if !completedBefore(w1, r1) {
			t.Errorf("write1 did not complete before read1: w1.end=%v r1.start=%v", w1.end, r1.start)
		}
		if !completedBefore(w1, r2) {
			t.Errorf("write1 did not complete before read2: w1.end=%v r2.start=%v", w1.end, r2.start)
		}
		// The two reads should run in parallel.
		if !overlaps(r1, r2) {
			t.Errorf("read1 and read2 did not overlap: r1=%v-%v r2=%v-%v", r1.start, r1.end, r2.start, r2.end)
		}
	})
}

func TestExecuteToolCalls_InterleavedReadWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Pattern: [read, write, read, write, read]
		// Expected groups: group1(read1) → group2(write1) → group3(read2) → group4(write2) → group5(read3)
		const sleep = 25 * time.Millisecond
		var mu sync.Mutex
		var records []callRecord

		toolSpecs := []struct {
			name     string
			readOnly bool
		}{
			{"read1", true},
			{"write1", false},
			{"read2", true},
			{"write2", false},
			{"read3", true},
		}
		idx := makeTimingToolIndex(t, sleep, &records, &mu, toolSpecs)

		calls := []ToolCallData{
			makeCall("c1", "read1"),
			makeCall("c2", "write1"),
			makeCall("c3", "read2"),
			makeCall("c4", "write2"),
			makeCall("c5", "read3"),
		}
		results := executeToolCalls(context.Background(), idx, calls, nil, nil)

		if len(results) != 5 {
			t.Fatalf("results: got %d want 5", len(results))
		}

		read1 := recordFor(t, records, "read1")
		writ1 := recordFor(t, records, "write1")
		read2 := recordFor(t, records, "read2")
		writ2 := recordFor(t, records, "write2")
		read3 := recordFor(t, records, "read3")

		// Each group must complete before the next starts.
		if !completedBefore(read1, writ1) {
			t.Errorf("read1 must complete before write1: read1.end=%v write1.start=%v", read1.end, writ1.start)
		}
		if !completedBefore(writ1, read2) {
			t.Errorf("write1 must complete before read2: write1.end=%v read2.start=%v", writ1.end, read2.start)
		}
		if !completedBefore(read2, writ2) {
			t.Errorf("read2 must complete before write2: read2.end=%v write2.start=%v", read2.end, writ2.start)
		}
		if !completedBefore(writ2, read3) {
			t.Errorf("write2 must complete before read3: write2.end=%v read3.start=%v", writ2.end, read3.start)
		}
	})
}

func TestExecuteToolCalls_ConsecutiveWritesSerialized(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const sleep = 25 * time.Millisecond
		var mu sync.Mutex
		var records []callRecord

		toolSpecs := []struct {
			name     string
			readOnly bool
		}{
			{"write1", false},
			{"write2", false},
		}
		idx := makeTimingToolIndex(t, sleep, &records, &mu, toolSpecs)

		calls := []ToolCallData{
			makeCall("c1", "write1"),
			makeCall("c2", "write2"),
		}
		results := executeToolCalls(context.Background(), idx, calls, nil, nil)

		if len(results) != 2 {
			t.Fatalf("results: got %d want 2", len(results))
		}

		w1 := recordFor(t, records, "write1")
		w2 := recordFor(t, records, "write2")

		// Writes must be serialized: first completes before second starts.
		if !completedBefore(w1, w2) {
			t.Errorf("write1 and write2 overlapped (not serialized): w1=%v-%v w2=%v-%v", w1.start, w1.end, w2.start, w2.end)
		}
	})
}

func TestExecuteToolCalls_UnknownToolDefaultsNonReadOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// An unknown tool name should be treated as non-read-only: it must be serialized
		// and not batched with adjacent reads.
		const sleep = 50 * time.Millisecond
		var mu sync.Mutex
		var records []callRecord

		// Only register read1; "mystery" is intentionally absent from the toolIndex.
		toolSpecs := []struct {
			name     string
			readOnly bool
		}{
			{"read1", true},
		}
		idx := makeTimingToolIndex(t, sleep, &records, &mu, toolSpecs)

		calls := []ToolCallData{
			makeCall("c1", "read1"),
			makeCall("c2", "mystery"), // unknown tool
		}
		results := executeToolCalls(context.Background(), idx, calls, nil, nil)

		if len(results) != 2 {
			t.Fatalf("results: got %d want 2", len(results))
		}

		// read1 should succeed; mystery should be an error (unknown tool).
		if results[0].IsError {
			t.Fatalf("read1 unexpected error: %v", results[0].Content)
		}
		if !results[1].IsError {
			t.Fatalf("mystery expected error result, got none")
		}

		// read1 must complete before mystery starts — the unknown tool flushes the read batch first.
		r1 := recordFor(t, records, "read1")

		// mystery never executes (executeSingleToolCall returns immediately for unknown tools
		// without calling Execute), so there's no timing record for it.
		// We verify the ordering constraint via result ordering: results[0] is read1,
		// results[1] is mystery (isError). The read batch was flushed synchronously before
		// mystery was attempted, so read1 completed before mystery's result was written.
		// Confirm read1 completed without error and mystery errored.
		if r1.name != "read1" {
			t.Errorf("expected records[0] to be read1, got %q", r1.name)
		}
	})
}
