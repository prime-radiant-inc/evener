package agent

import (
	"testing"
)

// TestManageWorktreeToolRegisteredRegistryOnlyNonReadOnly asserts spec §2's
// registration shape (mirroring session_init_registry_test.go's registry-tool
// checks): manage_worktree is registered directly on the registry (not part
// of the provider profile's own tool definitions, like update_goal/task_list),
// it is non-read-only (so execToolBatch serializes it, per spec), and it is
// advertised to the model via ToolDefinitions().
func TestManageWorktreeToolRegisteredRegistryOnlyNonReadOnly(t *testing.T) {
	t.Parallel()
	s := newSession(t)

	rt := s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	if rt.ReadOnly {
		t.Error("manage_worktree.ReadOnly = true, want false (list is part of a stateful lifecycle tool)")
	}

	for _, td := range s.profile.ToolDefinitions() {
		if td.Name == "manage_worktree" {
			t.Error("manage_worktree must not be part of the provider profile's tool definitions (registry-only per spec §2)")
		}
	}

	found := false
	for _, td := range s.ToolDefinitions() {
		if td.Name == "manage_worktree" {
			found = true
			break
		}
	}
	if !found {
		t.Error("manage_worktree not advertised in ToolDefinitions()")
	}
}

// TestManageWorktreeToolUnimplementedHandler asserts the Task-12 skeleton's
// stub handler returns a clear error rather than panicking or silently
// no-opping. Real operation semantics land in Tasks 13-16.
func TestManageWorktreeToolUnimplementedHandler(t *testing.T) {
	t.Parallel()
	s := newSession(t)

	rt := s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	_, err := rt.Exec(t.Context(), s.currentEnv(), map[string]any{"operation": "list"})
	if err == nil {
		t.Fatal("expected an error from the not-yet-implemented handler, got nil")
	}
}
