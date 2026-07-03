package agent

import (
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
)

// The pure restore-descriptor accessors reject a nil descriptor and collapse the
// "*" wildcard tool set.
func TestW2Dlg_RestoreDescriptorAccessors_Guards(t *testing.T) {
	t.Parallel()

	if _, ok := delegateRestoreLocalEnvPolicy(nil); ok {
		t.Fatal("delegateRestoreLocalEnvPolicy(nil): want !ok")
	}
	if _, ok := delegateRestoreWorkingDir(nil); ok {
		t.Fatal("delegateRestoreWorkingDir(nil): want !ok")
	}
	if got := restoredDelegateAllowedTools(nil); got != nil {
		t.Fatalf("restoredDelegateAllowedTools(nil) = %v, want nil", got)
	}
	wildcard := &jobstore.DelegateRestoreDescriptor{FrozenToolNames: []string{"*"}}
	if got := restoredDelegateAllowedTools(wildcard); got != nil {
		t.Fatalf("restoredDelegateAllowedTools(*) = %v, want nil", got)
	}
	if got := restoredDelegateRequiredTools(nil); got != nil {
		t.Fatalf("restoredDelegateRequiredTools(nil) = %v, want nil", got)
	}
}

// compactToolNames drops blanks, the wildcard, and duplicates while preserving
// first-seen order.
func TestW2Dlg_CompactToolNames_DropsBlanksWildcardDupes(t *testing.T) {
	t.Parallel()
	got := compactToolNames([]string{"", "*", "read_file", "read_file", " ", "edit_file"})
	if len(got) != 2 || got[0] != "read_file" || got[1] != "edit_file" {
		t.Fatalf("compactToolNames = %v, want [read_file edit_file]", got)
	}
}

// validateRestoredDelegateTools and the session variant both report an
// unavailable registry when required tools cannot be checked.
func TestW2Dlg_ValidateRestoredDelegateTools_NoRegistry(t *testing.T) {
	t.Parallel()
	desc := &jobstore.DelegateRestoreDescriptor{FrozenToolNames: []string{"read_file"}}

	if err := validateRestoredDelegateTools(nil, desc); err == nil {
		t.Fatal("validateRestoredDelegateTools(nil child): want error")
	}
	if err := (*Session)(nil).validateRestoredDelegateRequiredTools(desc); err == nil {
		t.Fatal("validateRestoredDelegateRequiredTools(nil session): want error")
	}
}

// resolveDelegateRestoreProfile fails on a nil session and on descriptors that
// omit the resolved profile/model, and surfaces an unavailable-profile error for
// an unknown profile ref.
func TestW2Dlg_ResolveDelegateRestoreProfile_Errors(t *testing.T) {
	t.Parallel()

	if _, err := (*Session)(nil).resolveDelegateRestoreProfile(schema.SessionMeta{}, nil); err == nil {
		t.Fatal("nil session: want error")
	}

	s := w2dlg_session(t)
	if _, err := s.resolveDelegateRestoreProfile(schema.SessionMeta{}, nil); err == nil {
		t.Fatal("nil descriptor: want error")
	}
	noModel := &jobstore.DelegateRestoreDescriptor{ResolvedProfileID: "openai"}
	if _, err := s.resolveDelegateRestoreProfile(schema.SessionMeta{}, noModel); err == nil {
		t.Fatal("descriptor missing resolved model: want error")
	}
	unknown := &jobstore.DelegateRestoreDescriptor{ResolvedProfileID: "ghostprofile", ResolvedModel: "gpt-5.2"}
	if _, err := s.resolveDelegateRestoreProfile(schema.SessionMeta{}, unknown); err == nil {
		t.Fatal("unknown profile ref: want error")
	}
}

// restoreDelegateChildEnvironment rejects a nil session, an invalid env policy,
// and an empty working directory.
func TestW2Dlg_RestoreDelegateChildEnvironment_Errors(t *testing.T) {
	t.Parallel()

	if _, err := (*Session)(nil).restoreDelegateChildEnvironment(&jobstore.DelegateRestoreDescriptor{}, ""); err == nil {
		t.Fatal("nil session: want error")
	}

	s := w2dlg_session(t)
	badPolicy := &jobstore.DelegateRestoreDescriptor{WorkingDir: "/tmp", LocalEnvPolicy: "bogus"}
	if _, err := s.restoreDelegateChildEnvironment(badPolicy, ""); err == nil {
		t.Fatal("invalid policy: want error")
	}
	badWorkDir := &jobstore.DelegateRestoreDescriptor{WorkingDir: "", LocalEnvPolicy: "default"}
	if _, err := s.restoreDelegateChildEnvironment(badWorkDir, ""); err == nil {
		t.Fatal("empty working_dir: want error")
	}
}

// restoreTerminalDelegateChild rejects each malformed precondition before it
// begins reconstructing the child session.
func TestW2Dlg_RestoreTerminalDelegateChild_Preconditions(t *testing.T) {
	t.Parallel()
	s := w2dlg_session(t)
	resumable := true

	if _, err := (*Session)(nil).restoreTerminalDelegateChild(&jobstore.JobRecord{}, "child", nil); err == nil {
		t.Fatal("nil session: want error")
	}
	if _, err := s.restoreTerminalDelegateChild(&jobstore.JobRecord{}, "child", nil); err == nil {
		t.Fatal("nil preflight: want error")
	}

	preflight := &delegateRestorePreflight{}
	notResumable := &jobstore.JobRecord{Type: jobstore.JobDelegate, Status: jobstore.StatusCompleted}
	if _, err := s.restoreTerminalDelegateChild(notResumable, "child", preflight); err == nil {
		t.Fatal("not resumable: want error")
	}
	noRestore := &jobstore.JobRecord{Type: jobstore.JobDelegate, Status: jobstore.StatusCompleted, Resumable: &resumable}
	if _, err := s.restoreTerminalDelegateChild(noRestore, "child", preflight); err == nil {
		t.Fatal("missing restore descriptor: want error")
	}
	mismatch := &jobstore.JobRecord{
		Type:            jobstore.JobDelegate,
		Status:          jobstore.StatusCompleted,
		Resumable:       &resumable,
		DelegateRestore: &jobstore.DelegateRestoreDescriptor{ChildSessionID: "other"},
	}
	if _, err := s.restoreTerminalDelegateChild(mismatch, "child", preflight); err == nil {
		t.Fatal("child mismatch: want error")
	}
}
