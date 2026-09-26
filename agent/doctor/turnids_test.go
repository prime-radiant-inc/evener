package doctor

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// sidC is a third session id, for sweeps that span two buckets.
const sidC = "02wMz5TxvEMoJEDTDGOTin"

func plainTurn(kind schema.TurnKind, text string) schema.Turn {
	return schema.NewTurn(kind, llm.User(text))
}

// reservedTurn is a client-authored turn as the daemon persists it: the
// mutation that authored it, plus the turn id the daemon reserved for it.
func reservedTurn(kind schema.TurnKind, text, stableTurnID string) schema.Turn {
	turn := plainTurn(kind, text)
	turn.ClientMutationID = "cm-" + stableTurnID
	turn.StableTurnID = stableTurnID
	return turn
}

// A session that reserved a turn id before the reservation namespace existed
// persisted a StableTurnID like "turn_11" — inside the namespace the
// transcript's own entry-index numbering owns — so a reseed publishes that id
// for two different turns. The sweep names those sessions and leaves alone the
// ones whose reserved ids are namespaced ("turn_mN") or absent.
func TestScanTurnIDs_NamesOnlyTheSessionsCarryingEntryIndexReservedIDs(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeRichSession(t, bucket, sidA, []schema.Turn{
		plainTurn(schema.TurnUserInput, "first ask"),
		plainTurn(schema.TurnAssistant, "answer"),
		reservedTurn(schema.TurnUserInput, "second ask", "turn_11"),
	}, nil, schema.SessionMeta{})
	writeRichSession(t, bucket, sidB, []schema.Turn{
		reservedTurn(schema.TurnUserInput, "first ask", "turn_m2"),
		plainTurn(schema.TurnAssistant, "answer"),
		plainTurn(schema.TurnUserInput, "second ask"),
	}, nil, schema.SessionMeta{})

	scan, err := ScanTurnIDs(base)
	if err != nil {
		t.Fatalf("ScanTurnIDs: %v", err)
	}
	if scan.SessionsScanned != 2 {
		t.Errorf("SessionsScanned = %d, want 2", scan.SessionsScanned)
	}
	if len(scan.Unreadable) != 0 {
		t.Errorf("Unreadable = %#v, want none", scan.Unreadable)
	}
	if len(scan.Sessions) != 1 {
		t.Fatalf("Sessions = %#v, want exactly the session carrying turn_11 (%s)", scan.Sessions, sidA)
	}
	found := scan.Sessions[0]
	if found.SessionID != sidA {
		t.Errorf("SessionID = %q, want %q", found.SessionID, sidA)
	}
	if want := "proj:" + hash1 + ":" + sidA; found.TranscriptRef != want {
		t.Errorf("TranscriptRef = %q, want %q", found.TranscriptRef, want)
	}
	if want := filepath.Join(bucket, "sessions", sidA+".transcript.jsonl"); found.TranscriptPath != want {
		t.Errorf("TranscriptPath = %q, want %q", found.TranscriptPath, want)
	}
	want := []ReservedTurn{{TurnID: "turn_11", EntryIndex: 3, Kind: string(schema.TurnUserInput)}}
	if !reflect.DeepEqual(found.ReservedTurns, want) {
		t.Errorf("ReservedTurns = %#v, want %#v", found.ReservedTurns, want)
	}
}

// The sweep answers for the whole state root, not one bucket, and it answers in
// a stable order so two runs produce the same list to hand to a human.
func TestScanTurnIDs_SweepsEveryBucketInAStableOrder(t *testing.T) {
	base := t.TempDir()
	one := stateHomeBucket(base, hash1)
	two := stateHomeBucket(base, hash2)
	writeRichSession(t, one, sidA, []schema.Turn{
		reservedTurn(schema.TurnUserInput, "ask", "turn_4"),
	}, nil, schema.SessionMeta{})
	writeRichSession(t, two, sidC, []schema.Turn{
		reservedTurn(schema.TurnUserInput, "ask", "turn_1"),
	}, nil, schema.SessionMeta{})

	scan, err := ScanTurnIDs(base)
	if err != nil {
		t.Fatalf("ScanTurnIDs: %v", err)
	}
	got := []string{}
	for _, s := range scan.Sessions {
		got = append(got, s.TranscriptRef)
	}
	want := []string{"proj:" + hash1 + ":" + sidA, "proj:" + hash2 + ":" + sidC}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("swept refs = %v, want %v", got, want)
	}
	if scan.SessionsScanned != 2 {
		t.Errorf("SessionsScanned = %d, want 2", scan.SessionsScanned)
	}
}

// A reserved id lands on whatever turn the mutation authored — a queued input,
// a steer, or the diagnostic turn a failed mutation records — and the reader
// that reseeds a session honors the persisted id whatever kind of turn carries
// it. So the sweep is kind-agnostic and reports the kind as evidence.
func TestScanTurnIDs_FindsReservedIDsOnEveryKindOfAuthoredTurn(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeRichSession(t, bucket, sidA, []schema.Turn{
		plainTurn(schema.TurnUserInput, "first ask"),
		reservedTurn(schema.TurnSteering, "look here", "turn_7"),
		reservedTurn(schema.TurnFailure, "provider refused", "turn_9"),
	}, nil, schema.SessionMeta{})

	scan, err := ScanTurnIDs(base)
	if err != nil {
		t.Fatalf("ScanTurnIDs: %v", err)
	}
	if len(scan.Sessions) != 1 {
		t.Fatalf("Sessions = %#v, want the one session", scan.Sessions)
	}
	want := []ReservedTurn{
		{TurnID: "turn_7", EntryIndex: 2, Kind: string(schema.TurnSteering)},
		{TurnID: "turn_9", EntryIndex: 3, Kind: string(schema.TurnFailure)},
	}
	if !reflect.DeepEqual(scan.Sessions[0].ReservedTurns, want) {
		t.Errorf("ReservedTurns = %#v, want %#v", scan.Sessions[0].ReservedTurns, want)
	}
}

// An empty state root is a clean answer, not an error: nothing to scan, nothing
// affected.
func TestScanTurnIDs_EmptyStateDir(t *testing.T) {
	scan, err := ScanTurnIDs(t.TempDir())
	if err != nil {
		t.Fatalf("ScanTurnIDs: %v", err)
	}
	if scan.SessionsScanned != 0 || len(scan.Sessions) != 0 || len(scan.Unreadable) != 0 {
		t.Errorf("scan of an empty state root = %#v, want an empty sweep", scan)
	}
}

// A session with meta but no transcript persisted no turn ids at all, so it is
// not a session the sweep can or should answer for.
func TestScanTurnIDs_SkipsSessionsWithNoTranscript(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeFile(t, filepath.Join(bucket, "sessions", sidA+".meta.json"), `{"id":"`+sidA+`"}`)

	scan, err := ScanTurnIDs(base)
	if err != nil {
		t.Fatalf("ScanTurnIDs: %v", err)
	}
	if scan.SessionsScanned != 0 || len(scan.Sessions) != 0 || len(scan.Unreadable) != 0 {
		t.Errorf("scan = %#v, want a session with no transcript to be no session at all", scan)
	}
}

// A transcript this build cannot decode leaves the session's status UNKNOWN.
// Reporting it as clean would be the dangerous lie — the sweep exists to decide
// which sessions get deleted — so it lands in its own list, named, with the
// decode error, and the rest of the sweep still finishes.
func TestScanTurnIDs_ReportsUnreadableTranscriptsInsteadOfCallingThemClean(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeRichSession(t, bucket, sidA, []schema.Turn{
		reservedTurn(schema.TurnUserInput, "ask", "turn_3"),
	}, nil, schema.SessionMeta{})
	writeFile(t, filepath.Join(bucket, "sessions", sidB+".transcript.jsonl"),
		`{"kind":"header","format_version":2,"session_id":"`+sidB+`"}`+"\n"+
			`{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","invented_field":true}}`+"\n")

	scan, err := ScanTurnIDs(base)
	if err != nil {
		t.Fatalf("ScanTurnIDs: %v", err)
	}
	if scan.SessionsScanned != 2 {
		t.Errorf("SessionsScanned = %d, want 2", scan.SessionsScanned)
	}
	if len(scan.Sessions) != 1 || scan.Sessions[0].SessionID != sidA {
		t.Errorf("Sessions = %#v, want just %s — an unreadable transcript is not a clean one", scan.Sessions, sidA)
	}
	if len(scan.Unreadable) != 1 {
		t.Fatalf("Unreadable = %#v, want the undecodable session %s", scan.Unreadable, sidB)
	}
	unreadable := scan.Unreadable[0]
	if unreadable.SessionID != sidB {
		t.Errorf("unreadable SessionID = %q, want %q", unreadable.SessionID, sidB)
	}
	if !strings.Contains(unreadable.Error, "invented_field") {
		t.Errorf("unreadable Error = %q, want the decode failure that made it unreadable", unreadable.Error)
	}
}

// The rendered sweep is what a human reads before deleting sessions, so it has
// to name every affected session, the ids that condemn it, and the sessions the
// sweep could not answer for.
func TestRenderTurnIDScan_NamesTheAffectedAndTheUnanswerable(t *testing.T) {
	scan := TurnIDScan{
		StateBase:       "/state",
		SessionsScanned: 3,
		Sessions: []TurnIDSession{{
			SessionID:      sidA,
			TranscriptRef:  "proj:" + hash1 + ":" + sidA,
			TranscriptPath: "/state/sessions/" + sidA + ".transcript.jsonl",
			ReservedTurns:  []ReservedTurn{{TurnID: "turn_11", EntryIndex: 3, Kind: "USER_INPUT"}},
		}},
		Unreadable: []UnreadableTranscript{{
			SessionID:     sidB,
			TranscriptRef: "local:" + sidB,
			Error:         "decode transcript entry: unknown field",
		}},
	}
	got := RenderTurnIDScan(scan)
	for _, want := range []string{
		"scanned 3 sessions",
		"/state",
		"1 session",
		"proj:" + hash1 + ":" + sidA,
		"turn_11",
		"entry 3",
		"USER_INPUT",
		"local:" + sidB,
		"decode transcript entry: unknown field",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered sweep missing %q:\n%s", want, got)
		}
	}
}

// A clean sweep has to say so in words — an empty affected list rendered as
// silence reads like the tool failed.
func TestRenderTurnIDScan_SaysSoWhenNothingIsAffected(t *testing.T) {
	got := RenderTurnIDScan(TurnIDScan{StateBase: "/state", SessionsScanned: 4})
	if !strings.Contains(got, "scanned 4 sessions") {
		t.Errorf("rendered sweep missing the scanned count:\n%s", got)
	}
	if !strings.Contains(got, "no session") {
		t.Errorf("rendered clean sweep must say no session is affected:\n%s", got)
	}
}

// TestScanTurnIDs_LegacyBucketNamesSessionAndReportsRealRoot is the RED case for
// FU1: a legacy-named bucket (a directory name identifier.ValidateProjectID
// rejects — "0123456789abcdef" has no readable-portion/suffix split) holds a
// session with a reserved entry-index turn id. Sweeping from the bucket dir
// (the daemon's per-session state-dir spelling) must report the real swept
// root — the state home that resolveBuckets up-walked to, not the bucket dir
// the caller handed in — and the human render must name the affected session,
// since refFor emits no ref for a legacy-named bucket and the session id is
// the only identity the render can carry. Both assertions fail on current
// code: StateBase stores the caller's bucket dir, and the render buries the
// session id inside the transcript path.
func TestScanTurnIDs_LegacyBucketNamesSessionAndReportsRealRoot(t *testing.T) {
	stateHome := t.TempDir()
	// "0123456789abcdef": no readable-portion/suffix split, so
	// identifier.ValidateProjectID rejects it — a legacy-named bucket.
	legacyBucket := stateHomeBucket(stateHome, "0123456789abcdef")
	writeRichSession(t, legacyBucket, sidA, []schema.Turn{
		reservedTurn(schema.TurnUserInput, "ask", "turn_11"),
	}, nil, schema.SessionMeta{})

	// Pass the bucket dir as the base — the daemon's per-session state dir
	// spelling — so resolveBuckets up-walks to the state home and sweeps
	// every sibling bucket under it.
	scan, err := ScanTurnIDs(legacyBucket)
	if err != nil {
		t.Fatalf("ScanTurnIDs: %v", err)
	}
	if len(scan.Sessions) != 1 || scan.Sessions[0].SessionID != sidA {
		t.Fatalf("Sessions = %#v, want the one affected session %s", scan.Sessions, sidA)
	}

	// Defect 2: StateBase must be the real swept root (the state home),
	// not the project-bucket dir the caller handed in. The sweep enumerated
	// buckets under stateHome, so that is the root the header should name.
	if scan.StateBase != stateHome {
		t.Errorf("StateBase = %q, want the swept root %q", scan.StateBase, stateHome)
	}

	// Defect 1: the render must name the affected session as an identity,
	// not just bury the session id inside the transcript path. refFor emits
	// no ref for a legacy-named bucket, so the render's "name every affected
	// session" contract falls to the session id. Strip the path and check
	// the id survives — on current code the path is the only place it appears.
	got := RenderTurnIDScan(scan)
	renderedWithoutPath := strings.ReplaceAll(got, scan.Sessions[0].TranscriptPath, "")
	if !strings.Contains(renderedWithoutPath, sidA) {
		t.Errorf("render does not name the affected session %s outside the transcript path:\n%s", sidA, got)
	}
}

// TestRenderTurnIDScan_NamesAffectedSessionWhenRefEmpty is the pure-render RED
// case for defect 1's affected-session line: when TranscriptRef is empty (a
// legacy-named bucket refFor cannot consume) the render must still name the
// session. On current code the affected line is "·   (<path>)" — the session
// id appears only inside the path, so stripping the path removes it entirely.
func TestRenderTurnIDScan_NamesAffectedSessionWhenRefEmpty(t *testing.T) {
	scan := TurnIDScan{
		StateBase:       "/state",
		SessionsScanned: 1,
		Sessions: []TurnIDSession{{
			SessionID:      sidA,
			TranscriptRef:  "", // legacy-named bucket: refFor emits no ref
			TranscriptPath: "/state/sessions/" + sidA + ".transcript.jsonl",
			ReservedTurns:  []ReservedTurn{{TurnID: "turn_11", EntryIndex: 3, Kind: "USER_INPUT"}},
		}},
	}
	got := RenderTurnIDScan(scan)
	renderedWithoutPath := strings.ReplaceAll(got, scan.Sessions[0].TranscriptPath, "")
	if !strings.Contains(renderedWithoutPath, sidA) {
		t.Errorf("render does not name the affected session %s when the ref is empty:\n%s", sidA, got)
	}
}

// TestRenderTurnIDScan_NamesUnreadableSessionWhenRefEmpty is the pure-render
// RED case for defect 1's unreadable line: the unreadable line needs the same
// treatment as the affected line. When TranscriptRef is empty the current
// render is "·  — <error>" — no session id at all, so the session the sweep
// could not answer for is unnamed.
func TestRenderTurnIDScan_NamesUnreadableSessionWhenRefEmpty(t *testing.T) {
	scan := TurnIDScan{
		StateBase:       "/state",
		SessionsScanned: 1,
		Unreadable: []UnreadableTranscript{{
			SessionID:     sidB,
			TranscriptRef: "", // legacy-named bucket: refFor emits no ref
			Error:         "decode transcript entry: unknown field",
		}},
	}
	got := RenderTurnIDScan(scan)
	if !strings.Contains(got, sidB) {
		t.Errorf("render does not name the unreadable session %s when the ref is empty:\n%s", sidB, got)
	}
	// The unreadable line must not leave empty parens "()" when the ref is
	// absent — UnreadableTranscript carries no path, so empty parens hold
	// nothing and read as an artifact, not a location.
	if strings.Contains(got, "()") {
		t.Errorf("render emits empty parens for an empty ref:\n%s", got)
	}
}
