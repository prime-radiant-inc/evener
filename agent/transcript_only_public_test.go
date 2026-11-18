package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func transcriptDataOf(t *testing.T, entries []transcript.Entry) transcriptData {
	t.Helper()
	data := transcriptData{Entries: entries}
	for _, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		data.EntryLines = append(data.EntryLines, line)
	}
	return data
}

// The model-facing transcript (read_session_transcript, find, raw ranges)
// shows exactly what it shows without transcript-only entries, renumbered the
// same way.
func TestPublicTranscriptOmitsTranscriptOnlyEntries(t *testing.T) {
	plainTurns := transcriptOnlyFixtureTurns()
	plain := entriesOf(plainTurns)
	interleaved := entriesOf(schematest.InterleaveTranscriptOnly(plainTurns))
	if got, want := publicTranscriptEntries(interleaved), publicTranscriptEntries(plain); !reflect.DeepEqual(got, want) {
		t.Fatalf("public entries differ:\n got %d\nwant %d", len(got), len(want))
	}
	got := publicTranscriptData(transcriptDataOf(t, interleaved))
	want := publicTranscriptData(transcriptDataOf(t, plain))
	if !reflect.DeepEqual(got.Entries, want.Entries) || !reflect.DeepEqual(got.EntryLines, want.EntryLines) {
		t.Fatal("public transcript data differs")
	}
	sampleLine := transcriptDataOf(t, interleaved[:1]).EntryLines[0]
	decoded, err := transcript.DecodeEntry(sampleLine)
	if err != nil {
		t.Fatal(err)
	}
	if _, include, err := publicTranscriptLine(sampleLine, 0, decoded.Turn); err != nil || include {
		t.Fatalf("publicTranscriptLine of a transcript-only entry: include=%v err=%v", include, err)
	}
	if _, err := transcriptExpansionJSONL(transcriptDataOf(t, interleaved), 0); err == nil {
		t.Fatal("a transcript-only entry was expandable as a public turn")
	}
}

// A compaction fold re-appends, after its markers, an entry it already wrote
// before them (schema.Turn.OriginalOrdinal). The model never sees the pair
// twice: ResumeHistory anchors on the last marker and keeps only the copy. The
// public transcript is a view of the same conversation, so it must show the
// block once too, the way the ATIF projection and the doctor's transcript read
// already do.
func TestPublicTranscriptOmitsFoldCopies(t *testing.T) {
	t.Parallel()
	const text = "one message, folded once"
	original := schema.NewTurn(schema.TurnUserInput, llm.User(text))
	ordinal := uint64(0)
	copied := schema.NewTurn(schema.TurnUserInput, llm.User(text))
	copied.OriginalOrdinal = &ordinal
	entries := []transcript.Entry{makeEntry(original), makeEntry(copied)}

	if got := publicTranscriptEntries(entries); len(got) != 1 {
		t.Fatalf("public transcript shows %d entries, want exactly 1: a fold copy repeats the entry its original_ordinal names", len(got))
	}
	if got := publicTranscriptData(transcriptDataOf(t, entries)); len(got.Entries) != 1 {
		t.Fatalf("public transcript data holds %d entries, want exactly 1", len(got.Entries))
	}

	// The rendered document and the expand_turn addresses must agree. Addresses
	// count public entries, so a dropped one renumbers what follows it — the
	// behavior an already-dropped kind like TURN_COMPLETION has always had.
	// What matters here is that the surviving block renders once and stays
	// expandable at its own address.
	root := newBucket(t)
	id := "02wMz5Txv1C3Hut0M8GCeB"
	writeFindTranscript(t, root, id, time.Time{}, original, copied)
	value, err := readMarkdownPage(context.Background(), filepath.Clean(transcriptPath(root, id)), root, "", schema.SessionMeta{}, "", nil, 0, 0)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if got := strings.Count(value.(readMarkdownEnvelope).Content, text); got != 1 {
		t.Fatalf("rendered transcript shows the block %d times, want exactly 1", got)
	}
	pin := 0
	page, err := readMarkdownPage(context.Background(), filepath.Clean(transcriptPath(root, id)), root, "", schema.SessionMeta{}, "", &pin, 0, 0)
	if err != nil {
		t.Fatalf("expand the surviving block: %v", err)
	}
	if got := strings.Count(page.(readMarkdownEnvelope).Content, text); got != 1 {
		t.Fatalf("expanded block shows the text %d times, want exactly 1", got)
	}
}

// A line can carry the same field twice in different cases, and encoding/json
// matches field names case-insensitively: the last occurrence wins, so
// {"original_ordinal":0,"ORIGINAL_ORDINAL":null} decodes to a public turn. A
// projection that asked the raw bytes for the exact key instead would call the
// same entry a copy, and then the range it counted and the lines it emitted
// would disagree — the range omits the newest turn while the copy it should
// have dropped still renders.
func TestPublicTranscriptDecidesInclusionFromDecodedTurn(t *testing.T) {
	t.Parallel()
	const first = "the earlier message"
	const last = "the message after the fold"
	root := newBucket(t)
	id := "02wMz5Txv1C3Hut0M8GCeB"
	writeFindTranscript(t, root, id, time.Time{},
		schema.NewTurn(schema.TurnUserInput, llm.User(first)),
		schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]\nfolded\n[END CHECKPOINT]")),
		schema.NewTurn(schema.TurnUserInput, llm.User(last)),
	)
	path := filepath.Clean(transcriptPath(root, id))

	// Rewrite the newest entry's turn to carry both spellings: the exact key
	// says "copy", the field the decoder actually reads says "public".
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(file), "\n"), "\n")
	entryLine := lines[len(lines)-1]
	var entry map[string]json.RawMessage
	if err := json.Unmarshal([]byte(entryLine), &entry); err != nil {
		t.Fatal(err)
	}
	var turn map[string]json.RawMessage
	if err := json.Unmarshal(entry["turn"], &turn); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	dup := strings.TrimSuffix(string(encoded), "}") + `,"original_ordinal":0,"ORIGINAL_ORDINAL":null}`
	entry["turn"] = json.RawMessage(dup)
	rewritten, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	lines[len(lines)-1] = string(rewritten)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The strict decoder is the authority, and it reads the entry as public.
	decoded, err := transcript.DecodeEntry([]byte(lines[len(lines)-1]))
	if err != nil {
		t.Fatalf("decode rewritten entry: %v", err)
	}
	if !decoded.Turn.InPublicTranscript() {
		t.Fatalf("rewritten entry does not decode as a public turn, so this fixture cannot test the disagreement")
	}

	value, err := readMarkdownPage(context.Background(), path, root, "", schema.SessionMeta{}, "last:1", nil, 0, 0)
	if err != nil {
		t.Fatalf("markdown last:1: %v", err)
	}
	markdown := value.(readMarkdownEnvelope).Content
	raw, err := readRaw(context.Background(), path, root, "", "last:1")
	if err != nil {
		t.Fatalf("jsonl last:1: %v", err)
	}
	jsonl := raw.(readRawEnvelope).Content

	if !strings.Contains(markdown, last) || !strings.Contains(jsonl, last) {
		t.Fatalf("last:1 dropped the newest message in a format: markdown=%v jsonl=%v", strings.Contains(markdown, last), strings.Contains(jsonl, last))
	}
	if strings.Contains(markdown, first) || strings.Contains(jsonl, first) {
		t.Fatalf("last:1 rendered the earlier message in a format: markdown=%v jsonl=%v", strings.Contains(markdown, first), strings.Contains(jsonl, first))
	}

	// Both emitted forms must decode to the same answer the source did. Redaction
	// re-marshals the raw map and marshaling sorts keys: a source line carrying
	// original_ordinal in two cases would emit the uppercase spelling first and
	// re-decode as a fold copy, so a consumer of this JSONL would drop the turn
	// the range selected while the markdown path shows it.
	assertPublicEmitted(t, "jsonl range", jsonl, last)
	// The newest message's own address: original at 0, marker at 1, later at 2.
	pin := 2
	expanded, err := readMarkdownPage(context.Background(), path, root, "", schema.SessionMeta{}, "", &pin, 0, 0)
	if err != nil {
		t.Fatalf("expand_turn: %v", err)
	}
	expansion := expanded.(readMarkdownEnvelope).Expansion
	if expansion == nil {
		t.Fatal("expand_turn returned no expansion payload")
	}
	assertPublicEmitted(t, "expand_turn", expansion.Data, last)
}

// assertPublicEmitted decodes each emitted entry line carrying the marker text
// and reports whether the public-transcript predicate keeps it. The predicate is
// the reader's own: this is the assertion that catches an emitted key
// contradicting the decision the projection made.
func assertPublicEmitted(t *testing.T, surface, content, marker string) {
	t.Helper()
	found := false
	for line := range strings.SplitSeq(strings.TrimSuffix(content, "\n"), "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		entry, err := transcript.DecodeEntry([]byte(line))
		if err != nil {
			t.Fatalf("%s: decode emitted line: %v", surface, err)
		}
		if !entry.Turn.InPublicTranscript() {
			t.Fatalf("%s: emitted line decodes as a non-public turn, so its reader drops it:\n%s", surface, line)
		}
		found = true
	}
	if !found {
		t.Fatalf("%s: emitted no line carrying the selected message", surface)
	}
}

// foldCopyFixture writes an original, a compaction marker, its fold copy, and a
// later message — the shape every affected transcript has. The copy is not a
// public entry, so the public transcript holds the original at 0 and the later
// message at 1.
func foldCopyFixture(t *testing.T) (path, root, id, first, last string) {
	t.Helper()
	root = newBucket(t)
	id = "02wMz5Txv1C3Hut0M8GCeB"
	first = "the earlier message"
	last = "the message after the fold"
	original := schema.NewTurn(schema.TurnUserInput, llm.User(first))
	marker := schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]\nfolded\n[END CHECKPOINT]"))
	ordinal := uint64(0)
	copied := schema.NewTurn(schema.TurnUserInput, llm.User(first))
	copied.OriginalOrdinal = &ordinal
	later := schema.NewTurn(schema.TurnUserInput, llm.User(last))
	writeFindTranscript(t, root, id, time.Time{}, original, marker, copied, later)
	return filepath.Clean(transcriptPath(root, id)), root, id, first, last
}

// A range counts public entries, and a fold copy is not one. Selecting the last
// turn must therefore return the message after the copy, not the copy: the
// selection and the emitted lines have to count the same entries, or the range
// that names the newest turn renders something else and leaves it out.
func TestPublicTranscriptRangeSkipsFoldCopyInBothFormats(t *testing.T) {
	t.Parallel()
	path, root, _, first, last := foldCopyFixture(t)

	value, err := readMarkdownPage(context.Background(), path, root, "", schema.SessionMeta{}, "last:1", nil, 0, 0)
	if err != nil {
		t.Fatalf("markdown last:1: %v", err)
	}
	markdown := value.(readMarkdownEnvelope).Content
	if !strings.Contains(markdown, last) {
		t.Fatalf("markdown last:1 did not render the newest message; the range counted an entry the public view omits")
	}
	if strings.Contains(markdown, first) {
		t.Fatalf("markdown last:1 rendered the folded message, not the newest one:\n%s", markdown)
	}

	raw, err := readRaw(context.Background(), path, root, "", "last:1")
	if err != nil {
		t.Fatalf("jsonl last:1: %v", err)
	}
	jsonl := raw.(readRawEnvelope).Content
	if !strings.Contains(jsonl, last) {
		t.Fatalf("jsonl last:1 did not emit the newest message:\n%s", jsonl)
	}
	if strings.Contains(jsonl, first) {
		t.Fatalf("jsonl last:1 emitted the folded message, not the newest one:\n%s", jsonl)
	}
}

// doctor transcript numbers entries by the same rule read_transcript does, so a
// number it shows addresses the same turn there. A fold copy holding no public
// position must not push that numbering one ahead of the document's.
func TestDoctorTranscriptNumbersMatchReadTranscriptAcrossFoldCopy(t *testing.T) {
	t.Parallel()
	path, root, id, first, last := foldCopyFixture(t)

	doc, err := doctor.Transcript(context.Background(), filepath.Dir(filepath.Dir(path)), id, doctor.TranscriptOpts{})
	if err != nil {
		t.Fatalf("doctor transcript: %v", err)
	}
	var doctorNumber *int
	for i := range doc.Turns {
		if doc.Turns[i].Text == last {
			doctorNumber = doc.Turns[i].Turn
			break
		}
	}
	if doctorNumber == nil {
		t.Fatalf("doctor transcript did not render the newest message as a numbered turn")
	}

	// The same entry through read_transcript, found by its own rendered text so
	// the comparison does not reuse doctor's numbering.
	value, err := readMarkdownPage(context.Background(), path, root, "", schema.SessionMeta{}, "", nil, 0, 0)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	document := value.(readMarkdownEnvelope).Content
	address := -1
	for seq := 0; seq < value.(readMarkdownEnvelope).Meta.TurnsTotal; seq++ {
		page, err := readMarkdownPage(context.Background(), path, root, "", schema.SessionMeta{}, "", &seq, 0, 0)
		if err != nil || !strings.Contains(page.(readMarkdownEnvelope).Content, last) {
			continue
		}
		if strings.Contains(page.(readMarkdownEnvelope).Content, first) {
			continue
		}
		address = seq
		break
	}
	if address < 0 || !strings.Contains(document, last) {
		t.Fatalf("read_transcript did not render the newest message as an expandable turn")
	}
	if *doctorNumber != address {
		t.Fatalf("doctor numbers the newest message turn %d, read_transcript addresses it as %d", *doctorNumber, address)
	}
}
