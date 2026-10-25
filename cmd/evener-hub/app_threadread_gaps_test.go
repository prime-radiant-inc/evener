package hub

import (
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

// TestCloneHubBoolNonNil covers the non-nil path.
func TestCloneHubBoolNonNil(t *testing.T) {
	v := true
	clone := cloneHubBool(&v)
	if clone == nil || *clone != true {
		t.Fatalf("cloneHubBool(&true) = %v, want &true", clone)
	}
	*clone = false
	if v != true {
		t.Fatalf("original was mutated: v = %v, want true", v)
	}
}

// TestCloneHubBoolNil covers the nil path.
func TestCloneHubBoolNil(t *testing.T) {
	if clone := cloneHubBool(nil); clone != nil {
		t.Fatalf("cloneHubBool(nil) = %v, want nil", clone)
	}
}

// TestCloneHubInt64NonNil covers the non-nil path.
func TestCloneHubInt64NonNil(t *testing.T) {
	v := int64(42)
	clone := cloneHubInt64(&v)
	if clone == nil || *clone != 42 {
		t.Fatalf("cloneHubInt64(&42) = %v, want &42", clone)
	}
	*clone = 99
	if v != 42 {
		t.Fatalf("original was mutated: v = %d, want 42", v)
	}
}

// TestCloneHubInt64Nil covers the nil path.
func TestCloneHubInt64Nil(t *testing.T) {
	if clone := cloneHubInt64(nil); clone != nil {
		t.Fatalf("cloneHubInt64(nil) = %v, want nil", clone)
	}
}

// TestOutputImageDescriptorKeySHA covers the SHA path.
func TestOutputImageDescriptorKeySHA(t *testing.T) {
	img := appwire.OutputImage{SHA: "abc123"}
	if got := outputImageDescriptorKey(img); got != "sha:abc123" {
		t.Fatalf("key = %q, want sha:abc123", got)
	}
}

// TestOutputImageDescriptorKeyURL covers the URL path.
func TestOutputImageDescriptorKeyURL(t *testing.T) {
	img := appwire.OutputImage{URL: "https://example.com/img.png"}
	if got := outputImageDescriptorKey(img); got != "https://example.com/img.png" {
		t.Fatalf("key = %q, want URL", got)
	}
}

// TestOutputImageDescriptorKeyPath covers the Path path.
func TestOutputImageDescriptorKeyPath(t *testing.T) {
	img := appwire.OutputImage{Path: "/tmp/img.png"}
	if got := outputImageDescriptorKey(img); got != "path:/tmp/img.png" {
		t.Fatalf("key = %q, want path:/tmp/img.png", got)
	}
}

// TestOutputImageDescriptorKeyEmpty covers the empty path.
func TestOutputImageDescriptorKeyEmpty(t *testing.T) {
	img := appwire.OutputImage{}
	if got := outputImageDescriptorKey(img); got != "" {
		t.Fatalf("key = %q, want empty", got)
	}
}

// TestOutputImageDescriptorKeyPriority covers that SHA takes priority over URL.
func TestOutputImageDescriptorKeyPriority(t *testing.T) {
	img := appwire.OutputImage{SHA: "sha", URL: "url", Path: "path"}
	if got := outputImageDescriptorKey(img); got != "sha:sha" {
		t.Fatalf("key = %q, want sha:sha (SHA has priority)", got)
	}
}

// TestAppwireDelegateFromAgentStatus covers the full mapping function.
func TestAppwireDelegateFromAgentStatus(t *testing.T) {
	delegate := agent.DelegateStatusInfo{
		DelegateID:     "dlg-1",
		RunGeneration:  2,
		OwnerSessionID: "sess-1",
		Status:         "running",
		Terminal:       false,
		Resumable:      true,
	}
	info := appwireDelegateFromAgentStatus(delegate)
	if info.RunGeneration != 2 {
		t.Fatalf("RunGeneration = %d, want 2", info.RunGeneration)
	}
	if info.DelegateID != "dlg-1" {
		t.Fatalf("DelegateID = %q, want dlg-1", info.DelegateID)
	}
	if info.OwnerSessionID != "sess-1" {
		t.Fatalf("OwnerSessionID = %q, want sess-1", info.OwnerSessionID)
	}
	if info.Status != "running" {
		t.Fatalf("Status = %q, want running", info.Status)
	}
	if info.Terminal != false {
		t.Fatal("Terminal = true, want false")
	}
	if info.Resumable != true {
		t.Fatal("Resumable = false, want true")
	}
}

// TestAppwireDelegateFromAgentStatusWithUsage covers the Usage non-nil path.
func TestAppwireDelegateFromAgentStatusWithUsage(t *testing.T) {
	usage := appwire.EvenerUsage{InputTokens: 100}
	delegate := agent.DelegateStatusInfo{
		DelegateID: "dlg-2",
		Usage:      &usage,
	}
	info := appwireDelegateFromAgentStatus(delegate)
	if info.Usage == nil || info.Usage.InputTokens != 100 {
		t.Fatalf("Usage = %+v, want InputTokens=100", info.Usage)
	}
}

// TestAppwireDelegateFromAgentStatusWithWorktree covers the Worktree non-nil path.
func TestAppwireDelegateFromAgentStatusWithWorktree(t *testing.T) {
	worktree := appwire.JobActivityWorktree{Branch: "test"}
	delegate := agent.DelegateStatusInfo{
		DelegateID: "dlg-3",
		Worktree:   &worktree,
	}
	info := appwireDelegateFromAgentStatus(delegate)
	if info.Worktree == nil || info.Worktree.Branch != "test" {
		t.Fatalf("Worktree = %+v, want Branch=test", info.Worktree)
	}
}
