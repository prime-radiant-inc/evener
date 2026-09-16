package hubcore

import (
	"errors"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/appwire"
)

// A patch whose rename published the new snapshot and whose follow-up failed
// has applied: the file carries the new revision and the store adopted it. The
// caller must be able to tell that from a patch that changed nothing, because
// the RPC layer broadcasts the canonical state for an applied write and every
// other client stays on the pre-patch revision otherwise — the same rule
// KeybindingsPostRenameError already carries for the keybindings store.
func TestTranscriptDisplayPatchAfterRenameReportsTheWriteApplied(t *testing.T) {
	store, err := newTranscriptDisplayStoreFS(afero.NewMemMapFs(), "/state", transcriptDisplayStoreFaults{
		AfterRename: func() error { return errors.New("post-rename failure") },
	})
	if err != nil {
		t.Fatalf("newTranscriptDisplayStoreFS: %v", err)
	}

	_, patchErr := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{
		Layout:           appwire.TranscriptViewportDesktop,
		ExpectedRevision: 0,
		Config:           validTranscriptDisplayPatchConfig(),
	})
	if patchErr == nil {
		t.Fatal("Patch = nil, want the post-rename failure reported")
	}
	if !errors.Is(patchErr, ErrWriteApplied) {
		t.Fatalf("Patch = %v (%T), want it to report the write applied", patchErr, patchErr)
	}
}

// The keybindings store's own post-rename error answers the same question, so
// one predicate covers both stores instead of each caller knowing a type.
func TestKeybindingsPostRenameErrorReportsTheWriteApplied(t *testing.T) {
	err := error(&KeybindingsPostRenameError{Err: errors.New("post-rename failure")})
	if !errors.Is(err, ErrWriteApplied) {
		t.Fatalf("KeybindingsPostRenameError = %v, want it to report the write applied", err)
	}
}

// A refused patch changed nothing and must not read as applied.
func TestTranscriptDisplayConflictDoesNotReportTheWriteApplied(t *testing.T) {
	store, err := newTranscriptDisplayStoreFS(afero.NewMemMapFs(), "/state", transcriptDisplayStoreFaults{})
	if err != nil {
		t.Fatalf("newTranscriptDisplayStoreFS: %v", err)
	}

	_, patchErr := store.Patch(appwire.TranscriptDisplayDefaultsPatchParams{
		Layout:           appwire.TranscriptViewportDesktop,
		ExpectedRevision: 7,
		Config:           validTranscriptDisplayPatchConfig(),
	})
	if patchErr == nil {
		t.Fatal("Patch(stale revision) = nil, want a conflict")
	}
	if errors.Is(patchErr, ErrWriteApplied) {
		t.Fatalf("Patch(stale revision) = %v, want a refusal that changed nothing", patchErr)
	}
}

// validTranscriptDisplayPatchConfig is a config the store accepts, so these
// tests reach the write rather than the validation in front of it.
func validTranscriptDisplayPatchConfig() appwire.TranscriptDisplayConfig {
	config := appwire.TranscriptDisplayShippedDefaults().Desktop.Config
	config.Content.Level = appwire.TranscriptLevelActivity
	return config
}
