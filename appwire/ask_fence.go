package appwire

import "strings"

// ValidateAskFence accepts an omitted pair or a complete pair. Revision zero
// identifies an observed empty set and must remain distinct from omission.
func ValidateAskFence(revision *uint64, bootGeneration string) error {
	if revision == nil && bootGeneration == "" {
		return nil
	}
	if revision == nil || strings.TrimSpace(bootGeneration) == "" {
		return InvalidParams("ask revision and boot generation must be supplied together")
	}
	return nil
}
