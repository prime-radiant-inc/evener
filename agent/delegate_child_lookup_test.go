package agent

import "testing"

// TestSnapshotsForChildSessionCapturesOnlyThatChild pins the cost shape of
// the per-child drive gates. Every tool round the root checks each live child
// against stop, drain-abandoned and drain-grace gates, and each check needs
// that one child's delegate row; capturing the whole tree for every check made
// a round cost O(children^2) deep copies. The lookup captures only the rows
// that name the child session.
func TestSnapshotsForChildSessionCapturesOnlyThatChild(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	for _, id := range []string{"dlg_a", "dlg_b", "dlg_c"} {
		seedDelegateControllerIdle(t, c, id, "")
	}

	rows := c.snapshotsForChildSession("child-dlg_b")
	if len(rows) != 1 || rows[0].id != "dlg_b" {
		t.Fatalf("snapshotsForChildSession = %+v, want only dlg_b", rows)
	}
	if rows := c.snapshotsForChildSession("child-unknown"); len(rows) != 0 {
		t.Fatalf("unknown child = %+v, want none", rows)
	}
}

// TestDirectStableDelegateForChildSessionHonorsVisibility keeps the lookup's
// contract: only a direct child of the asking session is found.
func TestDirectStableDelegateForChildSessionHonorsVisibility(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_nested", "dlg_parent")
	root := &Session{id: "root-session", delegateController: c}

	row, ok := root.directStableDelegateForChildSession("child-dlg_parent")
	if !ok || row.id != "dlg_parent" {
		t.Fatalf("direct child = %+v, %v; want dlg_parent", row, ok)
	}
	if row, ok := root.directStableDelegateForChildSession("child-dlg_nested"); ok {
		t.Fatalf("grandchild resolved as a direct child: %+v", row)
	}
	// The seed's rows all name root-session as owner, so a session owning
	// dlg_parent with that id sees dlg_nested as its direct child.
	parent := &Session{id: "root-session", owningDelegateID: "dlg_parent", delegateController: c}
	if row, ok := parent.directStableDelegateForChildSession("child-dlg_nested"); !ok || row.id != "dlg_nested" {
		t.Fatalf("nested child from its parent = %+v, %v; want dlg_nested", row, ok)
	}
}
