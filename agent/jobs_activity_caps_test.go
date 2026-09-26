package agent

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

// TestTruncateActivityText_DoesNotMaterializeTheInput pins that capping the
// output also bounds the work. Truncating must not allocate proportional to the
// input: the earlier []rune(s) implementation turned a 4 MiB label into a
// ~16 MiB slice to keep 200 runes, defeating the memory bound the cap exists
// for.
func TestTruncateActivityText_DoesNotMaterializeTheInput(t *testing.T) {
	// Not parallel: it measures process-wide allocation.
	input := strings.Repeat("a", activityMaxEncodedBytes) // 4 MiB
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	got := truncateActivityText(input, activityMaxLabelRunes)
	runtime.ReadMemStats(&after)
	if n := len([]rune(got)); n != activityMaxLabelRunes {
		t.Fatalf("label = %d runes, want %d", n, activityMaxLabelRunes)
	}
	// 64 KiB is far below the input and far above the few hundred bytes the
	// result costs, so it separates "walked the prefix" from "copied it all".
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<10 {
		t.Fatalf("allocated %d bytes to cap a %d-byte string at %d runes: truncation materialized the input", allocated, len(input), activityMaxLabelRunes)
	}
}

// TestProjectBoundedActivityTree_CapsOutcomeReasonAndOriginIDs pins that the
// externally sourced delegate fields — an outcome's Reason, a provider's
// OriginToolCallID/OriginItemID — are capped too. An oversized reason is a
// fixed part of any page carrying the delegate, so without a cap the size trim
// discards the renderable child underneath it.
func TestProjectBoundedActivityTree_CapsOutcomeReasonAndOriginIDs(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("\x01", 1<<20)
	child := &activitySessionSnapshot{SessionID: "child", Ref: "local:child"}
	child.Jobs = []*jobstore.JobRecord{{
		JobID: "job_child", Type: jobstore.JobShell, OwnerSessionID: "child", Status: jobstore.StatusRunning,
	}}
	row := stableActivitySnapshot("dlg_0", "root", "child", "brief")
	row.descriptor.OriginToolCallID = strings.Repeat("t", 4096)
	row.descriptor.OriginItemID = strings.Repeat("i", 4096)
	row.lastOutcome = &delegatestore.Outcome{Status: delegatestore.OutcomeCompleted, Reason: huge}
	root := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_0": row},
		Children:        map[string]*activitySessionSnapshot{"child": child},
	}

	got, err := projectBoundedActivityTree(root, "root", 0, 0, 0, time.Unix(10, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > activityMaxEncodedBytes {
		t.Fatalf("page = %d bytes, over the %d-byte limit", len(raw), activityMaxEncodedBytes)
	}
	if len(got.Root.Entries) != 1 || got.Root.Entries[0].Delegate == nil {
		t.Fatalf("entries = %+v, want the delegate retained", got.Root.Entries)
	}
	delegate := got.Root.Entries[0].Delegate
	if delegate.Child == nil || len(delegate.Child.Entries) != 1 {
		t.Fatalf("delegate child = %+v, want the renderable child retained under an oversized reason", delegate.Child)
	}
	if !strings.HasSuffix(delegate.Reason, "…") {
		t.Fatalf("Reason (%d bytes) was not capped", len(delegate.Reason))
	}
	if !strings.HasSuffix(delegate.OriginToolCallID, "…") || !strings.HasSuffix(delegate.OriginItemID, "…") {
		t.Fatalf("origin IDs not capped: toolCall=%d item=%d", len(delegate.OriginToolCallID), len(delegate.OriginItemID))
	}
}

// TestProjectBoundedActivityTree_BoundsMaxDepthAncestorChain pins the depth
// awareness of the caps. A continuation page's fixed parts are its whole
// ancestor chain (up to activityMaxContinuationPathLength sessions), and
// per-field rune caps do not bound their sum — the more so because JSON encodes
// a control rune as six bytes. A deepest-possible chain of control-character
// prose must still fit, with no entry to drop.
func TestProjectBoundedActivityTree_BoundsMaxDepthAncestorChain(t *testing.T) {
	t.Parallel()
	const levels = activityMaxContinuationPathLength
	prose := strings.Repeat("\x01", activityMaxDelegateProseRunes)
	warnings := make([]string, activityMaxDelegateWarnings)
	for i := range warnings {
		warnings[i] = strings.Repeat("\x01", activityMaxDelegateWarningRunes)
	}
	leaf := &activitySessionSnapshot{SessionID: fmt.Sprintf("s%d", levels), Ref: fmt.Sprintf("local:s%d", levels)}
	node := leaf
	for i := levels - 1; i >= 0; i-- {
		ownerID := fmt.Sprintf("s%d", i)
		childID := node.SessionID
		row := stableActivitySnapshot(fmt.Sprintf("dlg_%d", i), ownerID, childID, prose)
		row.descriptor.Description = prose
		row.notResumableReason = prose
		row.lastOutcome = &delegatestore.Outcome{Status: delegatestore.OutcomeCompleted, Reason: prose}
		row.latestPacket = &delegatestore.TerminalPacket{
			Message:                json.RawMessage(`"` + strings.Repeat("m", activityMaxDelegatePayloadBytes) + `"`),
			StructuredResult:       json.RawMessage(`"` + strings.Repeat("s", activityMaxDelegatePayloadBytes) + `"`),
			StructuredResultReason: prose,
			Warnings:               warnings,
		}
		node = &activitySessionSnapshot{
			SessionID: ownerID, Ref: "local:" + ownerID, RootID: "root",
			StableDelegates: map[string]delegateSnapshot{row.id: row},
			Children:        map[string]*activitySessionSnapshot{childID: node},
		}
	}

	got, err := projectBoundedActivityTree(*node, "root", -levels, 0, 0, time.Unix(10, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > activityMaxEncodedBytes {
		t.Fatalf("page = %d bytes, over the %d-byte limit", len(raw), activityMaxEncodedBytes)
	}
	if got.Root.Branch.Error != "" {
		t.Fatalf("branch error = %q, want none: the ancestor chain was shrunk to fit", got.Root.Branch.Error)
	}
	// Every ancestor must still be present: bounding the fixed content is only
	// worth doing if it keeps the page from sacrificing the chain (and with it
	// the path to the target) to make room.
	session := &got.Root
	for level := range levels {
		if session.Branch.Truncated {
			t.Fatalf("level %d was trimmed; the ancestor content should have been shrunk first", level)
		}
		if len(session.Entries) != 1 || session.Entries[0].Delegate == nil || session.Entries[0].Delegate.Child == nil {
			t.Fatalf("level %d lost its delegate: entries = %+v", level, session.Entries)
		}
		session = session.Entries[0].Delegate.Child
	}
}

// TestProjectBoundedActivityTree_CapsTheSessionLabel pins the fix for an
// envelope whose own label alone exceeds the limit. A session with no
// generated name labels itself with its OriginalPrompt verbatim, so a pasted
// multi-megabyte prompt used to make every page reporting that session — its
// own and any continuation carrying it as an ancestor — go out over
// activityMaxEncodedBytes with no entry left to drop. The label is capped at
// projection time instead, and the page fits.
func TestProjectBoundedActivityTree_CapsTheSessionLabel(t *testing.T) {
	t.Parallel()
	prompt := strings.Repeat("p", 4<<20) // a 4MB OriginalPrompt
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		Label: prompt,
	}
	got, err := projectBoundedActivityTree(snap, "root", 0, 0, 0, time.Unix(10, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > activityMaxEncodedBytes {
		t.Fatalf("page = %d bytes, over the %d-byte limit", len(raw), activityMaxEncodedBytes)
	}
	if got.Root.Branch.Error != "" {
		t.Fatalf("branch error = %q, want none: the label is capped, not reported as unfittable", got.Root.Branch.Error)
	}
	if n := len([]rune(got.Root.Label)); n > activityMaxLabelRunes {
		t.Fatalf("label = %d runes, want at most %d", n, activityMaxLabelRunes)
	}
	if !strings.HasSuffix(got.Root.Label, "…") {
		t.Fatalf("label %q does not say it was cut", got.Root.Label)
	}
}

// TestProjectActivitySession_CapsDelegateProse pins that a delegate's
// unbounded prose fields — its Mandate/Task and Description — are capped at
// projection time. They sit on every delegate in the ancestor chain a
// continuation page carries, so an unbounded brief is a fixed part of the
// envelope that trimming entries cannot shrink.
func TestProjectActivitySession_CapsDelegateProse(t *testing.T) {
	t.Parallel()
	prose := strings.Repeat("t", 3*activityMaxDelegateProseRunes)
	row := stableActivitySnapshot("dlg_1", "root", "child", prose)
	row.descriptor.Description = prose
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": row},
	}
	got := projectActivitySession(snap, newActivityBudget())
	if len(got.Entries) != 1 || got.Entries[0].Delegate == nil {
		t.Fatalf("entries = %+v, want one delegate", got.Entries)
	}
	delegate := got.Entries[0].Delegate
	for name, value := range map[string]string{
		"Mandate":     delegate.Mandate,
		"Task":        delegate.Task,
		"Description": delegate.Description,
	} {
		if n := len([]rune(value)); n > activityMaxDelegateProseRunes {
			t.Fatalf("delegate %s = %d runes, want at most %d", name, n, activityMaxDelegateProseRunes)
		}
		if !strings.HasSuffix(value, "…") {
			t.Fatalf("delegate %s does not say it was cut", name)
		}
	}
}

// TestProjectActivitySession_CollapsesUnsupportedTypeErrors pins that a
// session whose journal holds many records this projection cannot render
// reports them once, counted, rather than appending one sentence per record
// to Branch.Error — an unbounded envelope input, and unreadable besides.
func TestProjectActivitySession_CollapsesUnsupportedTypeErrors(t *testing.T) {
	t.Parallel()
	const n = 64
	records := make([]*jobstore.JobRecord, 0, n)
	for i := range n {
		records = append(records, &jobstore.JobRecord{
			JobID:          fmt.Sprintf("job_%d", i),
			Type:           jobstore.JobType("unknown"),
			OwnerSessionID: "root",
			Status:         jobstore.StatusRunning,
		})
	}
	snap := activitySessionSnapshot{SessionID: "root", Ref: "local:root", Jobs: records}
	got := projectActivitySession(snap, newActivityBudget())
	if c := strings.Count(got.Branch.Error, `job "`); c != 1 {
		t.Fatalf("branch error = %q, want it to name exactly one offender, got %d", got.Branch.Error, c)
	}
	if !strings.Contains(got.Branch.Error, "unsupported type") {
		t.Fatalf("branch error = %q, want it to say unsupported type", got.Branch.Error)
	}
	if want := strconv.Itoa(n); !strings.Contains(got.Branch.Error, want) {
		t.Fatalf("branch error = %q, want the count %s of collapsed records", got.Branch.Error, want)
	}
}

// TestMarkActivityEnvelopeTooLarge_WithdrawsTheContinuation pins the shape of
// the report for a page whose own fixed parts exceed the limit: the
// continuation is withdrawn (a token would re-produce this same page forever)
// and the error names the response's own size rather than blaming an entry.
func TestMarkActivityEnvelopeTooLarge_WithdrawsTheContinuation(t *testing.T) {
	t.Parallel()
	session := appwire.JobActivitySession{SessionID: "root"}
	session.Branch.Truncated = true
	session.Branch.Continuation = "stale-token"
	markActivityEnvelopeTooLarge(&session, activityMaxEncodedBytes+1)
	if session.Branch.Continuation != "" {
		t.Fatalf("continuation %q survived a page that can render nothing", session.Branch.Continuation)
	}
	if !strings.Contains(session.Branch.Error, "no entries rendered") {
		t.Fatalf("branch error = %q, want it to report the response's own size", session.Branch.Error)
	}
	if want := strconv.Itoa(activityMaxEncodedBytes + 1); !strings.Contains(session.Branch.Error, want) {
		t.Fatalf("branch error = %q, want the measured size %s in it", session.Branch.Error, want)
	}
	if strings.Contains(session.Branch.Error, "job") {
		t.Fatalf("branch error = %q mentions an entry, want it to blame the envelope", session.Branch.Error)
	}
}

// TestTruncateActivityText_IsRuneSafe pins the two multi-byte cases the byte
// fast path alone gets wrong: a string whose byte length exceeds the cap while
// its rune count does not (which used to slice past len(runes) and panic), and
// one that genuinely needs cutting (which must land on a rune boundary).
func TestTruncateActivityText_IsRuneSafe(t *testing.T) {
	t.Parallel()
	// 150 runes, 300 bytes: over the 200-byte fast path, under the 200-rune cap.
	withinRunes := strings.Repeat("é", 150)
	if got := truncateActivityText(withinRunes, activityMaxLabelRunes); got != withinRunes {
		t.Fatalf("150-rune string was altered to %q", got)
	}

	long := strings.Repeat("é", activityMaxLabelRunes+50)
	got := truncateActivityText(long, activityMaxLabelRunes)
	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a multi-byte rune: %q", got)
	}
	if n := len([]rune(got)); n != activityMaxLabelRunes {
		t.Fatalf("truncated to %d runes, want %d", n, activityMaxLabelRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated value %q does not say it was cut", got)
	}
}

// TestProjectActivitySession_CapsDelegatePayloads pins that the remaining
// free-form delegate fields — the raw terminal-packet payloads and the
// warnings list — are bounded too. They are fixed parts of a continuation
// page's ancestor chain, so an oversized one cannot be trimmed away, and a raw
// payload is dropped rather than sliced so the wire never carries invalid JSON.
func TestProjectActivitySession_CapsDelegatePayloads(t *testing.T) {
	t.Parallel()
	row := stableActivitySnapshot("dlg_1", "root", "child", "brief")
	warnings := make([]string, activityMaxDelegateWarnings+4)
	for i := range warnings {
		warnings[i] = strings.Repeat("w", activityMaxDelegateWarningRunes*2)
	}
	row.latestPacket = &delegatestore.TerminalPacket{
		Message:                json.RawMessage(`"` + strings.Repeat("m", activityMaxDelegatePayloadBytes) + `"`),
		StructuredResult:       json.RawMessage(`"` + strings.Repeat("s", activityMaxDelegatePayloadBytes) + `"`),
		StructuredResultReason: strings.Repeat("r", activityMaxDelegateProseRunes*2),
		Warnings:               warnings,
	}
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": row},
	}
	got := projectActivitySession(snap, newActivityBudget())
	delegate := got.Entries[0].Delegate
	if delegate.Message != nil {
		t.Fatalf("Message = %d bytes, want it omitted over the %d-byte cap", len(delegate.Message), activityMaxDelegatePayloadBytes)
	}
	if delegate.StructuredResult != nil {
		t.Fatalf("StructuredResult = %d bytes, want it omitted over the %d-byte cap", len(delegate.StructuredResult), activityMaxDelegatePayloadBytes)
	}
	if n := len([]rune(delegate.StructuredReason)); n > activityMaxDelegateProseRunes {
		t.Fatalf("StructuredReason = %d runes, want at most %d", n, activityMaxDelegateProseRunes)
	}
	if len(delegate.Warnings) != activityMaxDelegateWarnings+1 {
		t.Fatalf("Warnings = %d entries, want %d plus the omission note", len(delegate.Warnings), activityMaxDelegateWarnings)
	}
	if last := delegate.Warnings[len(delegate.Warnings)-1]; !strings.Contains(last, "more warnings omitted") {
		t.Fatalf("last warning = %q, want it to note the omitted ones", last)
	}
	found := false
	for _, diagnostic := range delegate.Diagnostics {
		if strings.Contains(diagnostic, "payload omitted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics %q, want one naming the omitted payload", delegate.Diagnostics)
	}
}

// TestProjectActivitySession_BoundsTheCollapsedOffenderIdentifier pins that
// the counted unsupported-type error cannot itself become fixed envelope
// content: the offender's ID and type are capped like every other projection
// string.
func TestProjectActivitySession_BoundsTheCollapsedOffenderIdentifier(t *testing.T) {
	t.Parallel()
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root",
		Jobs: []*jobstore.JobRecord{{
			JobID: strings.Repeat("j", 4<<10), Type: jobstore.JobType("unknown"),
			OwnerSessionID: "root", Status: jobstore.StatusRunning,
		}},
	}
	got := projectActivitySession(snap, newActivityBudget())
	if !strings.Contains(got.Branch.Error, "…") {
		t.Fatalf("branch error = %q, want the offender identifier capped", got.Branch.Error)
	}
	if len(got.Branch.Error) > 4*activityMaxLabelRunes {
		t.Fatalf("branch error is %d bytes, want it bounded by the identifier caps", len(got.Branch.Error))
	}
}

// TestProjectBoundedActivityTree_BoundsPacketPayloadsOnContinuationAncestors
// pins the ancestor-chain half of the payload caps. A continuation page carries
// its ancestor chain's delegates as fixed parts it cannot trim, so a page whose
// every ancestor bears an oversized terminal packet must still fit once those
// payloads are bounded.
func TestProjectBoundedActivityTree_BoundsPacketPayloadsOnContinuationAncestors(t *testing.T) {
	t.Parallel()
	const depth = 4
	oversized := activityMaxDelegatePayloadBytes * 4
	leaf := &activitySessionSnapshot{SessionID: "s3", Ref: "local:s3"}
	node := leaf
	for i := depth - 1; i >= 0; i-- {
		ownerID := fmt.Sprintf("s%d", i)
		childID := node.SessionID
		row := stableActivitySnapshot(fmt.Sprintf("dlg_%d", i), ownerID, childID, "brief")
		row.latestPacket = &delegatestore.TerminalPacket{
			Message:                json.RawMessage(`"` + strings.Repeat("m", oversized) + `"`),
			StructuredResult:       json.RawMessage(`"` + strings.Repeat("s", oversized) + `"`),
			StructuredResultReason: strings.Repeat("r", activityMaxDelegateProseRunes*4),
			Warnings:               []string{strings.Repeat("w", activityMaxDelegateWarningRunes*4)},
		}
		node = &activitySessionSnapshot{
			SessionID: ownerID, Ref: "local:" + ownerID, RootID: "root",
			StableDelegates: map[string]delegateSnapshot{row.id: row},
			Children:        map[string]*activitySessionSnapshot{childID: node},
		}
	}

	// startDepth = -depth is exactly how a continuation to the leaf is loaded:
	// every session above it is an ancestor the page cannot drop.
	got, err := projectBoundedActivityTree(*node, "root", -depth, 0, 0, time.Unix(10, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > activityMaxEncodedBytes {
		t.Fatalf("page = %d bytes, over the %d-byte limit", len(raw), activityMaxEncodedBytes)
	}
	session := &got.Root
	for i := range depth {
		if len(session.Entries) != 1 || session.Entries[0].Delegate == nil {
			t.Fatalf("ancestor %d has entries %+v, want one delegate", i, session.Entries)
		}
		delegate := session.Entries[0].Delegate
		if delegate.Message != nil || delegate.StructuredResult != nil {
			t.Fatalf("ancestor %d kept an oversized payload: message=%d result=%d", i, len(delegate.Message), len(delegate.StructuredResult))
		}
		if n := len([]rune(delegate.StructuredReason)); n > activityMaxDelegateProseRunes {
			t.Fatalf("ancestor %d StructuredReason = %d runes, want at most %d", i, n, activityMaxDelegateProseRunes)
		}
		if n := len(delegate.Warnings); n > activityMaxDelegateWarnings+1 {
			t.Fatalf("ancestor %d warnings = %d, want at most %d plus the note", i, n, activityMaxDelegateWarnings)
		}
		if delegate.Child == nil {
			t.Fatalf("ancestor %d has no child to descend into", i)
		}
		session = delegate.Child
	}
}

// TestProjectActivitySession_SingleUnsupportedTypeKeepsOriginalWording pins
// that the common one-offender case reads exactly as it always did rather
// than being reworded into the counted form.
func TestProjectActivitySession_SingleUnsupportedTypeKeepsOriginalWording(t *testing.T) {
	t.Parallel()
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root",
		Jobs: []*jobstore.JobRecord{{JobID: "j1", Type: jobstore.JobType("unknown"), OwnerSessionID: "root", Status: jobstore.StatusRunning}},
	}
	got := projectActivitySession(snap, newActivityBudget())
	want := fmt.Sprintf("job %q has unsupported type %q", "j1", jobstore.JobType("unknown"))
	if got.Branch.Error != want {
		t.Fatalf("branch error = %q, want %q", got.Branch.Error, want)
	}
}
