package agent

import (
	"strings"
	"testing"
)

func TestFormatDelegateSendDeduplicatesActionStatus(t *testing.T) {
	t.Parallel()
	// Case 1: Action == Status ("completed" == "completed")
	out := delegateSendResult{
		DelegateID: "dlg_dedup_1",
		Action:     "completed",
		Status:     "completed",
	}
	formatted := formatDelegateSend(out)
	if !strings.Contains(formatted, "[delegate_id dlg_dedup_1 · completed]") {
		t.Errorf("formatted footer = %q, want '[delegate_id dlg_dedup_1 · completed]'", formatted)
	}
	if strings.Contains(formatted, "completed · completed") {
		t.Errorf("formatted footer stutters: %q", formatted)
	}

	// Case 2: Action != Status ("delivered" != "completed")
	outDiff := delegateSendResult{
		DelegateID: "dlg_dedup_2",
		Action:     "delivered",
		Status:     "completed",
	}
	formattedDiff := formatDelegateSend(outDiff)
	if !strings.Contains(formattedDiff, "[delegate_id dlg_dedup_2 · delivered · completed]") {
		t.Errorf("formatted footer = %q, want '[delegate_id dlg_dedup_2 · delivered · completed]'", formattedDiff)
	}
}
