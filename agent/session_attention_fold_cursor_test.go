package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// appendBulkHistory grows w by pairs of assistant and tool-result turns until
// the file holds at least minBytes of history, the shape a long root session's
// transcript takes.
func appendBulkHistory(tb testing.TB, w *transcript.Writer, minBytes int) {
	tb.Helper()
	assistant := strings.Repeat("a", 2*1024)
	output := strings.Repeat("o", 20*1024)
	written := 0
	for index := 0; written < minBytes; index++ {
		callID := fmt.Sprintf("call-%d", index)
		batch := []schema.Turn{
			schema.NewTurn(schema.TurnAssistant, llm.Assistant(assistant)),
			schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "read_file", output, false)),
		}
		if _, err := w.AppendBatch(batch); err != nil {
			tb.Fatalf("append bulk history: %v", err)
		}
		written += len(assistant) + len(output)
	}
}

// deliverAndConsumeRootAttention runs one delegate report's whole attention
// lifecycle on root: the durable append, the wake arm, and the consumed
// resolution.
func deliverAndConsumeRootAttention(tb testing.TB, root *Session, attentionID string) {
	tb.Helper()
	if _, err := root.appendDelegateNotificationDurably(attentionID, "<delegate-notification>done</delegate-notification>"); err != nil {
		tb.Fatalf("append %s: %v", attentionID, err)
	}
	if err := root.armDelegateAttention(attentionID); err != nil {
		tb.Fatalf("arm %s: %v", attentionID, err)
	}
	if err := root.resolveAttentionDurably([]string{attentionID}, delegateAttentionConsumed); err != nil {
		tb.Fatalf("resolve %s: %v", attentionID, err)
	}
}

// BenchmarkRootDelegateAttentionDelivery measures one delegate report's whole
// attention lifecycle on a root whose transcript already holds ~95 MB of
// history: the durable append (check and verify), the wake arm, and the
// consumed resolution (check and verify). One delivery runs before the timer,
// so the loop measures a session that has already read its transcript once.
func BenchmarkRootDelegateAttentionDelivery(b *testing.B) {
	stateDir := b.TempDir()
	root, err := NewSession(benchClient(), provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(b.TempDir()), SessionConfig{
		MaxSubagentDepth: 1,
		StateDir:         stateDir,
		NoProjectPrompts: true,
	})
	if err != nil {
		b.Fatalf("NewSession: %v", err)
	}
	b.Cleanup(root.Close)
	appendBulkHistory(b, root.attachedTranscript(), 95<<20)
	delivery := 0
	deliver := func() {
		delivery++
		deliverAndConsumeRootAttention(b, root, fmt.Sprintf("delegate:dlg_bench/delivery/%d", delivery))
	}
	deliver()
	for b.Loop() {
		deliver()
	}
}

// A delivery must not re-decode the receiver's whole transcript. The probe is
// an in-place rewrite of the first history entry, far from the tail, that
// keeps the file's identity and length: any read that decodes the transcript
// from byte zero fails on it, while a fold that resumes from where the last
// one stopped never looks at those bytes again.
func TestRootDelegateAttentionDeliveryDoesNotRedecodeTranscriptPrefix(t *testing.T) {
	stateDir := t.TempDir()
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, NoProjectPrompts: true}),
	)
	appendBulkHistory(t, root.attachedTranscript(), 256<<10)
	deliverAndConsumeRootAttention(t, root, "delegate:dlg_prefix/delivery/1")

	path := transcriptPath(stateDir, root.ID())
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	firstEntry := int64(bytes.IndexByte(raw, '\n') + 1)
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("X"), firstEntry); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readDelegateAttentionFold(path, root.ID()); err == nil {
		t.Fatal("a full fold decoded the rewritten first entry; the probe proves nothing")
	}

	deliverAndConsumeRootAttention(t, root, "delegate:dlg_prefix/delivery/2")
}

func newAttentionFoldTranscript(t *testing.T) (string, string, *transcript.Writer) {
	t.Helper()
	const sessionID = "attention-fold-cursor"
	path := transcriptPath(t.TempDir(), sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID: sessionID, CreatedAt: time.Unix(1700000000, 0).UTC(), ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	return path, sessionID, writer
}

func attentionSteeringTurn(attentionID, text string) schema.Turn {
	turn := schema.NewTurn(schema.TurnSteering, llm.User(text))
	turn.AttentionID = attentionID
	return turn
}

func attentionCommitTurn(toolCallID, deliveryID string) schema.Turn {
	turn := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(toolCallID, "delegate", "report", false))
	turn.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: toolCallID, DeliveryID: deliveryID}}
	return turn
}

func appendAttentionTurns(t *testing.T, w *transcript.Writer, turns ...schema.Turn) {
	t.Helper()
	for _, turn := range turns {
		if err := w.AppendDurable(turn); err != nil {
			t.Fatal(err)
		}
	}
}

// requireCursorMatchesFullFold reads path through cursor and through a full
// fold from byte zero and requires the same decision from both: the same fold,
// or the same error.
func requireCursorMatchesFullFold(t *testing.T, cursor *delegateAttentionFoldCursor, path, sessionID, step string) delegateAttentionFold {
	t.Helper()
	got, gotErr := cursor.read(path, sessionID)
	want, wantErr := readDelegateAttentionFold(path, sessionID)
	if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
		t.Fatalf("%s: cursor error %v, full fold error %v", step, gotErr, wantErr)
	}
	if wantErr == nil && !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: cursor fold\n%#v\nfull fold\n%#v", step, got, want)
	}
	return got
}

func TestDelegateAttentionFoldCursorMatchesFullFoldAcrossAppends(t *testing.T) {
	path, sessionID, writer := newAttentionFoldTranscript(t)
	var cursor delegateAttentionFoldCursor
	requireCursorMatchesFullFold(t, &cursor, path, sessionID, "header only")
	steps := []struct {
		name string
		turn schema.Turn
	}{
		{"steering a", attentionSteeringTurn("delegate:a", "alpha")},
		{"unrelated assistant turn", schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("x", 8*1024)))},
		{"steering b", attentionSteeringTurn("delegate:b", "bravo")},
		{"replayed steering a", attentionSteeringTurn("delegate:a", "alpha")},
		{"delivery commit", attentionCommitTurn("call-1", "delivery-1")},
		{"consume a", delegateAttentionResolutionTurnForGeneration("delegate:a", delegateAttentionConsumed, 7)},
		{"discard b", delegateAttentionResolutionTurn("delegate:b", delegateAttentionDiscarded)},
		{"replayed discard b", delegateAttentionResolutionTurn("delegate:b", delegateAttentionDiscarded)},
		{"steering c", attentionSteeringTurn("delegate:c", "charlie")},
	}
	for _, step := range steps {
		appendAttentionTurns(t, writer, step.turn)
		requireCursorMatchesFullFold(t, &cursor, path, sessionID, step.name)
		requireCursorMatchesFullFold(t, &cursor, path, sessionID, step.name+" unchanged")
	}
	var restarted delegateAttentionFoldCursor
	requireCursorMatchesFullFold(t, &restarted, path, sessionID, "fresh cursor after restart")
}

// A fold returned earlier stays what it was: extending the cursor must not
// write through into maps or slices a caller still holds.
func TestDelegateAttentionFoldCursorNeverMutatesAReturnedFold(t *testing.T) {
	path, sessionID, writer := newAttentionFoldTranscript(t)
	appendAttentionTurns(t, writer, attentionSteeringTurn("delegate:a", "alpha"))
	var cursor delegateAttentionFoldCursor
	before := requireCursorMatchesFullFold(t, &cursor, path, sessionID, "steering a")
	appendAttentionTurns(t, writer,
		attentionSteeringTurn("delegate:b", "bravo"),
		delegateAttentionResolutionTurn("delegate:a", delegateAttentionConsumed),
		attentionCommitTurn("call-1", "delivery-1"),
	)
	requireCursorMatchesFullFold(t, &cursor, path, sessionID, "after appends")
	if got := before.pendingIDs(); !reflect.DeepEqual(got, []string{"delegate:a"}) {
		t.Fatalf("earlier fold pending = %v, want [delegate:a]", got)
	}
	if len(before.order) != 1 || len(before.content) != 1 || len(before.turns) != 1 || len(before.deliveryCommits) != 0 {
		t.Fatalf("earlier fold changed after a later read: %#v", before)
	}
}

// A caller may change the fold it was handed — appendDelegateAttentionResolutions
// records each resolution it appends into it — and the cursor must not see
// that: the verify read after an append has to decide from the file alone.
// Here the resolution lands in a different transcript, so the file the cursor
// reads never records it.
func TestDelegateAttentionFoldCursorIgnoresChangesToAReturnedFold(t *testing.T) {
	path, sessionID, writer := newAttentionFoldTranscript(t)
	appendAttentionTurns(t, writer, attentionSteeringTurn("delegate:a", "alpha"))
	_, _, elsewhere := newAttentionFoldTranscript(t)
	var cursor delegateAttentionFoldCursor
	fold := requireCursorMatchesFullFold(t, &cursor, path, sessionID, "steering a")
	if err := appendDelegateAttentionResolutions(elsewhere, transcript.PlaceDelivery, "", fold, []string{"delegate:a"}, delegateAttentionConsumed, 0); err != nil {
		t.Fatal(err)
	}
	verified := requireCursorMatchesFullFold(t, &cursor, path, sessionID, "resolution recorded elsewhere")
	if _, resolved := verified.resolutions["delegate:a"]; resolved {
		t.Fatal("verify read reports a resolution this transcript never recorded")
	}
	appendAttentionTurns(t, writer, attentionSteeringTurn("delegate:b", "bravo"))
	requireCursorMatchesFullFold(t, &cursor, path, sessionID, "appended after")
}

// An ID repeated within one resolution call is appended once.
func TestAppendDelegateAttentionResolutionsAppendsARepeatedIDOnce(t *testing.T) {
	path, sessionID, writer := newAttentionFoldTranscript(t)
	appendAttentionTurns(t, writer, attentionSteeringTurn("delegate:a", "alpha"))
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendDelegateAttentionResolutions(writer, transcript.PlaceAsync, "", fold, []string{"delegate:a", "delegate:a"}, delegateAttentionConsumed, 0); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// One header, one steering entry, one resolution.
	if lines := bytes.Count(raw, []byte("\n")); lines != 3 {
		t.Fatalf("transcript has %d lines, want 3:\n%s", lines, raw)
	}
}

// A torn trailing line is not consumed: the cursor resumes at its start once
// it completes, exactly as a full fold picks it up.
func TestDelegateAttentionFoldCursorResumesAtATornTail(t *testing.T) {
	path, sessionID, writer := newAttentionFoldTranscript(t)
	appendAttentionTurns(t, writer, attentionSteeringTurn("delegate:a", "alpha"))
	var cursor delegateAttentionFoldCursor
	requireCursorMatchesFullFold(t, &cursor, path, sessionID, "steering a")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	line := encodeAttentionEntryLine(t, 1, attentionSteeringTurn("delegate:b", "bravo"))
	appendRaw(t, path, line[:len(line)/2])
	requireCursorMatchesFullFold(t, &cursor, path, sessionID, "torn tail")
	appendRaw(t, path, line[len(line)/2:])
	fold := requireCursorMatchesFullFold(t, &cursor, path, sessionID, "completed tail")
	if !reflect.DeepEqual(fold.pendingIDs(), []string{"delegate:a", "delegate:b"}) {
		t.Fatalf("completed tail pending = %v", fold.pendingIDs())
	}
}

// Each change a pure append cannot produce sends the cursor back to byte
// zero. Every case below is built so a cursor that resumed anyway would
// return a different fold than the full one.
func TestDelegateAttentionFoldCursorRefoldsWhenTheFileIsNotAnAppend(t *testing.T) {
	filler := schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("f", 8*1024)))
	cases := []struct {
		name   string
		mutate func(t *testing.T, path string, dropFrom int64)
	}{
		{"truncated", func(t *testing.T, path string, dropFrom int64) {
			truncateTo(t, path, dropFrom)
		}},
		{"truncated and regrown past the cursor", func(t *testing.T, path string, dropFrom int64) {
			truncateTo(t, path, dropFrom)
			appendRaw(t, path, encodeAttentionEntryLine(t, 2, attentionSteeringTurn("delegate:replacement", strings.Repeat("r", 512))))
		}},
		{"replaced by a file with a rewritten prefix", func(t *testing.T, path string, _ int64) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rewritten := bytes.Replace(raw, []byte("alpha"), []byte("alphz"), 1)
			rewritten = append(rewritten, encodeAttentionEntryLine(t, 9, attentionSteeringTurn("delegate:later", "later"))...)
			replacement := path + ".replacement"
			if err := os.WriteFile(replacement, rewritten, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"removed", func(t *testing.T, path string, _ int64) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, sessionID, writer := newAttentionFoldTranscript(t)
			appendAttentionTurns(t, writer, attentionSteeringTurn("delegate:a", "alpha"), filler)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			dropFrom := info.Size()
			appendAttentionTurns(t, writer, attentionSteeringTurn("delegate:b", "bravo"))
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			var cursor delegateAttentionFoldCursor
			requireCursorMatchesFullFold(t, &cursor, path, sessionID, "before")
			tc.mutate(t, path, dropFrom)
			requireCursorMatchesFullFold(t, &cursor, path, sessionID, "after")
			appendRaw(t, path, encodeAttentionEntryLine(t, 10, attentionSteeringTurn("delegate:next", "next")))
			requireCursorMatchesFullFold(t, &cursor, path, sessionID, "appended after")
		})
	}
}

// Errors are the full fold's errors: a foreign session never resumes, and a
// bad appended entry fails the resumed read just as it fails a full one.
func TestDelegateAttentionFoldCursorReportsTheFullFoldsErrors(t *testing.T) {
	path, sessionID, writer := newAttentionFoldTranscript(t)
	appendAttentionTurns(t, writer, attentionSteeringTurn("delegate:a", "alpha"))
	var cursor delegateAttentionFoldCursor
	requireCursorMatchesFullFold(t, &cursor, path, sessionID, "steering a")
	requireCursorMatchesFullFold(t, &cursor, path, "another-session", "foreign session")
	requireCursorMatchesFullFold(t, &cursor, path, sessionID, "back to the owner")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	appendAttentionTurns(t, writer, attentionSteeringTurn("delegate:a", "conflicting"))
	requireCursorMatchesFullFold(t, &cursor, path, sessionID, "conflicting content")
	requireCursorMatchesFullFold(t, &cursor, path, sessionID, "conflicting content again")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	truncateTo(t, path, info.Size())
	requireCursorMatchesFullFold(t, &cursor, path, sessionID, "conflict removed")
	appendRaw(t, path, []byte("{not json}\n"))
	requireCursorMatchesFullFold(t, &cursor, path, sessionID, "undecodable entry")
}

func encodeAttentionEntryLine(t *testing.T, seq int, turn schema.Turn) []byte {
	t.Helper()
	line, err := json.Marshal(transcript.Entry{Kind: "entry", MachineryFlagged: true, Seq: seq, Turn: turn})
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

func appendRaw(t *testing.T, path string, data []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func truncateTo(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
}
