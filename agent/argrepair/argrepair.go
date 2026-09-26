// Package argrepair provides read-only access to the JSON repair and argument
// validation machinery for packages outside agent/internal/. The repair
// functions themselves are unchanged; this is a thin re-export so the
// transcript projection path (internal/apptranscript) can reconstruct healed
// communicate messages and apply the same size gate the live path uses,
// without importing agent/internal/ directly.
package argrepair

import (
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/tool/repair"
)

// MaxToolArgumentBytes is the same byte cap the live path enforces before
// parsing tool arguments. Re-exported so the projection side can apply the
// same size gate before extracting intent from oversized valid JSON.
const MaxToolArgumentBytes = tool.MaxToolArgumentBytes

// RepairJSON heals malformed JSON tool-argument bytes using the same
// machinery the live path used (agent/session_tool_repair.go calls
// repair.RepairJSON). Read-only: the repair package is unchanged. Returns
// the repaired bytes (which may equal raw when no repair was possible).
func RepairJSON(raw []byte) []byte {
	repaired, _ := repair.RepairJSON(raw)
	return repaired
}

// ValidateRawArguments rejects raw tool argument bytes that must never reach
// a lossy JSON decode or an allocation-heavy schema path. Re-exported so the
// projection side can apply the same validation the live path uses.
func ValidateRawArguments(arguments []byte) error {
	return tool.ValidateRawArguments(arguments)
}
