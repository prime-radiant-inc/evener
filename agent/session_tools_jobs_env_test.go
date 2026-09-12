package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

func TestProjectStableDelegateStatus_EnvironmentFields(t *testing.T) {
	t.Parallel()
	netDisabled := false
	snapshot := delegateSnapshot{
		id:        "dlg_test_env",
		lifecycle: "running",
		descriptor: delegatestore.Descriptor{
			Task:            "test task",
			AgentType:       "subagent",
			WorkingDir:      "/home/user/project",
			Isolation:       "worktree",
			ToolNameCeiling: []string{"read_file", "write_file"},
			Sandbox: &delegatestore.SandboxSnapshot{
				Mode:    "workspace-write",
				Network: &netDisabled,
			},
		},
	}

	now := time.Date(2026, 9, 2, 19, 10, 7, 0, time.UTC)
	result := projectStableDelegateStatus(now, snapshot)

	if result.Cwd != "/home/user/project" {
		t.Errorf("Cwd = %q, want %q", result.Cwd, "/home/user/project")
	}
	if result.Isolation != "worktree" {
		t.Errorf("Isolation = %q, want %q", result.Isolation, "worktree")
	}
	if result.SandboxMode != "workspace-write" {
		t.Errorf("SandboxMode = %q, want %q", result.SandboxMode, "workspace-write")
	}
	if result.SandboxNetwork == nil {
		t.Fatal("SandboxNetwork is nil, want false")
	}
	if *result.SandboxNetwork != false {
		t.Errorf("SandboxNetwork = %v, want false", *result.SandboxNetwork)
	}
}

func TestProjectStableDelegateStatus_NilSandboxNetworkDefaultsToTrue(t *testing.T) {
	t.Parallel()
	snapshot := delegateSnapshot{
		id:        "dlg_test_net",
		lifecycle: "running",
		descriptor: delegatestore.Descriptor{
			Task:      "test task",
			AgentType: "subagent",
			Sandbox: &delegatestore.SandboxSnapshot{
				Mode: "read-only",
				// Network is nil — should default to true (enabled)
			},
		},
	}

	result := projectStableDelegateStatus(time.Now(), snapshot)

	if result.SandboxNetwork == nil {
		t.Fatal("SandboxNetwork is nil, want true (effective default for nil network)")
	}
	if *result.SandboxNetwork != true {
		t.Errorf("SandboxNetwork = %v, want true (nil network means enabled)", *result.SandboxNetwork)
	}
}

func TestProjectStableDelegateStatus_NoSandboxOmitsFields(t *testing.T) {
	t.Parallel()
	snapshot := delegateSnapshot{
		id:        "dlg_test_nosandbox",
		lifecycle: "running",
		descriptor: delegatestore.Descriptor{
			Task:      "test task",
			AgentType: "subagent",
			// Sandbox is nil
		},
	}

	result := projectStableDelegateStatus(time.Now(), snapshot)

	if result.SandboxMode != "" {
		t.Errorf("SandboxMode = %q, want empty (no sandbox)", result.SandboxMode)
	}
	if result.SandboxNetwork != nil {
		t.Errorf("SandboxNetwork = %v, want nil (no sandbox)", *result.SandboxNetwork)
	}
}
