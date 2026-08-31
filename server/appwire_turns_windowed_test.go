package server

import (
	"path/filepath"
	"strconv"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// TestWindowedSnapshotPagesInFullPositionSpace: a windowed seed's Latest and
// Page cursors must live in the same front-anchored position space a full
// seed of the same transcript would produce, with pages below the window
// returning empty data (the hub serves those from its file-backed paging).
func TestWindowedSnapshotPagesInFullPositionSpace(t *testing.T) {
	snapshot := &appTurnSnapshot{threadID: "th_w"}
	// Simulate a windowed seed: 5 prefix turn positions, 3 window turns.
	window := make([]appwire.Turn, 3)
	for i := range window {
		window[i] = appwire.Turn{ID: "turn_" + strconv.Itoa(6+i), Items: []appwire.ThreadItem{{ID: "i" + strconv.Itoa(i)}}, ItemsView: "full", Status: appwire.TurnStatusCompleted}
	}
	snapshot.SeedWindowed(window, 5)

	// Latest(2): the newest 2 turns, cursor pointing at global position 6.
	turns, cursor := snapshot.Latest(2)
	if len(turns) != 2 || turns[0].ID != "turn_7" || turns[1].ID != "turn_8" {
		t.Fatalf("Latest(2) turns: %v", turns)
	}
	if cursor != "6" {
		t.Fatalf("Latest(2) cursor: got %q, want 6 (global space)", cursor)
	}

	// Page above the boundary: page from cursor 6 with limit 2 → turns 5-6
	// (global), i.e. window-local position 0 only (turn_5 belongs to the
	// prefix's position space; the hub serves it from disk).
	page := snapshot.Page("6", 2)
	if len(page.Data) != 1 || page.Data[0].ID != "turn_6" {
		t.Fatalf("Page(6,2) data: %v", page.Data)
	}
	if page.NextCursor != "5" {
		t.Fatalf("Page(6,2) next: got %q, want 5 (the prefix boundary)", page.NextCursor)
	}

	// Page at the boundary: cursor 5 (== prefixTurnCount) returns empty —
	// the hub's file-backed paging owns everything below.
	page = snapshot.Page("5", 2)
	if len(page.Data) != 0 {
		t.Fatalf("Page(5,2) data: %v, want empty below the window", page.Data)
	}
	if page.NextCursor != "5" {
		t.Fatalf("Page(5,2) next: got %q, want the cursor preserved", page.NextCursor)
	}

	// A limit larger than the window: everything the window holds, cursor
	// at the prefix boundary.
	turns, cursor = snapshot.Latest(10)
	if len(turns) != 3 {
		t.Fatalf("Latest(10) turns: %d, want 3", len(turns))
	}
	if cursor != "5" {
		t.Fatalf("Latest(10) cursor: got %q, want 5", cursor)
	}
}

// TestWindowedSnapshotLatestLimitBetweenWindowAndTotal: a limit that reaches
// past the window but not the full position space (window 3, prefix 5, total
// 8, limit 4) must return the whole window with the prefix-boundary cursor —
// the window cannot serve more turns than it holds. This pins the panic the
// naive `lo := len(turns)-limit` produced (slice bounds [-1:]) — limit is
// client-controlled, so the negative index was reachable from a request.
func TestWindowedSnapshotLatestLimitBetweenWindowAndTotal(t *testing.T) {
	snapshot := &appTurnSnapshot{threadID: "th_l"}
	window := make([]appwire.Turn, 3)
	for i := range window {
		window[i] = appwire.Turn{ID: "turn_" + strconv.Itoa(6+i), Items: []appwire.ThreadItem{{ID: "i" + strconv.Itoa(i)}}, ItemsView: "full", Status: appwire.TurnStatusCompleted}
	}
	snapshot.SeedWindowed(window, 5)

	turns, cursor := snapshot.Latest(4)
	if len(turns) != 3 {
		t.Fatalf("Latest(4) turns: %d, want 3 (the window cannot serve more than it holds)", len(turns))
	}
	if turns[0].ID != "turn_6" || turns[2].ID != "turn_8" {
		t.Fatalf("Latest(4) turn ids: %v", turns)
	}
	if cursor != "5" {
		t.Fatalf("Latest(4) cursor: got %q, want 5 (the prefix boundary — the hub serves the rest)", cursor)
	}
}

// TestWindowedSnapshotPreludeNotificationDoesNotShiftPositions: a live
// prelude notification arriving on a windowed snapshot must not insert a
// prelude turn into the window — the prelude's position belongs to the
// prefix the hub serves from disk, and a front insertion would shift every
// window position into the prefix's cursor space.
func TestWindowedSnapshotPreludeNotificationDoesNotShiftPositions(t *testing.T) {
	snapshot := &appTurnSnapshot{threadID: "th_p"}
	window := []appwire.Turn{{ID: "turn_6", Items: []appwire.ThreadItem{{ID: "i1"}}, ItemsView: "full", Status: appwire.TurnStatusCompleted}}
	snapshot.SeedWindowed(window, 5)

	// A prelude-announcing notification (e.g. plugin loaded, bundled into
	// the prelude turn by the projector).
	snapshot.Apply([]appserver.SequencedNotification{{
		Notification: appwire.Notification{
			Method: "plugin/loaded",
			Params: []byte(`{"name":"x"}`),
		},
	}})

	if len(snapshot.turns) != 1 {
		t.Fatalf("windowed snapshot grew to %d turns after a prelude notification; the prelude must not enter the window", len(snapshot.turns))
	}
	if snapshot.turns[0].ID != "turn_6" {
		t.Fatalf("window turn changed: %q", snapshot.turns[0].ID)
	}
}

// TestWindowedSnapshotMatchesFullProjectionDifferential: over the same
// suffix, the windowed form's turn ids and cursor space must equal what the
// full form produces over prefix+suffix. This is the differential that
// keeps the hub's live/past paging seam invisible to clients.
func TestWindowedSnapshotMatchesFullProjectionDifferential(t *testing.T) {
	// Full projection: turns 1..8 (ids turn_1..turn_8).
	full := make([]appwire.Turn, 8)
	for i := range full {
		full[i] = appwire.Turn{ID: "turn_" + strconv.Itoa(i+1), Items: []appwire.ThreadItem{{ID: "i" + strconv.Itoa(i)}}, ItemsView: "full", Status: appwire.TurnStatusCompleted}
	}
	fullSnapshot := &appTurnSnapshot{threadID: "th_d"}
	fullSnapshot.Seed(full)

	// Windowed projection: prefix 5, suffix turns 6..8 with the SAME ids.
	window := full[5:]
	windowedSnapshot := &appTurnSnapshot{threadID: "th_d"}
	windowedSnapshot.SeedWindowed(window, 5)

	// Latest must agree on turns and cursor.
	fullTurns, fullCursor := fullSnapshot.Latest(2)
	winTurns, winCursor := windowedSnapshot.Latest(2)
	if fullCursor != winCursor {
		t.Fatalf("Latest cursor mismatch: full=%q windowed=%q", fullCursor, winCursor)
	}
	if len(fullTurns) != len(winTurns) {
		t.Fatalf("Latest turn count mismatch: full=%d windowed=%d", len(fullTurns), len(winTurns))
	}
	for i := range fullTurns {
		if fullTurns[i].ID != winTurns[i].ID {
			t.Fatalf("Latest turn %d id mismatch: full=%q windowed=%q", i, fullTurns[i].ID, winTurns[i].ID)
		}
	}

	// Page from a cursor above the window. The full projection pages turns
	// 5-6 (next=4); the window holds only turn_6, so its page returns what
	// it holds and hands off AT the prefix boundary (next=5). The invariant
	// is not cursor equality — the windowed page is a prefix-truncated view,
	// and smaller pages near the boundary are the design — it is that the
	// windowed page never returns MORE than the full one, never skips a
	// position the full page covered, and its next cursor lands exactly on
	// the boundary the hub's file-backed paging takes over from.
	fullPage := fullSnapshot.Page("6", 2)
	winPage := windowedSnapshot.Page("6", 2)
	if len(winPage.Data) > len(fullPage.Data) {
		t.Fatalf("windowed page returned more turns than the full projection")
	}
	for i := range winPage.Data {
		if winPage.Data[i].ID != fullPage.Data[len(fullPage.Data)-len(winPage.Data)+i].ID {
			t.Fatalf("windowed page is not the trailing run of the full page's turns")
		}
	}
	if winPage.NextCursor != "5" {
		t.Fatalf("windowed next cursor: got %q, want the prefix boundary 5", winPage.NextCursor)
	}
	if fullPage.NextCursor != "4" {
		t.Fatalf("full next cursor: got %q, want 4 (sanity)", fullPage.NextCursor)
	}
}

// TestTurnsFromEntriesWindowedCountMatchesFullProjection is the drift guard
// for the sidecar's PrefixTurnCount: over the same transcript, the count the
// windowed projection returns must equal the number of turns the FULL
// projection holds below the suffix. The count is computed by the transcript
// package (prefixTurnCount) from the prefix entries the windowed form never
// sees, so this differential compares the count the production anchor
// actually wrote (read back from the sidecar) against the count the real
// full projection holds below the suffix — a drift in either side fails.
func TestTurnsFromEntriesWindowedCountMatchesFullProjection(t *testing.T) {
	const sessionID = "02wMz5Txv1C3Hut0M8GCeB"
	// A prefix mix that exercises the emission rules: projecting kinds, a
	// marker kind with empty text (projects nothing), and a prelude.
	header := transcript.Header{SessionID: sessionID, SystemPrompt: "system prompt"}
	// The fixture mirrors a real windowed resume's boundary: the sidecar
	// anchors AT the checkpoint entry, so the checkpoint is the SUFFIX's
	// first entry (ResumeHistory's window starts at it) and the prefix is
	// everything before it.
	prefix := []transcript.Entry{
		{Kind: "entry", Seq: 0, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("one"))},
		{Kind: "entry", Seq: 1, Turn: schema.NewTurn(schema.TurnAssistant, llm.Assistant("two"))},
		// Empty-text environment turn: ProjectTurn emits nothing.
		{Kind: "entry", Seq: 2, Turn: schema.NewTurn(schema.TurnEnvironment, llm.User("   "))},
	}
	suffix := []transcript.Entry{
		{Kind: "entry", Seq: 3, Turn: schema.NewTurn(schema.TurnCheckpoint, llm.User("compaction summary"))},
		{Kind: "entry", Seq: 4, Turn: schema.NewTurn(schema.TurnUserInput, llm.User("after"))},
		{Kind: "entry", Seq: 5, Turn: schema.NewTurn(schema.TurnAssistant, llm.Assistant("done"))},
	}
	project := func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		return apptranscript.ProjectTurn(turnID, entryIndex, turn, map[string]string{}, nil, apptranscript.ToolResultOutputImages)
	}

	// The count the sidecar actually carries: write this transcript and
	// resume it once — the full-scan anchor computes PrefixTurnCount from
	// the prefix entries it decoded.
	path := filepath.Join(t.TempDir(), sessionID+".transcript.jsonl")
	writer, err := transcript.NewWriter(path, header)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for _, entry := range append(append([]transcript.Entry{}, prefix...), suffix...) {
		if err := writer.Append(entry.Turn); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	first, _, err := transcript.OpenWriterForResume(path, sessionID)
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	sidecar, ok := transcript.ReadSidecar(path)
	if !ok {
		t.Fatalf("sidecar missing after the full-scan resume")
	}
	want := sidecar.PrefixTurnCount
	// The full projection over prefix+suffix: prelude + one turn per
	// projecting entry (2 prefix entries project; the empty environment
	// turn does not), then the suffix's turns.
	fullTurns, _ := apptranscript.TurnsFromEntries(header, append(append([]transcript.Entry{}, prefix...), suffix...), project)
	// Turns below the WINDOW in the full projection: the window's turns are
	// the SUFFIX entries' turns (the checkpoint entry is the suffix's first
	// entry — ResumeHistory's window starts AT it), so below-the-window is
	// everything the full projection holds before the first suffix turn.
	suffixTurns, _ := apptranscript.TurnsFromEntries(transcript.Header{}, suffix, project)
	below := len(fullTurns) - len(suffixTurns)
	if below != want {
		t.Fatalf("prefix turn count drift: sidecar says %d, full projection holds %d below the window", want, below)
	}

	// The windowed turns are the full projection's suffix turns — the same
	// turns at the same global ids — nothing else (the prelude belongs to the
	// prefix space; the hub's file-backed pages serve it).
	windowedTurns, windowedPrefix := apptranscript.TurnsFromEntriesWindowed(header, suffix, len(prefix), want, project)
	if windowedPrefix != want {
		t.Fatalf("windowed prefix count: got %d, want %d", windowedPrefix, want)
	}
	fullSuffix := fullTurns[len(fullTurns)-len(suffixTurns):]
	if len(windowedTurns) != len(fullSuffix) {
		t.Fatalf("windowed turns: got %d, want %d (the full projection's suffix turns)", len(windowedTurns), len(fullSuffix))
	}
	for i := range windowedTurns {
		if windowedTurns[i].ID != fullSuffix[i].ID {
			t.Fatalf("windowed turn %d id: got %q, want %q", i, windowedTurns[i].ID, fullSuffix[i].ID)
		}
	}
}
