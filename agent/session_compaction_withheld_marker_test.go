package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// foldTailFaultFS fails the next Sync on the transcript file once armed. The
// fold's compaction markers are buffered writes that carry no fsync of their
// own, so with a young SyncInterval the first fsync after arming is the one a
// staged retained-tail copy makes: its durable write fails, the writer rolls
// the line back out, and the copy records nothing — the transcript I/O failure
// shape of issue #1158.
type foldTailFaultFS struct {
	afero.Fs
	mu      sync.Mutex
	failure error
}

type foldTailFaultFile struct {
	afero.File
	fs *foldTailFaultFS
}

func (fs *foldTailFaultFS) OpenFile(name string, flag int, mode os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, mode)
	if err != nil {
		return nil, err
	}
	return &foldTailFaultFile{File: file, fs: fs}, nil
}

func (fs *foldTailFaultFS) arm(err error) {
	fs.mu.Lock()
	fs.failure = err
	fs.mu.Unlock()
}

func (file *foldTailFaultFile) Sync() error {
	file.fs.mu.Lock()
	failure := file.fs.failure
	file.fs.failure = nil
	file.fs.mu.Unlock()
	if failure != nil {
		return failure
	}
	return file.File.Sync()
}

// TestCompactionWithholdsMarkerWhenRetainedTailWriteFails pins the durability
// contract of issue #1158: a fold whose staged retained-tail copy cannot be
// written durably must not publish its compaction marker. The marker is what a
// resume anchors on to discard every entry before it, so a marker whose
// replacement copies never landed silently loses the turns it folded away.
// Here the original entries must stay readable through a resume, with no
// duplicate copies of the retained turns left behind, while the in-memory fold
// still stands (the live history is compacted and Compact succeeds).
func TestCompactionWithholdsMarkerWhenRetainedTailWriteFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "withhold-marker-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nopaque-withhold-summary\n[END SUMMARY]")}
	}, withStateDir(dir))
	if err := s.Rename("Withheld fold fixture"); err != nil {
		t.Fatal(err)
	}
	const inputs = 20
	for i := range inputs {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("opaque-withhold-input-%d", i)))
		s.recordTurn(turn, turn)
	}

	// Swap in a writer over a faultable filesystem, mirroring the production
	// interval so the buffered marker records take no opportunistic fsync: the
	// armed fault then lands on the staged copy write, not on the markers.
	if err := s.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	fs := &foldTailFaultFS{Fs: afero.NewOsFs()}
	writer, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	writer.SyncInterval = time.Hour
	s.attachTranscript(writer, nil)
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldTranscriptCommit = func() {
			fs.arm(errors.New("opaque staged tail sync failure"))
		}
	})

	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	entries := readPersistedTranscriptEntries(t, dir, s.ID())
	markers := 0
	for _, entry := range entries {
		if entry.Turn.Kind == schema.TurnCheckpoint || entry.Turn.Kind == schema.TurnSummary {
			markers++
		}
	}
	if markers != 0 {
		t.Fatalf("fold published %d compaction marker(s) whose staged retained-tail copy write failed; the marker a resume anchors on must be withheld", markers)
	}

	// The original transcript must survive a resume: every original input is
	// readable exactly once — neither folded away by a half-applied marker nor
	// duplicated by a copy the failed fold left behind.
	resumed := ResumeHistory(entries)
	for i := range inputs {
		want := fmt.Sprintf("opaque-withhold-input-%d", i)
		n := 0
		for _, turn := range resumed {
			if turn.Message.Text() == want {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("resumed history holds input %d %d times, want exactly 1: the original must stay readable and un-duplicated", i, n)
		}
	}

	// The fold still published in memory: the live history is compacted and
	// the folded-away turns are gone from it, while the readable transcript
	// keeps them for the next resume.
	live := currentHistory(t, s)
	if at := indexOfTurnText(live, "opaque-withhold-input-0"); at >= 0 {
		t.Fatalf("live history was not compacted: folded-away input still at %d", at)
	}
	if at := indexOfTurnText(live, "opaque-withhold-input-19"); at < 0 {
		t.Fatal("live history lost its retained tail: the in-memory fold must stand")
	}
}
