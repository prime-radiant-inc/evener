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
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// foldTailFaultFS counts Sync calls on the transcript file once armed and
// fails exactly the Nth, one-shot. The fold's compaction markers are buffered
// writes that carry no fsync of their own, so with a young SyncInterval the
// syncs after arming are the staged retained-tail copies' durable writes:
// arming on the second makes the first copy land durably and the second fail
// and roll back — the transcript I/O failure shape of issue #1158, with lines
// on both sides of the failure for the withhold to take back out.
type foldTailFaultFS struct {
	afero.Fs
	mu         sync.Mutex
	failOnNth  int // 0 disarms
	syncsSince int
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

// arm fails exactly the nth Sync call made after arming.
func (fs *foldTailFaultFS) arm(nth int) {
	fs.mu.Lock()
	fs.syncsSince = 0
	fs.failOnNth = nth
	fs.mu.Unlock()
}

// syncsSinceArm reports how many Sync calls were made since the last arming.
func (fs *foldTailFaultFS) syncsSinceArm() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.syncsSince
}

func (file *foldTailFaultFile) Sync() error {
	file.fs.mu.Lock()
	file.fs.syncsSince++
	nth := file.fs.failOnNth
	if nth > 0 && file.fs.syncsSince == nth {
		file.fs.failOnNth = 0
		file.fs.mu.Unlock()
		return errors.New("opaque staged tail sync failure")
	}
	file.fs.mu.Unlock()
	return file.File.Sync()
}

// TestCompactionWithholdsMarkerWhenRetainedTailWriteFails pins the durability
// contract of issue #1158: a fold whose staged retained-tail copies cannot
// all be written durably must not publish its compaction marker. The marker
// is what a resume anchors on to discard every entry before it, so a marker
// whose replacement copies never landed silently loses the turns it folded
// away. Here the first staged copy lands durably and the second fails, so the
// transaction's lines span both sides of the failure: the original entries
// must stay readable through a resume with the landed copy taken back out
// (each original exactly once), the fold's records must never reach a
// recorded-entry or published-length consumer, and the in-memory fold still
// stands (the live history is compacted and Compact succeeds).
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
	// armed fault then lands on the staged copy writes, not on the markers.
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
	boundaryLength := s.TranscriptRecordedLength()

	// The wired consumers a served session projects through: the recorded
	// entry hook and the published-length handoff. A withheld fold must not
	// deliver a single record or a length past the pre-fold boundary to
	// either.
	var recordedMu sync.Mutex
	var recorded []transcript.Record
	publishedLengths := make(chan int64, 16)
	s.SetTranscriptRecordedFunc(func(rec transcript.Record) {
		recordedMu.Lock()
		recorded = append(recorded, rec)
		recordedMu.Unlock()
	})
	s.SetTranscriptPublishedFunc(func(length int64, _ *appwire.ConversationProvenanceState) {
		select {
		case publishedLengths <- length:
		default:
		}
	})
	updateSessionTestConfig(s, func(cfg *testConfig) {
		cfg.beforeFoldTranscriptCommit = func() {
			fs.arm(2) // the first staged copy lands, the second fails
		}
	})

	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// The scenario really ran: the first staged copy's durable write synced
	// (and landed) before the armed fault hit the second. Fewer than two
	// syncs means the fault hit some other write and the assertions below
	// would prove nothing.
	if n := fs.syncsSinceArm(); n < 2 {
		t.Fatalf("only %d sync(s) since arming, want the first staged copy to land before the second failed: the fault hit the wrong write", n)
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
			t.Fatalf("resumed history holds input %d %d times, want exactly 1: the original must stay readable and the landed copy must be taken back out", i, n)
		}
	}

	// Nothing the transaction wrote reached a consumer: no recorded entry was
	// delivered, and the settled published length is the pre-fold boundary.
	recordedMu.Lock()
	delivered := recorded
	recordedMu.Unlock()
	if len(delivered) != 0 {
		t.Fatalf("the withheld fold delivered %d recorded entr(ies) to the consumer hook, want none: the first is %+v", len(delivered), delivered[0])
	}
	select {
	case length := <-publishedLengths:
		if length != boundaryLength {
			t.Fatalf("published length %d after the withheld fold, want the pre-fold boundary %d", length, boundaryLength)
		}
	default:
		t.Fatal("no published length reached the consumer after the fold settled")
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
