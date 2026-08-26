package appwire

import (
	"testing"
)

// TestNoLegacyTreeNotification asserts that NotifyEvenerTreeChanged is not
// defined in the appwire protocol. The legacy tree-changed notification was
// retired by Task 14 (R50: zero legacy now); the navigation service's
// bounded HTTP resources are the sole authority.
func TestNoLegacyTreeNotification(t *testing.T) {
	for _, notification := range Notifications {
		if notification.Name == "evener/tree/changed" {
			t.Fatal("legacy evener/tree/changed notification is still registered")
		}
	}
}
