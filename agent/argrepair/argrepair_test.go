package argrepair_test

import (
	"testing"

	"primeradiant.com/evener/agent/argrepair"
)

func TestRepairJSON_HealsMissingOuterBrace(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"key": "value"`)
	repaired := argrepair.RepairJSON(raw)
	if string(repaired) != `{"key": "value"}` {
		t.Fatalf("RepairJSON(%s) = %s, want healed json", raw, repaired)
	}
}
