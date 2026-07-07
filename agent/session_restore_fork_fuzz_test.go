//go:build serffuzz

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// Fuzz targets for session reconstruction from persisted state — the deserialize
// seam where a fuzzed meta.json / transcript JSONL is turned back into a live
// Session (RestoreSessionFromMetaWithConfig) or branched into a child
// (ForkSession). Persisted-state decode is prime bug territory: a malformed meta,
// a corrupt transcript, or an out-of-range fork index must never panic and must
// leave a self-consistent result (a valid child id / restored session) or a clean
// error — never a half-built artifact.
//
// Both targets reuse the package's seqReader (lifecycle_covfuzz_test.go) as a
// stable byte cursor so the persisted corpus keeps its meaning across edits.
// State lands in a real t.TempDir sandbox (both functions read/write the OS
// filesystem directly via os.* and schema.SaveSessionMeta), never real disk
// outside the sandbox, and model calls go through a scripted adapter — no
// network, no subprocess.
//
// Anti-collision lane token: rfz_.

// rfzTexts is a small, adversarial pool of message/label/name payloads: empty,
// whitespace, embedded newline, raw non-UTF8 bytes, and multibyte runes.
var rfzTexts = []string{"", "hello", "edited message", "  ", "line1\nline2", "\x00\xff", "很长"}

// rfzStrategies spans every context strategy selectStrategy accepts plus one
// bogus value, so the fuzzer reaches both the strategy-install success paths and
// the "unknown context strategy" clean-error return.
var rfzStrategies = []string{
	"", "compact", "recall", "session-log", "ooda",
	"obs-mask", "checkpoint-pred", "memory-crystals", "recursive-distill",
	"rfz-bogus-strategy",
}

// rfzGoalStatuses covers the one status that triggers goal restore ("active")
// plus terminal/empty statuses that must be dropped without error.
var rfzGoalStatuses = []string{"active", "complete", "blocked", ""}

var rfzTurnKinds = []schema.TurnKind{schema.TurnUserInput, schema.TurnAssistant}

// FuzzRfzForkSession drives ForkSession against a fuzzer-built parent transcript
// (structured turns or raw corrupt bytes) with a fuzzed divergence index, edited
// message, and fork label. Beyond never panicking it asserts the fork contract:
//
//   - error  => empty child id (no leaked id on the failure path);
//   - success => the child id is a valid ULID; the child meta is persisted and
//     points back at the parent with the requested divergence and no inherited
//     label; the child transcript is readable, ends in a USER_INPUT turn (the
//     edited message), and holds exactly divergenceTurn entries (prefix of
//     divergenceTurn-1 plus the edited turn); a non-empty label lands on the
//     PARENT meta, never the child.
func FuzzRfzForkSession(f *testing.F) {
	seeds := [][]byte{
		{0, 3, 0, 1, 0, 3, 2},
		{1, 4, 0, 0, 0, 0, 5, 1},
		{2, 5, 1, 0, 1, 0, 1, 2, 6, 0},
		{3, 2, 9, 9, 9, 9},
		// Crafted success: mode 0, 2 user turns, divergence 1 (hits entry[0]),
		// label off — reaches the child-write + child-meta consistency oracle.
		{0, 2, 0, 0, 1, 1, 2, 1, 1},
		// Crafted labeled success: mode 0, 4 turns (U,A,U,A), divergence 3 (hits
		// entry[2]=U), label on — reaches the parent-meta label-update branch.
		{0, 4, 0, 1, 0, 1, 0, 0, 0, 0, 4, 0, 0, 2},
		{},
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &seqReader{data: data}
		stateDir := t.TempDir()
		parentID := ulid.Make().String()

		// A valid parent meta must exist: ForkSession loads it to copy fields.
		if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
			ID:        parentID,
			ProfileID: "openai",
			Model:     "gpt-5",
		}); err != nil {
			t.Fatalf("save parent meta: %v", err)
		}

		mode := r.intn(4)
		numEntries := r.intn(8) // 0..7 parent turns
		kinds := make([]schema.TurnKind, numEntries)
		for i := range kinds {
			kinds[i] = rfzTurnKinds[r.intn(len(rfzTurnKinds))]
		}
		parentPath := filepath.Join(stateDir, sessionsSubdir, parentID+".transcript.jsonl")

		if mode == 3 {
			// Corrupt: raw fuzz bytes stand in for the parent transcript, exercising
			// the header-parse-error / empty / skip-corrupt-line branches.
			if err := os.MkdirAll(filepath.Dir(parentPath), 0o755); err != nil {
				t.Fatalf("mkdir sessions: %v", err)
			}
			if err := os.WriteFile(parentPath, data, 0o644); err != nil {
				t.Fatalf("write corrupt transcript: %v", err)
			}
		} else {
			w, err := transcript.NewWriter(parentPath, transcript.Header{
				SessionID: parentID,
				ProfileID: "openai",
				Model:     "gpt-5",
			})
			if err != nil {
				t.Fatalf("new parent writer: %v", err)
			}
			for _, k := range kinds {
				if err := w.Append(schema.NewTurn(k, llm.User(rfzTexts[r.intn(len(rfzTexts))]))); err != nil {
					t.Fatalf("append parent turn: %v", err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatalf("close parent writer: %v", err)
			}
		}

		divergenceTurn := r.intn(numEntries+4) - 1 // -1 .. numEntries+2 (in and out of range)
		editedMessage := rfzTexts[r.intn(len(rfzTexts))]
		label := ""
		if r.intn(2) == 0 {
			// A fork label is human-readable UI text (valid UTF-8 by contract). The
			// meta persists as JSON, which coerces invalid UTF-8 to U+FFFD, so the
			// byte-identity round-trip oracle below only holds for valid-UTF-8 labels;
			// coerce here rather than assert a round-trip JSON cannot provide. The raw
			// non-UTF-8 payload in rfzTexts still exercises message/name/note fields.
			label = strings.ToValidUTF8(rfzTexts[r.intn(len(rfzTexts))], "�")
		}

		childID, err := ForkSession(stateDir, parentID, divergenceTurn, editedMessage, label)

		if err != nil {
			if childID != "" {
				t.Fatalf("ForkSession error but non-empty childID %q: %v", childID, err)
			}
			return
		}

		// --- success oracle: a valid, internally consistent child branch ---
		if _, perr := ulid.Parse(childID); perr != nil {
			t.Fatalf("ForkSession success but childID %q is not a valid ULID: %v", childID, perr)
		}
		childMeta, merr := schema.LoadSessionMeta(stateDir, childID)
		if merr != nil {
			t.Fatalf("child meta not persisted after successful fork: %v", merr)
		}
		if childMeta.ParentSessionID != parentID {
			t.Fatalf("child meta ParentSessionID = %q, want %q", childMeta.ParentSessionID, parentID)
		}
		if childMeta.DivergenceTurn != divergenceTurn {
			t.Fatalf("child meta DivergenceTurn = %d, want %d", childMeta.DivergenceTurn, divergenceTurn)
		}
		if childMeta.ForkLabel != "" {
			t.Fatalf("child meta carries fork label %q; the label belongs on the parent", childMeta.ForkLabel)
		}

		childPath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
		_, entries, _, rerr := readTranscript(childPath)
		if rerr != nil {
			t.Fatalf("child transcript unreadable after successful fork: %v", rerr)
		}
		if len(entries) != divergenceTurn {
			t.Fatalf("child transcript has %d entries, want divergenceTurn=%d (prefix+edited)", len(entries), divergenceTurn)
		}
		last := entries[len(entries)-1]
		if last.Turn.Kind != schema.TurnUserInput {
			t.Fatalf("child transcript last turn kind = %q, want USER_INPUT (edited message)", last.Turn.Kind)
		}

		if label != "" {
			reparent, lerr := schema.LoadSessionMeta(stateDir, parentID)
			if lerr != nil {
				t.Fatalf("reload parent meta after labeled fork: %v", lerr)
			}
			if reparent.ForkLabel != label {
				t.Fatalf("parent meta ForkLabel = %q, want %q", reparent.ForkLabel, label)
			}
		}
	})
}

// FuzzRfzRestoreSessionFromMeta drives RestoreSessionFromMetaWithConfig with a
// fuzzed SessionMeta (context strategy, goal, pinned note, fork linkage, cheap
// model, token/turn counts), a scripted adapter (optionally fault-injecting the
// model call), a DenyEnv, and an optionally-corrupt on-disk transcript. Beyond
// never panicking it asserts:
//
//   - error  => nil session (no half-built session on the failure path);
//   - success => the restored session's id, turn count, fork parent, and pinned
//     note match the meta they were reconstructed from — the post-state is a
//     faithful projection of the persisted meta, not a partially-applied one.
func FuzzRfzRestoreSessionFromMeta(f *testing.F) {
	seeds := [][]byte{
		{0, 1, 0, 0, 0},
		{2, 0, 1, 1, 3, 4, 5, 1, 0},
		{9, 9, 1, 0, 0, 1, 2},
		{1, 3, 0, 0, 0, 0, 0, 0, 0, 0},
		{},
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		r := &seqReader{data: data}
		stateDir := t.TempDir()
		meta := rfzDecodeMeta(r)

		// Optionally seed a (fuzzed, possibly corrupt) transcript at the meta's
		// path so the history-recovery + ResumeHistory branch runs.
		if r.intn(2) == 0 && meta.ID != "" {
			tpath := filepath.Join(stateDir, sessionsSubdir, meta.ID+".transcript.jsonl")
			if err := os.MkdirAll(filepath.Dir(tpath), 0o755); err != nil {
				t.Fatalf("mkdir sessions: %v", err)
			}
			if err := os.WriteFile(tpath, data, 0o644); err != nil {
				t.Fatalf("seed transcript: %v", err)
			}
		}

		c := llm.NewClient()
		var faultResponder func(llm.Request) error
		if r.intn(4) == 0 {
			faultResponder = func(llm.Request) error { return errors.New("rfz injected model fault") }
		}
		c.Register(&agenttest.ScriptedAdapter{
			Provider:       "openai",
			Responder:      func(llm.Request) llm.Response { return agenttest.FinalResponse("done") },
			FaultResponder: faultResponder,
		})

		env := &agenttest.DenyEnv{WorkDir: t.TempDir()}
		restoreCfg := RestoreSessionConfig{
			StateDir:                stateDir,
			deferRestoreSideEffects: r.intn(2) == 0,
		}

		sess, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5"), env, meta, restoreCfg)
		if err != nil {
			if sess != nil {
				t.Fatalf("RestoreSessionFromMetaWithConfig error but non-nil session: %v", err)
			}
			return
		}
		defer sess.Close()

		// --- post-state consistency oracle ---
		if sess.id != meta.ID {
			t.Fatalf("restored session id = %q, want meta.ID %q", sess.id, meta.ID)
		}
		if sess.modelResponses != meta.TurnCount {
			t.Fatalf("restored modelResponses = %d, want meta.TurnCount %d", sess.modelResponses, meta.TurnCount)
		}
		if sess.fork.parentID != meta.ParentSessionID {
			t.Fatalf("restored fork.parentID = %q, want meta.ParentSessionID %q", sess.fork.parentID, meta.ParentSessionID)
		}
		if sess.pinnedNote != meta.PinnedNote {
			t.Fatalf("restored pinnedNote = %q, want meta.PinnedNote %q", sess.pinnedNote, meta.PinnedNote)
		}
	})
}

// rfzDecodeMeta builds a SessionMeta from the fuzzer's byte cursor. The ID is
// usually a fresh ULID (so restore has a stable path to write to) but sometimes
// empty, exercising the empty-id branch. Every other field is drawn to reach the
// meta-dependent restore branches: unknown/known context strategies, an active
// goal vs a dropped terminal goal, a cheap-model re-application, and fork linkage.
func rfzDecodeMeta(r *seqReader) schema.SessionMeta {
	id := ulid.Make().String()
	if r.intn(8) == 0 {
		id = ""
	}
	meta := schema.SessionMeta{
		ID:              id,
		ProfileID:       "openai",
		Model:           "gpt-5",
		TurnCount:       r.intn(5),
		LastInputTokens: r.intn(200000),
		PinnedNote:      rfzTexts[r.intn(len(rfzTexts))],
		Name:            rfzTexts[r.intn(len(rfzTexts))],
		OriginalPrompt:  rfzTexts[r.intn(len(rfzTexts))],
		ParentSessionID: rfzMaybeID(r),
		DivergenceTurn:  r.intn(6),
		ForkLabel:       rfzTexts[r.intn(len(rfzTexts))],
		Config: schema.ConfigSnapshot{
			ContextStrategy:  rfzStrategies[r.intn(len(rfzStrategies))],
			MaxSubagentDepth: r.intn(3),
			MaxTurns:         r.intn(4),
		},
	}
	if r.intn(3) == 0 {
		meta.CheapModel = "gpt-5-mini"
	}
	if r.intn(2) == 0 {
		meta.Goal = &schema.GoalSnapshot{
			Objective:  rfzTexts[r.intn(len(rfzTexts))],
			Status:     rfzGoalStatuses[r.intn(len(rfzGoalStatuses))],
			Iterations: r.intn(5),
		}
	}
	return meta
}

// rfzMaybeID returns a fresh ULID about half the time and the empty string the
// rest, so the fork-parent linkage is exercised both set and unset.
func rfzMaybeID(r *seqReader) string {
	if r.intn(2) == 0 {
		return ulid.Make().String()
	}
	return ""
}