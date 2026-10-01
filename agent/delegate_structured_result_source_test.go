package agent

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// A terminal packet names where its structured result came from (#3548): a
// result schema's own output, or a no-schema delegate's captured default
// output envelope. Without the marker a schema whose output is exactly the
// default envelope's shape is byte-identical to a no-schema capture, so a
// client cannot tell them apart to decide whether to unwrap to the envelope's
// data.
func TestStableDelegateFinishStampsStructuredResultSource(t *testing.T) {
	t.Parallel()
	t.Run("a no-schema capture is the default envelope", func(t *testing.T) {
		t.Parallel()
		finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
			result:                  "reported",
			communicated:            true,
			structuredResult:        map[string]any{"message": "Swept.", "data": map[string]any{"rows": 3}, "artifacts": []any{}},
			structuredResultPresent: true,
		})
		if got := finish.packet.StructuredResultSource; got != "default_envelope" {
			t.Fatalf("structured result source = %q, want %q", got, "default_envelope")
		}
	})
	t.Run("a schema result is the schema source", func(t *testing.T) {
		t.Parallel()
		finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
			result:                  "reported",
			communicated:            true,
			structuredResult:        map[string]any{"answer": "yes"},
			structuredResultPresent: true,
			descriptor: delegatestore.Descriptor{
				ResultSchema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`),
			},
		})
		if got := finish.packet.StructuredResultSource; got != "schema" {
			t.Fatalf("structured result source = %q, want %q", got, "schema")
		}
	})
	t.Run("a packet with no structured result carries no source", func(t *testing.T) {
		t.Parallel()
		finish := stableDelegateFinishFromRun(delegateTerminalRunInputs{
			result:       "plain report",
			communicated: true,
		})
		if got := finish.packet.StructuredResultSource; got != "" {
			t.Fatalf("structured result source = %q, want empty", got)
		}
	})
}
