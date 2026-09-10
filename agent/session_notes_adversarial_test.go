package agent

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

func TestBarePercentFilenamePreserved(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "report%23final.md")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	want := (&url.URL{Scheme: "file", Path: path}).String()
	control, err := canonicalSessionURL(want, root)
	if err != nil || control != want {
		t.Fatalf("file URL control: %q, %v", control, err)
	}
	got, err := canonicalSessionURL("report%23final.md", root)
	if err != nil || got != want {
		t.Fatalf("bare path: %q, %v; want %q", got, err, want)
	}
}

func TestCanonicalURLLiteralPercentControls(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"report%2Ffinal.md", "report%25final.md"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			want := (&url.URL{Scheme: "file", Path: path}).String()
			for _, input := range []string{name, path, want} {
				got, err := canonicalSessionURL(input, root)
				if err != nil || got != want {
					t.Fatalf("input %q: got %q, %v; want %q", input, got, err, want)
				}
			}
		})
	}
}

func TestCanonicalURLEncodedTraversalControls(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "scope")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	base := (&url.URL{Scheme: "file", Path: root}).String()
	for _, suffix := range []string{"/%2e%2e/outside.md", "/..%2Foutside.md", "/%2e%2e%2foutside.md"} {
		input := base + suffix
		parsed, err := url.Parse(input)
		if err != nil || filepath.Clean(parsed.Path) != outside {
			t.Fatalf("invalid traversal control %q: %v, %v", input, parsed, err)
		}
		if got, err := canonicalSessionURL(input, root); err == nil || !strings.Contains(err.Error(), "outside the session scope") {
			t.Fatalf("encoded traversal %q: got %q, %v", input, got, err)
		}
	}
}

// notesRenameFailureFS fails only the mutation's metadata rename. All reads,
// temp writes, the concurrent autosave rename, and reload use the real disk.
type notesRenameFailureFS struct {
	afero.Fs
	fail bool
	err  error
}

func (fs *notesRenameFailureFS) Rename(old, newPath string) error {
	if fs.fail {
		return fs.err
	}
	return fs.Fs.Rename(old, newPath)
}

func TestNotesMetadataFailureCannotEscapeAutosave(t *testing.T) {
	for _, operation := range []string{"agent-note", "url-add", "url-remove", "url-rpc-remove"} {
		t.Run(operation, func(t *testing.T) {
			s := newNotesToolSession(t)
			s.stateDir = t.TempDir()
			s.setAgentNote("committed note")
			entry, err := s.addSessionURL("https://example.com/committed", "committed label")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.ensureClientMutationStore(); err != nil {
				t.Fatal(err)
			}
			if err := s.autoSaveMeta(); err != nil {
				t.Fatal(err)
			}
			before, err := schema.LoadSessionMeta(s.stateDir, s.id)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("metadata rename rejected")
			fs := &notesRenameFailureFS{Fs: afero.NewOsFs(), err: failure}
			s.cfg.testOnly.metaFS = fs

			var mutate func() error
			var frame string
			switch operation {
			case "agent-note":
				frame = "mutateAgentNoteSerialized"
				mutate = func() error {
					_, _, _, _, err := s.mutateAgentNoteSerialized("rejected note")
					return err
				}
			case "url-add":
				frame = "mutateSessionURLAddSerialized"
				mutate = func() error {
					_, _, err := s.mutateSessionURLAddSerialized("https://example.com/rejected", "rejected label")
					return err
				}
			case "url-remove":
				frame = "mutateSessionURLRemoveSerialized"
				mutate = func() error {
					_, _, err := s.mutateSessionURLRemoveSerialized(entry.ID)
					return err
				}
			case "url-rpc-remove":
				frame = "RemoveSessionURL"
				mutate = func() error {
					_, err := s.RemoveSessionURL("rejected-removal", entry.ID)
					return err
				}
			}

			// Own the real autosave serializer BEFORE its snapshot, then park
			// the mutation on that mutex. Waiting for tentative state instead
			// would deadlock the corrected code, which mutates only AFTER locking.
			s.metaSaveMu.Lock()
			unlock := releaseOnce(s.metaSaveMu.Unlock)
			defer unlock()
			mutationDone := make(chan error, 1)
			go func() { mutationDone <- mutate() }()
			deadline := time.Now().Add(5 * time.Second) // tripwire, not scheduling
			for {
				parked := false
				for g := range strings.SplitSeq(goroutineDump(), "\n\ngoroutine ") {
					if !strings.Contains(g, "created by primeradiant.com/evener/agent.TestNotesMetadataFailureCannotEscapeAutosave.") {
						continue
					}
					lines := strings.Split(g, "\n")
					for i, line := range lines {
						// The direct caller distinguishes metaSaveMu from a
						// nested s.mu wait while reading/mutating the store.
						if strings.HasPrefix(line, "sync.(*Mutex).Lock(") && i+2 < len(lines) &&
							strings.HasPrefix(lines[i+2], "primeradiant.com/evener/agent.(*Session)."+frame+"(") {
							parked = true
							break
						}
					}
					if parked {
						break
					}
				}
				if parked {
					break
				}
				if time.Now().After(deadline) {
					unlock()
					<-mutationDone
					t.Fatalf("mutation did not park on autosave serializer:\n%s", goroutineDump())
				}
				runtime.Gosched()
			}
			// This is the real autosave snapshot+write path with its required
			// lock held. Old code exposes tentative state here, before its own
			// save can fail. Corrected code still exposes committed state.
			autosaveErr := s.autoSaveMetaLocked()
			fs.fail = true
			unlock()
			mutationErr := <-mutationDone
			fs.fail = false
			if autosaveErr != nil {
				t.Fatalf("concurrent autosave: %v", autosaveErr)
			}
			if !errors.Is(mutationErr, failure) {
				t.Fatalf("mutation error = %v, want Rename failure", mutationErr)
			}
			_, liveNote, liveURLs := s.notesSnapshotAll()
			if liveNote != before.AgentNote || !reflect.DeepEqual(liveURLs, before.SessionURLs) {
				t.Fatalf("live rollback: note %q, URLs %+v; want note %q, URLs %+v", liveNote, liveURLs, before.AgentNote, before.SessionURLs)
			}
			for {
				select {
				case ev := <-s.Events():
					if ev.Kind == events.EventNotesUpdated || ev.Kind == events.EventUrlsUpdated {
						t.Errorf("rejected mutation emitted success: %+v", ev)
					}
				default:
					goto drained
				}
			}
		drained:
			reloaded, err := schema.LoadSessionMeta(s.stateDir, s.id)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.AgentNote != before.AgentNote || !reflect.DeepEqual(reloaded.SessionURLs, before.SessionURLs) {
				t.Fatalf("rejected mutation escaped autosave: disk note %q, URLs %+v; rolled-back note %q, URLs %+v", reloaded.AgentNote, reloaded.SessionURLs, liveNote, liveURLs)
			}
		})
	}
}
