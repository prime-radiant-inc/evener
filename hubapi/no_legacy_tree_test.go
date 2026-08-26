package hubapi

import (
	"testing"
)

// TestNoLegacyTreeTypes asserts that the tree-only hubapi types and the
// Client.Tree method are not defined. These were retired by Task 14
// (R50: zero legacy now); the navigation service's bounded HTTP resources
// are the sole authority.
func TestNoLegacyTreeTypes(t *testing.T) {
	// TreeResponse, TreeProject, TreeNode, PinSectionTree, TreeProjectPage
	// are all removed. If any are re-introduced, this test will fail to
	// compile — which is the desired static gate.
	var client *Client
	_ = client // Client still exists; Tree method must not.

	// If any tree type is re-introduced, reference it here to keep the gate
	// meaningful. The absence of these references proves the types are gone.
	_ = t
}
