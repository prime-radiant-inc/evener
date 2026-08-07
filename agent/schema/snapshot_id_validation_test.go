package schema

import (
	"errors"
	"os"
	"testing"

	"github.com/spf13/afero"
)

// unsafeSessionIDs are the shapes the persistence boundary must refuse. Each
// either escapes <dir>/sessions when joined into a path, collides with another
// session's file, or folds onto another ID under the path- and case-
// canonicalization the write lock keys on.
var unsafeSessionIDs = map[string]string{
	"empty":                  "",
	"dot":                    ".",
	"traversal":              "..",
	"traversal prefix":       "../escaped",
	"traversal segment":      "sessions/../../escaped",
	"forward separator":      "nested/id",
	"backward separator":     "nested\\id",
	"leading separator":      "/absolute",
	"dot in name":            "01TEST.0001",
	"space":                  "01TEST 0001",
	"tab":                    "01TEST\t0001",
	"newline":                "01TEST\n0001",
	"nul":                    "01TEST\x000001",
	"colon":                  "local:01TEST0001",
	"non-ascii sigma":        "01TESTσ0001",
	"non-ascii final sigma":  "01TESTς0001",
	"non-ascii accented":     "01TESTé0001",
	"non-ascii fullwidth":    "01ＴＥＳＴ0001",
	"unicode line separator": "01TEST 0001",
	"glob star":              "01TEST*",
	"tilde path":             "~/01TEST0001",
	"percent encoded slash":  "01TEST%2F0001",
	"over length":            "0123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789",
}

// safeSessionIDs must keep working: real minted IDs plus the terse fixture IDs
// the existing suites persist. Rejecting these would be a behavior break, not a
// hardening.
var safeSessionIDs = []string{
	"02wMz5TxvEMoJEDTDGOTil",
	"01TEST0001",
	"01SESSIONXXXXXXXXXXXXXXXXXX",
	"WORKER",
	"missing",
	"session",
	"child",
	"parent",
	"sess_ended",
	"sess1",
	"th_1",
	"x",
	"-01TEST0001",
	"01TEST0001-",
	"a_b-C9",
}

func TestSaveSessionMetaRejectsUnsafeSessionIDs(t *testing.T) {
	t.Parallel()
	for name, id := range unsafeSessionIDs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fs := afero.NewMemMapFs()
			err := SaveSessionMetaWithFS(fs, "/state", SessionMeta{ID: id})
			if !errors.Is(err, ErrInvalidSessionID) {
				t.Fatalf("SaveSessionMetaWithFS(%q) error = %v, want ErrInvalidSessionID", id, err)
			}
			assertNoFilesWritten(t, fs)
		})
	}
}

func TestAppendSessionObservedByRejectsUnsafeSessionIDs(t *testing.T) {
	t.Parallel()
	for name, id := range unsafeSessionIDs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fs := afero.NewMemMapFs()
			err := appendSessionObservedByWithFS(fs, "/state", id, "02wMz5TxvCu3kdckfnw0Gh")
			if !errors.Is(err, ErrInvalidSessionID) {
				t.Fatalf("appendSessionObservedByWithFS(%q) error = %v, want ErrInvalidSessionID", id, err)
			}
			assertNoFilesWritten(t, fs)
		})
	}
}

func TestLoadSessionMetaRejectsUnsafeSessionIDs(t *testing.T) {
	t.Parallel()
	for name, id := range unsafeSessionIDs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fs := afero.NewMemMapFs()
			if _, err := LoadSessionMetaWithFS(fs, "/state", id); !errors.Is(err, ErrInvalidSessionID) {
				t.Fatalf("LoadSessionMetaWithFS(%q) error = %v, want ErrInvalidSessionID", id, err)
			}
		})
	}
}

// TestSaveSessionMetaRefusesToEscapeSessionsDir states the hole this closes in
// its own terms: a traversal ID must not write outside <dir>/sessions.
func TestSaveSessionMetaRefusesToEscapeSessionsDir(t *testing.T) {
	t.Parallel()
	const victim = "/state/victim.meta.json"
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, victim, []byte(`{"id":"victim"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := SaveSessionMetaWithFS(fs, "/state", SessionMeta{ID: "../victim", Name: "clobbered"})
	if !errors.Is(err, ErrInvalidSessionID) {
		t.Fatalf("escaping save error = %v, want ErrInvalidSessionID", err)
	}
	data, readErr := afero.ReadFile(fs, victim)
	if readErr != nil {
		t.Fatalf("the file outside the sessions dir went missing: %v", readErr)
	}
	if string(data) != `{"id":"victim"}` {
		t.Fatalf("a session-meta write reached outside the sessions dir: %s", data)
	}
}

func TestSessionMetaPersistenceAcceptsSafeSessionIDs(t *testing.T) {
	t.Parallel()
	for _, id := range safeSessionIDs {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			fs := afero.NewMemMapFs()
			if err := SaveSessionMetaWithFS(fs, "/state", SessionMeta{ID: id, Name: "kept"}); err != nil {
				t.Fatalf("SaveSessionMetaWithFS(%q): %v", id, err)
			}
			got, err := LoadSessionMetaWithFS(fs, "/state", id)
			if err != nil {
				t.Fatalf("LoadSessionMetaWithFS(%q): %v", id, err)
			}
			if got.Name != "kept" {
				t.Fatalf("meta for %q = %+v", id, got)
			}
			if err := appendSessionObservedByWithFS(fs, "/state", id, "02wMz5TxvCu3kdckfnw0Gh"); err != nil {
				t.Fatalf("appendSessionObservedByWithFS(%q): %v", id, err)
			}
		})
	}
}

// TestLoadSessionMetaStillReportsMissingAsNotExist pins the error shape callers
// already branch on: a well-formed ID with no file on disk stays a not-exist
// error rather than becoming a validation error.
func TestLoadSessionMetaStillReportsMissingAsNotExist(t *testing.T) {
	t.Parallel()
	fs := afero.NewMemMapFs()
	_, err := LoadSessionMetaWithFS(fs, "/state", "02wMz5TxvEMoJEDTDGOTil")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing meta error = %v, want os.ErrNotExist", err)
	}
	if errors.Is(err, ErrInvalidSessionID) {
		t.Fatalf("a well-formed but absent ID reported as invalid: %v", err)
	}
}

func assertNoFilesWritten(t *testing.T, fs afero.Fs) {
	t.Helper()
	walkErr := afero.Walk(fs, "/", func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() {
			t.Errorf("rejected session ID still wrote %s", path)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}
}
