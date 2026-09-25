package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
)

const (
	activityMaxWorkUnits    = 2000
	activityMaxNewDepth     = 32
	activityMaxEncodedBytes = 4 << 20
	activityMaxTokenBytes   = 16 << 10
	// activityContinuationVersion is the continuation format this build
	// mints and the only one it accepts. It moves whenever the meaning of a
	// token's fields moves, because a token is checked field by field: a
	// version that stayed put while fields were added or repurposed would
	// let an older token be read as if it said something it never said. A
	// token from another version is refused as stale, so a client restarts
	// pagination once, rather than being decoded by some compatibility path
	// this build has no way to keep honest.
	activityContinuationVersion = 3
	// activityMaxContinuationPathLength bounds a client-supplied
	// continuation's Path at activityMaxNewDepth+1 rather than
	// activityMaxNewDepth. The two numbers measure different things: the
	// depth budget is relative to the page's target, which is projection's
	// own depth 0 however many hops led to it, while a minted Path is
	// absolute — counted from the tree's root through the filtered ancestor
	// chain. A page resumed len(Path) hops down can therefore reach
	// absolute depth len(Path)+activityMaxNewDepth, past this limit, so the
	// mint sites check the path they are about to name against it and
	// report the session to request instead of handing back a token this
	// decoder would refuse (activityContinuationPathFits).
	//
	// The extra hop over activityMaxNewDepth is not slack for old tokens —
	// those are refused on version before their length is ever read. It is
	// the longest path THIS build mints: a page resumed one hop down spends
	// the whole budget below its target and names an absolute path of
	// exactly activityMaxNewDepth+1, so a decoder capped at
	// activityMaxNewDepth would refuse a token this service had just
	// handed out. Anything past this length is refused, and the mint sites
	// report the session to request rather than producing one. Both sides
	// are pinned by
	// TestMarkActivitySessionTruncated_ReportsTheSessionWhenThePathCannotBeNamed:
	// a path at the limit still mints and still decodes, one hop past it
	// mints nothing.
	activityMaxContinuationPathLength = activityMaxNewDepth + 1
	// activitySkippedEntrySuffix completes a named skip diagnostic;
	// activitySkippedEntryShortMessage is what a page falls back to when it
	// cannot fit the name — see explainActivitySkippedEntry.
	activitySkippedEntrySuffix       = " is too large to render in one response and was skipped"
	activitySkippedEntryShortMessage = "one entry was too large to render and was skipped"
	// activityMaxLabelRunes and activityMaxDelegateProseRunes cap the
	// free-form text the activity projection copies out of a session's own
	// metadata or a delegate descriptor. Both are otherwise unbounded: a
	// session with no generated name labels itself with its OriginalPrompt
	// verbatim (a pasted prompt can be megabytes), and a delegate's
	// Task/Mandate/Description are whatever the spawner passed. They are the
	// response's FIXED parts — a label sits on every session and a
	// continuation page carries its ancestor chain's delegate metadata no
	// matter how the entries are trimmed — so an unbounded one goes out over
	// activityMaxEncodedBytes with nothing left to drop (see
	// markActivityEnvelopeTooLarge). The label cap mirrors the hub's own
	// sidebar title cap (hubcore.maxTitleRunes), which exists for the same
	// reason; the delegate cap is far above any ordinary brief yet bounds a
	// max-length (activityMaxNewDepth+1) ancestor chain's Task+Description to
	// a small fraction of the envelope.
	activityMaxLabelRunes         = 200
	activityMaxDelegateProseRunes = 4096
	// activityMaxDelegatePayloadBytes, activityMaxDelegateWarnings and
	// activityMaxDelegateWarningRunes bound the remaining free-form delegate
	// fields the projection copies verbatim: the raw terminal-packet payloads
	// (Message, StructuredResult) and the warnings list. They sit on each
	// delegate in a continuation page's ancestor chain, which the size trim
	// cannot drop, so an oversized one is a fixed part of the envelope. An
	// oversized payload is omitted rather than sliced — slicing would leave
	// invalid JSON on the wire — and remains available from the delegate's own
	// transcript.
	activityMaxDelegatePayloadBytes = 16 << 10
	activityMaxDelegateWarnings     = 8
	activityMaxDelegateWarningRunes = 512
	// activityAncestorTextFloorRunes is the smallest cap
	// shrinkActivityAncestors will apply to an ancestor's prose before it stops
	// cutting. Below this a field carries no useful text, so a page that still
	// will not fit is genuinely over the limit rather than merely wordy.
	activityAncestorTextFloorRunes = 64
)

// truncateActivityText caps s at maxRunes runes, appending an ellipsis when it
// truncates so a reader can tell a capped value from a genuinely short one.
// Rune-safe: never splits a multi-byte character.
//
// It walks runes only as far as the cap. Converting the whole string to a
// []rune first — as an earlier version did — allocates proportional to the
// INPUT (a 4 MiB label becoming a ~16 MiB slice) for a result that keeps at
// most maxRunes runes, which defeats the memory bound the cap exists to
// enforce.
func truncateActivityText(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	// Runes never outnumber bytes, so a string this short cannot need cutting
	// and does not have to be decoded.
	if len(s) <= maxRunes {
		return s
	}
	runeIndex := 0
	cutAt := -1
	for byteIndex := range s {
		if runeIndex == maxRunes-1 {
			cutAt = byteIndex
		}
		if runeIndex == maxRunes {
			// s[:cutAt] is the whole string short of maxRunes-1 runes.
			return s[:cutAt] + "…"
		}
		runeIndex++
	}
	// Multi-byte runes made the byte fast path conservative: the whole string
	// fits after all.
	return s
}

// boundActivityDelegatePayload clones a terminal-packet payload, or drops it
// when it alone would dominate the envelope. Dropping beats slicing: the field
// is json.RawMessage, so a cut would leave invalid JSON on the wire. The full
// payload stays available from the delegate's own transcript.
func boundActivityDelegatePayload(payload json.RawMessage) json.RawMessage {
	if len(payload) > activityMaxDelegatePayloadBytes {
		return nil
	}
	return append(json.RawMessage(nil), payload...)
}

// capActivityDelegateWarnings bounds both the number of warnings and the length
// of each so the list cannot dominate the envelope on an ancestor chain the
// page cannot trim. An over-long list ends with a counted note in place of the
// warnings it dropped.
func capActivityDelegateWarnings(warnings []string) []string {
	if len(warnings) == 0 {
		return nil
	}
	kept := min(len(warnings), activityMaxDelegateWarnings)
	capped := make([]string, 0, kept+1)
	for _, warning := range warnings[:kept] {
		capped = append(capped, truncateActivityText(warning, activityMaxDelegateWarningRunes))
	}
	if len(warnings) > kept {
		capped = append(capped, fmt.Sprintf("%d more warnings omitted", len(warnings)-kept))
	}
	return capped
}

// activityContinuation is a real, checked cursor position: resuming from
// one re-enters the exact session a prior page's mid-list cutoff stopped
// at (RootID/SessionID/Path) AND skips exactly the entries that session
// already rendered (ResumeIndex), rather than re-rendering that session's
// list from its own top with a fresh budget. JobsEpoch/
// DelegatesEpoch are the fold-cache generations (foldcache.Result.Epoch —
// see historicalJobFoldCache/historicalDelegateFoldCache in
// jobs_activity_past.go) SessionID's own jobs.jsonl and RootID's shared
// delegates.jsonl were at when this token was minted; loadActivitySnapshotForParams
// rejects a resume whose epochs no longer match what the fold caches
// currently report for those same paths, rather than silently applying
// ResumeIndex to a journal that was rewritten or shrunk out from under it.
// An ordinary append never bumps epoch (see foldcache.Result.Epoch's own
// doc comment) — append-only growth is exactly the case a resume must
// tolerate, not treat as staleness.
type activityContinuation struct {
	Version        int      `json:"v"`
	RootID         string   `json:"root"`
	SessionID      string   `json:"session"`
	Path           []string `json:"path"`
	ResumeIndex    int      `json:"idx,omitempty"`
	JobsEpoch      uint64   `json:"jobs_epoch,omitempty"`
	DelegatesEpoch uint64   `json:"dlg_epoch,omitempty"`
	// JobsAbsent and DelegatesAbsent record whether each journal existed at
	// all when this token was minted, which the generations above cannot
	// say: a journal that is not there reports 0, and so does one folded
	// for the first time. A resume compares them exactly as it compares the
	// generations, because a journal that appeared or went away moves the
	// entries ResumeIndex counts — a session renders its jobs and then its
	// delegates, so either journal changing presence renumbers the list.
	JobsAbsent      bool `json:"jobs_absent,omitempty"`
	DelegatesAbsent bool `json:"dlg_absent,omitempty"`
	// DelegatesUnreadable records that the delegate journal was there and
	// could not be read when this token was minted. That read folds
	// nothing and reports generation 0, the same number a journal read
	// once and never rewritten reports, so without this a token minted
	// over a readable journal resumes into a page whose delegate list is
	// empty — its position counting entries that are not there.
	DelegatesUnreadable bool `json:"dlg_unreadable,omitempty"`
	// Revision is the revision appwire.JobActivityTree reported for the page
	// this token was minted with — the live jobActivityClock revision for a
	// live root, or activitySnapshotPersistedRevision for a historical one —
	// carried into the continuation too, and echoed back as the resumed page's
	// own Revision (LoadSessionJobActivityTree). It is checked on resume only
	// when the root is LIVE (loadActivitySnapshotForParams): a live
	// session's JobsEpoch/DelegatesEpoch above are always 0 (it reads neither
	// fold cache), so they provide no staleness protection at all for a live
	// continuation — 0 == 0 always passes, even across a real mutation.
	// Revision closes that gap the same way epoch closes it for historical
	// sessions. A historical continuation is not validated on this field: the
	// epoch fields already say whether a resume position is safe, and echoing
	// the token's number back keeps a pagination walk's revision stable even
	// though the persisted max it was first computed from can move under it.
	Revision uint64 `json:"rev,omitempty"`
}

// activitySessionSnapshot is the lock-free input to the activity projection.
// Traversal owns constructing it; projection only consumes cloned job records.
type activitySessionSnapshot struct {
	SessionID       string
	Ref             string
	Label           string
	RootID          string
	Revision        uint64
	Jobs            []*jobstore.JobRecord
	LiveJobs        map[string]*jobstore.JobRecord
	StableDelegates map[string]delegateSnapshot
	Usage           *appwire.EvenerUsage // cumulative self-only tokens; nil = unknown
	Diagnostics     []string
	// JobsEpoch and DelegatesEpoch are historicalJobFoldCache's and
	// historicalDelegateFoldCache's current generation counters for this
	// session's own jobs.jsonl and the delegates.jsonl it folds,
	// respectively (0 for a LIVE session's own snapshot — loadLiveActivityBase
	// reads neither cache, and a live session's own data is always current,
	// never stale in the sense these caches guard against; a live root's
	// CLOSED children still carry their real ones). Carried into any
	// continuation that names THIS session — see activityContinuation,
	// markActivitySessionTruncated and collectActivitySessionEpochs.
	JobsEpoch      uint64
	DelegatesEpoch uint64
	// JobsJournalAbsent records that this session had no jobs.jsonl when the
	// fold looked, which JobsEpoch cannot say. The fold reports both facts
	// from one stat — generation 0 and Absent — and a journal folded for the
	// first time reports that same 0, so the generation alone cannot tell a
	// file that was never there from one read once and never rewritten. The
	// entries a session renders are its jobs and then its delegates, so a
	// journal that appears or disappears between two requests moves every
	// position a continuation counted — see activitySessionEpochs.
	JobsJournalAbsent bool
	// DelegatesJournalAbsent is the same fact for the delegates.jsonl this
	// session folds, read the same way from the same fold.
	DelegatesJournalAbsent bool
	// DelegatesJournalUnreadable records that the journal was there and
	// could not be read — a line past the scanner's cap, contained rather
	// than fatal (see scanRootDelegateState). That read folds nothing, so
	// the generation it reports is not one it earned, and this is what
	// separates it from a journal genuinely at generation 0.
	DelegatesJournalUnreadable bool
	Children                   map[string]*activitySessionSnapshot // child session ID
	Errors                     map[string]error                    // child session ID
}

type activitySessionLocator struct {
	live      *Session
	stateDir  string
	sessionID string
}

type activityLoadedBase struct {
	snapshot       activitySessionSnapshot
	directChildren map[string]*subagent
}

// activityBudget carries projection-local traversal state and optional response
// bounds. A zero-value budget remains the unlimited cycle guard Task 2 used.
type activityBudget struct {
	visiting     map[string]bool
	bounded      bool
	rootID       string
	maxWorkUnits int
	usedWork     int
	maxDepth     int
	now          time.Time
	// revision is the value markActivitySessionTruncated embeds as
	// activityContinuation.Revision — see that field's doc comment.
	revision uint64
}

func newActivityBudget() *activityBudget {
	return &activityBudget{visiting: make(map[string]bool)}
}

func newBoundedActivityBudget(rootID string, now time.Time, revision uint64) *activityBudget {
	return &activityBudget{
		visiting:     make(map[string]bool),
		bounded:      true,
		rootID:       rootID,
		maxWorkUnits: activityMaxWorkUnits,
		maxDepth:     activityMaxNewDepth,
		revision:     revision,
		now:          now,
	}
}

// activityStaleContinuationError is every rejection a client answers the
// same way: drop the token and request the session again. One shape for all
// of them, so a caller can recognise the class without matching on which
// particular fact stopped being true.
func activityStaleContinuationError(reason string) error {
	return fmt.Errorf("activity continuation is stale: %s; restart pagination without a continuation", reason)
}

func encodeActivityContinuation(cont activityContinuation) string {
	payload, err := json.Marshal(cont)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeActivityContinuation(token, expectedRoot string) (activityContinuation, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return activityContinuation{}, errors.New("empty continuation")
	}
	if len(token) > activityMaxTokenBytes {
		return activityContinuation{}, fmt.Errorf("continuation exceeds %d bytes", activityMaxTokenBytes)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return activityContinuation{}, fmt.Errorf("decode continuation: %w", err)
	}
	var cont activityContinuation
	if err := json.Unmarshal(raw, &cont); err != nil {
		return activityContinuation{}, fmt.Errorf("unmarshal continuation: %w", err)
	}
	if cont.Version != activityContinuationVersion {
		return activityContinuation{}, activityStaleContinuationError(fmt.Sprintf("format version %d is not the %d this build mints", cont.Version, activityContinuationVersion))
	}
	if cont.RootID == "" || cont.SessionID == "" {
		return activityContinuation{}, errors.New("continuation missing root or session")
	}
	// Continuation paths are client-controlled: without this, a long valid
	// path could force buildActivityContinuationAt
	// to open many historical sessions' files with no bound at all, since
	// ordinary (non-continuation) traversal's own depth limit
	// (activityMaxNewDepth) is enforced by buildActivityFullSnapshot's
	// recursion, which this path-following code doesn't go through. The
	// limit itself is activityMaxContinuationPathLength
	// (activityMaxNewDepth+1), not activityMaxNewDepth — see its doc
	// comment for why the extra hop is still accepted.
	if len(cont.Path) > activityMaxContinuationPathLength {
		return activityContinuation{}, fmt.Errorf("continuation path length %d exceeds %d", len(cont.Path), activityMaxContinuationPathLength)
	}
	if cont.ResumeIndex < 0 {
		return activityContinuation{}, fmt.Errorf("continuation resume index %d is negative", cont.ResumeIndex)
	}
	if expectedRoot != "" && cont.RootID != expectedRoot {
		return activityContinuation{}, fmt.Errorf("continuation root %q does not match %q", cont.RootID, expectedRoot)
	}
	if err := validIDToken(cont.RootID); err != nil {
		return activityContinuation{}, fmt.Errorf("invalid continuation root: %w", err)
	}
	if err := validIDToken(cont.SessionID); err != nil {
		return activityContinuation{}, fmt.Errorf("invalid continuation session: %w", err)
	}
	seen := make(map[string]bool, len(cont.Path))
	for _, hop := range cont.Path {
		if err := validIDToken(hop); err != nil {
			return activityContinuation{}, fmt.Errorf("invalid continuation path hop %q: %w", hop, err)
		}
		if seen[hop] {
			return activityContinuation{}, fmt.Errorf("duplicate continuation path hop %q", hop)
		}
		seen[hop] = true
	}
	cont.Path = append([]string(nil), cont.Path...)
	return cont, nil
}

// JobActivityTree builds the session's job-activity tree reported over
// appwire, after validating params' root reference against this session.
func (s *Session) JobActivityTree(params appwire.JobsListParams) (appwire.JobActivityTree, error) {
	if s == nil {
		return appwire.JobActivityTree{}, errors.New("session unavailable")
	}
	if err := validateActivityRootRef(params.Ref, s.ID()); err != nil {
		return appwire.JobActivityTree{}, err
	}
	root := activitySessionLocator{live: s, stateDir: s.stateDir, sessionID: s.ID()}
	now := s.sclock().Now().UTC()
	if s.jobActivityClock == nil {
		// context.Background() here is a real, known gap, not a soft one:
		// this root's OWN job list loads via loadLiveActivityBase ->
		// jm.store.LoadOrdered(), a completely separate, unbounded,
		// non-cancelable read on jobstore.Store's stateful, cursor-caching,
		// mutex-protected internal path (readAllLocked) — NOT the bounded
		// jobstore.ScanEvents this ctx would otherwise reach. Store.LoadOrdered
		// has one caller; retrofitting it with the same ScanLimits+ctx
		// ScanEvents uses would mean redesigning how its cursor-trust
		// invariants interact with a resumable, ceiling-respecting read,
		// inside code shared with the live append path — real, but out of
		// scope here; flagged for a follow-up. What context.Background()
		// DOES still reach: any already-exited descendant of this live
		// root, loaded through loadHistoricalActivityBase's bounded
		// scanners exactly like the hub's persisted fallback. Separately,
		// this path's own caller (cmd/evener/serve.go's SetJobsFunc hook)
		// takes no context to thread through in the first place, so even a
		// bounded LoadOrdered would only gain the byte/event ceilings
		// here, not cancellation.
		snapshot, startDepth, resumeIndex, err := loadActivitySnapshotForParams(context.Background(), root, params)
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		return projectBoundedActivityTree(*snapshot, root.sessionID, startDepth, resumeIndex, 0, now)
	}
	return projectStableLiveActivityTreeAt(s.jobActivityClock, root.sessionID, now, func() (*activitySessionSnapshot, int, int, error) {
		return loadActivitySnapshotForParams(context.Background(), root, params)
	})
}

func projectStableLiveActivityTree(clock *jobActivityClock, rootID string, load func() (*activitySessionSnapshot, int, int, error)) (appwire.JobActivityTree, error) {
	return projectStableLiveActivityTreeAt(clock, rootID, time.Now().UTC(), load)
}

func projectStableLiveActivityTreeAt(clock *jobActivityClock, rootID string, now time.Time, load func() (*activitySessionSnapshot, int, int, error)) (appwire.JobActivityTree, error) {
	for range 8 {
		before := activityCurrentRootRevision(clock)
		snapshot, startDepth, resumeIndex, err := load()
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		after := activityCurrentRootRevision(clock)
		if before == after {
			return projectBoundedActivityTree(*snapshot, rootID, startDepth, resumeIndex, after, now)
		}
	}
	return appwire.JobActivityTree{}, errors.New("activity tree changed while snapshot was being built; retry")
}

func activityCurrentRootRevision(clock *jobActivityClock) uint64 {
	if clock == nil {
		return 0
	}
	return clock.revision.Load()
}

func activityCurrentRootID(clock *jobActivityClock, fallback string) string {
	if clock != nil && strings.TrimSpace(clock.rootSessionID) != "" {
		return clock.rootSessionID
	}
	return strings.TrimSpace(fallback)
}

// loadActivitySnapshotForParams builds a fresh historicalActivityCache for
// ctx and threads it through the whole recursive load, so the shared root
// delegate journal is scanned once no matter how many sessions this one
// build visits, and a canceled ctx stops before opening a later session's
// files (checked in loadHistoricalActivityBase). It returns the resumeIndex
// a continuation's mid-list cutoff carried (0 for a fresh, non-continuation
// load), for projectBoundedActivityTree to apply against the target
// session's own entries.
//
// The cache is created here and never returned: it lives only for this one
// load, and every caller discards it. LoadSessionJobActivityTree used to
// reuse it for a second full-tree walk that recomputed a continuation's
// revision; that walk is gone (the page now echoes the token's revision), so
// there is nothing left for a caller to do with the cache.
func loadActivitySnapshotForParams(ctx context.Context, root activitySessionLocator, params appwire.JobsListParams) (*activitySessionSnapshot, int, int, error) {
	cache := newHistoricalActivityCache(ctx, root.sessionID)
	if strings.TrimSpace(params.Continuation) == "" {
		visited := map[string]bool{root.sessionID: true}
		snapshot, err := buildActivityFullSnapshot(root, visited, false, cache, 0)
		return snapshot, 0, 0, err
	}
	cont, err := decodeActivityContinuation(params.Continuation, root.sessionID)
	if err != nil {
		return nil, 0, 0, err
	}
	visited := map[string]bool{root.sessionID: true}
	snapshot, jobsEpoch, delegatesEpoch, err := buildActivityContinuationSnapshot(root, cont, visited, false, cache)
	if err != nil {
		return nil, 0, 0, err
	}
	// The generations here are the TARGET session's own — the session whose
	// journals a resume folds — and a mint carries that same session's, live
	// root or not (collectActivitySessionEpochs). An ordinary append never
	// moves either (see activityContinuation's doc comment), so this rejects
	// exactly the resume whose underlying journal was rewritten or shrunk
	// since, the case ResumeIndex is unsafe to apply to. A live session reads
	// neither fold cache, so its own generations are 0 on both sides and this
	// check is silent for it; the revision below is what fences a live
	// target. A live root paging into a CLOSED child is fenced by both: the
	// clock for what the daemon does under this root, these generations for a
	// rewrite of the child's journal that the clock never sees.
	// Presence travels beside the generations and is checked the same way,
	// because a generation cannot speak for a journal that is not there:
	// an absent one reports 0 and so does a journal folded for the first
	// time. A journal that appeared, or went away, renumbers the entries
	// ResumeIndex counts — which is also why a session whose journal was
	// never there still mints and still resumes, as long as it is still
	// not there.
	target := collectActivitySessionEpochs(*snapshot)[cont.SessionID]
	if cont.JobsEpoch != jobsEpoch || cont.DelegatesEpoch != delegatesEpoch ||
		cont.JobsAbsent != target.jobsAbsent || cont.DelegatesAbsent != target.delegatesAbsent ||
		cont.DelegatesUnreadable != target.delegatesUnreadable {
		return nil, 0, 0, activityStaleContinuationError("the underlying journal changed")
	}

	// A live session has no fold-cache generation at all (jobsEpoch and
	// delegatesEpoch above are both 0 for one — activitySessionSnapshot's
	// doc comment), so the check above cannot speak for a live target:
	// 0 == 0 passes across a real mutation. Revision closes that gap the
	// same way a generation closes it for a historical session: checked
	// against the SAME jobActivityClock
	// projectStableLiveActivityTreeAt's own before/after retry loop reads,
	// so any mutation between mint and resume — not just one within a
	// single request — is caught here.
	if root.live != nil {
		if current := activityCurrentRootRevision(root.live.jobActivityClock); cont.Revision != current {
			return nil, 0, 0, activityStaleContinuationError("the live session changed")
		}
	}
	return snapshot, -len(cont.Path), cont.ResumeIndex, nil
}

// buildActivityFullSnapshot loads loc's full subtree.
//
// depth is loc's own depth, explicit rather than derived from
// len(visited)-1: visited keeps growing across a continuation's whole hop
// chain for cycle detection, but projection resets ITS depth to 0 at the
// continuation target (loadActivitySnapshotForParams's startDepth =
// -len(cont.Path), reaching 0 exactly at the target — see
// decodeActivityContinuation). Deriving depth from len(visited)-1 would
// count the ancestor chain leading UP TO a continuation target as part of
// the target's own depth, so a max-length continuation could make the
// target's own children look already past activityMaxNewDepth and load
// them as empty placeholders instead of the next page, even though
// projection treats the target as a fresh depth-0 root. Every caller
// passes 0 for a session that is
// projection's own depth-0 node (an ordinary tree root, OR a continuation
// target reached via any number of hops); the recursive call below passes
// depth+1 for an actual child.
func buildActivityFullSnapshot(loc activitySessionLocator, visited map[string]bool, required bool, cache *historicalActivityCache, depth int) (*activitySessionSnapshot, error) {
	loaded, err := loadActivityBase(loc, required, cache)
	if err != nil {
		return nil, err
	}
	snapshot := loaded.snapshot
	snapshot.Children = make(map[string]*activitySessionSnapshot)
	snapshot.Errors = make(map[string]error)
	for _, delegateID := range sortedStableActivityDelegateIDs(snapshot.StableDelegates) {
		row := snapshot.StableDelegates[delegateID]
		childID, err := activityChildSessionForStable(row)
		if err != nil {
			key := delegateID
			if row.descriptor.ChildSessionID != "" {
				key = row.descriptor.ChildSessionID
			}
			snapshot.Errors[key] = err
			continue
		}
		if snapshot.Children[childID] != nil || snapshot.Errors[childID] != nil {
			continue
		}
		if visited[childID] {
			snapshot.Errors[childID] = errors.New("cycle detected")
			continue
		}
		// Mirrors activityMaxNewDepth/activityMaxWorkUnits here so a wide or
		// deep tree can't force unbounded loading (file opens, recursion,
		// decoding) before projection's own budget ever gets a chance to
		// apply. Past the bound nothing is loaded and nothing stands in for
		// the child: projection decides the branch is truncated before it
		// reaches for one, and reports the session to request instead of a
		// token it could not fence (projectStableActivityDelegate).
		if depth >= cache.budget.maxDepth {
			continue
		}
		if !activityConsumeWorkUnit(cache.budget, 1) {
			continue
		}
		childLoc, err := resolveActivityChildByID(loc, loaded, childID)
		if err != nil {
			snapshot.Errors[childID] = err
			continue
		}
		nextVisited := cloneActivityVisited(visited)
		nextVisited[childID] = true
		child, err := buildActivityFullSnapshot(childLoc, nextVisited, true, cache, depth+1)
		if err != nil {
			// A canceled request must surface as a real error all the way to
			// the caller, not be laundered into a per-child branch error
			// that leaves the parent — and ultimately
			// LoadSessionJobActivityTree — reporting success with a
			// silently missing subtree. Any OTHER per-child error
			// (corruption, a missing state dir, …) is recorded on this one
			// branch instead, and the walk keeps visiting siblings.
			if cache.ctx.Err() != nil {
				return nil, cache.ctx.Err()
			}
			snapshot.Errors[childID] = err
			continue
		}
		snapshot.Children[childID] = child
	}
	return &snapshot, nil
}

// buildActivityContinuationSnapshot re-enters the session cont's Path chain
// points at and returns its root-shaped, single-child-filtered snapshot for
// the wire ALONGSIDE the TARGET session's own JobsEpoch/DelegatesEpoch (not
// the outer root's; the target is what ResumeIndex was minted against and
// what it must be checked against on resume — see
// loadActivitySnapshotForParams).
func buildActivityContinuationSnapshot(loc activitySessionLocator, cont activityContinuation, visited map[string]bool, required bool, cache *historicalActivityCache) (*activitySessionSnapshot, uint64, uint64, error) {
	if len(cont.Path) == 0 {
		if loc.sessionID != cont.SessionID {
			return nil, 0, 0, fmt.Errorf("continuation session %q does not match root %q", cont.SessionID, loc.sessionID)
		}
		// No hops: loc IS the continuation target, projection's own
		// depth-0 node (see buildActivityFullSnapshot's doc comment).
		snapshot, err := buildActivityFullSnapshot(loc, visited, required, cache, 0)
		if err != nil {
			return nil, 0, 0, err
		}
		return snapshot, snapshot.JobsEpoch, snapshot.DelegatesEpoch, nil
	}
	return buildActivityContinuationAt(loc, cont, 0, visited, required, cache)
}

func buildActivityContinuationAt(loc activitySessionLocator, cont activityContinuation, hop int, visited map[string]bool, required bool, cache *historicalActivityCache) (*activitySessionSnapshot, uint64, uint64, error) {
	if hop == len(cont.Path) {
		if loc.sessionID != cont.SessionID {
			return nil, 0, 0, fmt.Errorf("continuation session %q does not match resolved path %q", cont.SessionID, loc.sessionID)
		}
		// loc is the continuation target: projection's own depth-0 node
		// (see buildActivityFullSnapshot's doc comment) regardless of how
		// many hops led here — NOT len(visited)-1, which would count the
		// whole ancestor chain as depth already consumed.
		snapshot, err := buildActivityFullSnapshot(loc, visited, required, cache, 0)
		if err != nil {
			return nil, 0, 0, err
		}
		return snapshot, snapshot.JobsEpoch, snapshot.DelegatesEpoch, nil
	}
	// Charge this hop against the shared load budget the same way
	// buildActivityFullSnapshot charges each child it visits:
	// decodeActivityContinuation's path-length cap bounds how many hops a
	// SINGLE continuation can name, but says nothing about how much of the
	// tree-wide load budget resolving them consumes -- loadActivityBase
	// below opens files, so without this check a long path would consume
	// that budget unconditionally, once per hop, with no bound of its own.
	if !activityConsumeWorkUnit(cache.budget, 1) {
		return nil, 0, 0, errors.New("continuation path exhausted the load budget")
	}
	loaded, err := loadActivityBase(loc, required, cache)
	if err != nil {
		return nil, 0, 0, err
	}
	delegateID := cont.Path[hop]
	if row, ok := loaded.snapshot.StableDelegates[delegateID]; ok {
		childID, err := activityChildSessionForStable(row)
		if err != nil {
			return nil, 0, 0, err
		}
		if visited[childID] {
			return nil, 0, 0, errors.New("cycle detected")
		}
		childLoc, err := resolveActivityChildByID(loc, loaded, childID)
		if err != nil {
			return nil, 0, 0, err
		}
		nextVisited := cloneActivityVisited(visited)
		nextVisited[childID] = true
		child, jobsEpoch, delegatesEpoch, err := buildActivityContinuationAt(childLoc, cont, hop+1, nextVisited, true, cache)
		if err != nil {
			return nil, 0, 0, err
		}
		filtered := activityFilterSnapshotToDelegate(loaded.snapshot, delegateID, child)
		return &filtered, jobsEpoch, delegatesEpoch, nil
	}
	return nil, 0, 0, fmt.Errorf("continuation path hop %q not found", delegateID)
}

func loadActivityBase(loc activitySessionLocator, required bool, cache *historicalActivityCache) (activityLoadedBase, error) {
	if loc.live != nil {
		return loadLiveActivityBase(loc.live)
	}
	return loadHistoricalActivityBase(loc.stateDir, loc.sessionID, required, cache)
}

func loadLiveActivityBase(s *Session) (activityLoadedBase, error) {
	if s == nil {
		return activityLoadedBase{}, errors.New("session unavailable")
	}
	snapshot := activitySessionSnapshot{
		SessionID:       s.ID(),
		Ref:             encodeRef("", s.ID()),
		Label:           liveActivitySessionLabel(s),
		RootID:          activityCurrentRootID(s.jobActivityClock, s.ID()),
		Revision:        activityCurrentRootRevision(s.jobActivityClock),
		Jobs:            []*jobstore.JobRecord{},
		LiveJobs:        map[string]*jobstore.JobRecord{},
		StableDelegates: map[string]delegateSnapshot{},
	}
	if s.delegateController != nil {
		for _, row := range s.delegateController.Snapshot().rows {
			if row.descriptor.OwnerSessionID == s.ID() {
				snapshot.StableDelegates[row.id] = row
			}
		}
	}
	snapshot.Usage = appwire.EvenerUsageFromLLM(s.CumulativeUsageSnapshot())
	jm, err := sessionJobManager(s)
	if err == nil && jm != nil && jm.store != nil {
		ordered, err := jm.store.LoadOrdered()
		if err != nil {
			return activityLoadedBase{}, err
		}
		snapshot.Jobs = ordered
		snapshot.LiveJobs = jm.liveJobRecords()
	}
	children := make(map[string]*subagent)
	if s.subagents != nil {
		for _, sub := range s.subagents.directSubagents() {
			if sub == nil {
				continue
			}
			children[sub.id] = sub
		}
	}
	return activityLoadedBase{snapshot: snapshot, directChildren: children}, nil
}

func liveActivitySessionLabel(s *Session) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	id := s.id
	name := s.naming.value
	prompt := s.cfg.spawn.subagentTask
	for _, turn := range s.history {
		if turn.Kind == schema.TurnUserInput {
			// The projection the label must match: an image paste appends a
			// machinery note to the user turn, and the label carries the
			// user's prose — never the note — like every other user-facing
			// surface (bubbles, fork prefill, metadata).
			prompt = apptranscript.UserFacingText(turn.Message)
			break
		}
	}
	s.mu.Unlock()
	return activitySessionLabel(schema.SessionMeta{ID: id, Name: name, OriginalPrompt: prompt})
}

func resolveActivityChildByID(parent activitySessionLocator, loaded activityLoadedBase, childID string) (activitySessionLocator, error) {
	if sub := loaded.directChildren[childID]; sub != nil && sub.sess != nil {
		sub.mu.Lock()
		closed := sub.closed
		sub.mu.Unlock()
		if !closed {
			if !sameActivityStateDir(parent.stateDir, sub.sess.StateDir()) {
				return activitySessionLocator{}, fmt.Errorf("child session %q crosses state directory boundary", childID)
			}
			return activitySessionLocator{live: sub.sess, stateDir: parent.stateDir, sessionID: childID}, nil
		}
	}
	if strings.TrimSpace(parent.stateDir) == "" {
		return activitySessionLocator{}, fmt.Errorf("child session %q unavailable: no state directory", childID)
	}
	return activitySessionLocator{stateDir: parent.stateDir, sessionID: childID}, nil
}

func sameActivityStateDir(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return left == right
	}
	return canonicalOrClean(left) == canonicalOrClean(right)
}

func activitySessionLabel(meta schema.SessionMeta) string {
	if label := strings.TrimSpace(schema.SessionDisplayName(meta)); label != "" {
		return label
	}
	return strings.TrimSpace(meta.ID)
}

func activityChildSessionForStable(row delegateSnapshot) (string, error) {
	descriptor := row.descriptor
	if descriptor.ChildSessionID == "" || descriptor.TranscriptRef == "" {
		return "", fmt.Errorf("delegate %q has an incomplete child link", row.id)
	}
	projectID, childID, err := decodeRef(descriptor.TranscriptRef)
	if err != nil {
		return "", err
	}
	if projectID != "" {
		return "", fmt.Errorf("transcript ref %q crosses state directory boundary", descriptor.TranscriptRef)
	}
	if childID != descriptor.ChildSessionID {
		return "", fmt.Errorf("delegate %q child link does not match durable child session", row.id)
	}
	return childID, nil
}

func sortedStableActivityDelegateIDs(records map[string]delegateSnapshot) []string {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func cloneActivityVisited(visited map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(visited))
	maps.Copy(clone, visited)
	return clone
}

// activityFilterSnapshotToDelegate narrows base to the one delegate a
// continuation's path goes through, keeping the ancestor as a chain to
// re-descend rather than a subtree to re-render. base's fold-cache
// generations come along: a continuation minted anywhere on the resumed
// page is checked against the generations the NEXT request reads for these
// same journals, so filtering them away mints zeros that read as a rewrite
// that never happened.
func activityFilterSnapshotToDelegate(base activitySessionSnapshot, delegateID string, child *activitySessionSnapshot) activitySessionSnapshot {
	filtered := activitySessionSnapshot{
		SessionID:      base.SessionID,
		Ref:            base.Ref,
		Label:          base.Label,
		RootID:         base.RootID,
		Revision:       base.Revision,
		JobsEpoch:      base.JobsEpoch,
		DelegatesEpoch: base.DelegatesEpoch,

		JobsJournalAbsent:          base.JobsJournalAbsent,
		DelegatesJournalAbsent:     base.DelegatesJournalAbsent,
		DelegatesJournalUnreadable: base.DelegatesJournalUnreadable,
		Jobs:                       []*jobstore.JobRecord{},
		LiveJobs:                   map[string]*jobstore.JobRecord{},
		StableDelegates:            make(map[string]delegateSnapshot, 1),
		Diagnostics:                append([]string(nil), base.Diagnostics...),
		Children:                   make(map[string]*activitySessionSnapshot),
		Errors:                     make(map[string]error),
	}
	if row, ok := base.StableDelegates[delegateID]; ok {
		filtered.StableDelegates[delegateID] = row
		if child != nil && row.descriptor.ChildSessionID != "" {
			filtered.Children[row.descriptor.ChildSessionID] = child
		}
	}
	return filtered
}

func projectBoundedActivityTree(snapshot activitySessionSnapshot, rootID string, startDepth, resumeIndex int, revision uint64, now time.Time) (appwire.JobActivityTree, error) {
	budget := newBoundedActivityBudget(rootID, now, revision)
	root := projectActivitySessionAt(snapshot, budget, startDepth, nil, resumeIndex)
	tree := appwire.JobActivityTree{Revision: revision, Root: root}
	// Collected from snapshot (the internal tree projection just consumed)
	// before trimming works purely on the flattened wire shape, which
	// carries no generation information of its own — see
	// collectActivitySessionEpochs and trimActivityTrailingEntry. revision is
	// the same value just seeded into budget.revision above, embedded the
	// same way in whatever continuation trimming mints too.
	// startDepth is -len(continuation.Path) (loadActivitySnapshotForParams),
	// so -startDepth is the number of delegate hops down to the session
	// resumeIndex was applied to — the position trimming has to add back to
	// mint in that session's own entry numbering rather than this page's.
	resume := activityTrimResume{depth: -startDepth, index: resumeIndex}
	return trimActivityTreeToFit(tree, rootID, collectActivitySessionEpochs(snapshot), revision, resume)
}

// activitySessionEpochs is one session's own fold-cache generations: the
// generation of its jobs.jsonl and of the delegates.jsonl it folds. A
// continuation is checked against the generations of the session it TARGETS
// (loadActivitySnapshotForParams), so that is what a mint for that
// session has to carry — the page root's own are only right for a token
// naming the page root.
type activitySessionEpochs struct {
	jobs      uint64
	delegates uint64
	// jobsAbsent is activitySessionSnapshot.JobsJournalAbsent, carried
	// alongside the generations because it is the one thing they cannot
	// say: a journal that was never there and a journal folded for the
	// first time both report 0.
	jobsAbsent          bool
	delegatesAbsent     bool
	delegatesUnreadable bool
}

// epochs is everything a continuation naming this session has to record
// about its journals. Every mint goes through here rather than assembling
// the struct itself: a mint site that leaves a field at its zero value
// claims a state the session is not in, and the resume that compares it
// then refuses a token nothing invalidated — for a condition that does not
// change between requests, forever.
func (s activitySessionSnapshot) epochs() activitySessionEpochs {
	return activitySessionEpochs{
		jobs:                s.JobsEpoch,
		delegates:           s.DelegatesEpoch,
		jobsAbsent:          s.JobsJournalAbsent,
		delegatesAbsent:     s.DelegatesJournalAbsent,
		delegatesUnreadable: s.DelegatesJournalUnreadable,
	}
}

// collectActivitySessionEpochs walks snapshot's Children tree and returns
// every visited session's own generations, keyed by SessionID.
// trimActivityTrailingEntry needs this because it operates AFTER projection
// has already flattened the internal snapshot tree to its wire
// (appwire.JobActivitySession) shape, which has no generation fields of its
// own.
func collectActivitySessionEpochs(snapshot activitySessionSnapshot) map[string]activitySessionEpochs {
	epochs := make(map[string]activitySessionEpochs)
	var walk func(s activitySessionSnapshot)
	walk = func(s activitySessionSnapshot) {
		epochs[s.SessionID] = s.epochs()
		for _, child := range s.Children {
			if child != nil {
				walk(*child)
			}
		}
	}
	walk(snapshot)
	return epochs
}

func activitySnapshotPersistedRevision(snapshot *activitySessionSnapshot, rootID string) uint64 {
	if snapshot == nil {
		return 0
	}
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		rootID = strings.TrimSpace(snapshot.RootID)
		if rootID == "" {
			rootID = strings.TrimSpace(snapshot.SessionID)
		}
	}
	var maxRevision uint64
	var walk func(*activitySessionSnapshot)
	walk = func(node *activitySessionSnapshot) {
		if node == nil {
			return
		}
		nodeRoot := strings.TrimSpace(node.RootID)
		if (nodeRoot == "" || rootID == "" || nodeRoot == rootID) && node.Revision > maxRevision {
			maxRevision = node.Revision
		}
		for _, row := range node.StableDelegates {
			if row.revision > maxRevision {
				maxRevision = row.revision
			}
		}
		for _, childID := range sortedActivityChildIDs(node.Children) {
			walk(node.Children[childID])
		}
	}
	walk(snapshot)
	return maxRevision
}

func sortedActivityChildIDs(children map[string]*activitySessionSnapshot) []string {
	ids := make([]string, 0, len(children))
	for id := range children {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// mergeActivityRecords overlays live records on durable history without
// mutating either input. Durable records retain their append positions. Jobs
// visible only in the live map are inserted by (StartedAt, JobID).
func mergeActivityRecords(durable []*jobstore.JobRecord, live map[string]*jobstore.JobRecord) []*jobstore.JobRecord {
	// One index per durable job: its position in first-appearance order, with
	// a later duplicate replacing the record in place. A history of hundreds
	// of thousands of jobs is merged on every page a jobs list serves, so this
	// keeps it to a single map operation per durable record.
	durableIndex := make(map[string]int, len(durable))
	ordered := make([]*jobstore.JobRecord, 0, len(durable))
	for _, rec := range durable {
		if rec == nil || rec.JobID == "" {
			continue
		}
		if at, seen := durableIndex[rec.JobID]; seen {
			ordered[at] = rec
			continue
		}
		durableIndex[rec.JobID] = len(ordered)
		ordered = append(ordered, rec)
	}

	liveByID := make(map[string]*jobstore.JobRecord, len(live))
	liveKeys := make([]string, 0, len(live))
	for key := range live {
		liveKeys = append(liveKeys, key)
	}
	sort.Strings(liveKeys)
	for _, key := range liveKeys {
		rec := live[key]
		if rec == nil || rec.JobID == "" {
			continue
		}
		if _, seen := liveByID[rec.JobID]; !seen {
			liveByID[rec.JobID] = rec
		}
	}

	merged := make([]*jobstore.JobRecord, 0, len(ordered)+len(liveByID))
	for _, rec := range ordered {
		if len(liveByID) > 0 {
			if liveRec := liveByID[rec.JobID]; liveRec != nil {
				rec = liveRec
			}
		}
		merged = append(merged, cloneActivityRecord(rec))
	}

	liveOnly := make([]*jobstore.JobRecord, 0, len(liveByID))
	for jobID, rec := range liveByID {
		if _, durable := durableIndex[jobID]; !durable {
			liveOnly = append(liveOnly, cloneActivityRecord(rec))
		}
	}
	sort.Slice(liveOnly, func(i, j int) bool {
		return activityRecordBefore(liveOnly[i], liveOnly[j])
	})
	for _, rec := range liveOnly {
		at := len(merged)
		for i, current := range merged {
			if activityRecordBefore(rec, current) {
				at = i
				break
			}
		}
		merged = append(merged, nil)
		copy(merged[at+1:], merged[at:])
		merged[at] = rec
	}
	return merged
}

func cloneActivityRecord(rec *jobstore.JobRecord) *jobstore.JobRecord {
	if rec == nil {
		return nil
	}
	clone := *rec
	if rec.EndedAt != nil {
		ended := *rec.EndedAt
		clone.EndedAt = &ended
	}
	if rec.ExitCode != nil {
		exit := *rec.ExitCode
		clone.ExitCode = &exit
	}
	if rec.LastActivity != nil {
		last := *rec.LastActivity
		clone.LastActivity = &last
	}
	return &clone
}

func activityRecordBefore(left, right *jobstore.JobRecord) bool {
	if left.StartedAt.Equal(right.StartedAt) {
		return left.JobID < right.JobID
	}
	return left.StartedAt.Before(right.StartedAt)
}

func projectActivitySession(snapshot activitySessionSnapshot, budget *activityBudget) appwire.JobActivitySession {
	return projectActivitySessionAt(snapshot, budget, 0, nil, 0)
}

// projectActivitySessionAt renders snapshot's own entries (its owned shell
// jobs, then its stable delegates — the fixed order every entryIndex below
// counts against) into one appwire.JobActivitySession, recursing into each
// delegate's child subtree. resumeIndex skips that many leading entries
// without consuming budget or rendering them: a continuation minted by
// markActivitySessionTruncated below carries exactly this position, so a
// resumed page picks up where the truncated one left off instead of
// re-rendering snapshot's list from the top with a fresh budget.
//
// resumeIndex is meaningful only for the ONE session a continuation names —
// but for a continuation whose Path has hops (the truncated session was a
// nested delegate, not the tree's own root), that session is reached only
// after buildActivityContinuationAt's filtered ancestor chain is
// re-descended through len(Path) recursive projectStableActivityDelegate
// calls, not at THIS function's own top-level call. depth carries exactly
// that position already (loadActivitySnapshotForParams sets the top-level
// call's startDepth to -len(cont.Path), so depth reaches 0 exactly once,
// at the same node a plain, non-continuation load would call depth 0 — its
// own root, or here, the continuation's target session) — so resumeIndex
// is threaded UNCHANGED through every recursive call below and applied only
// where depth == 0, rather than being reset to 0 for children: a
// zero-hop continuation (target IS the root) needs it applied at THIS
// call's depth 0 too, which passing 0 to every recursive call would miss
// whenever depth's OWN starting point was already negative.
func projectActivitySessionAt(snapshot activitySessionSnapshot, budget *activityBudget, depth int, path []string, resumeIndex int) appwire.JobActivitySession {
	if budget == nil {
		budget = newActivityBudget()
	}
	effectiveResumeIndex := 0
	if depth == 0 {
		effectiveResumeIndex = resumeIndex
	}
	projected := appwire.JobActivitySession{
		SessionID:   snapshot.SessionID,
		Ref:         snapshot.Ref,
		Label:       truncateActivityText(snapshot.Label, activityMaxLabelRunes),
		Entries:     make([]appwire.JobActivityEntry, 0),
		Diagnostics: append([]string(nil), snapshot.Diagnostics...),
	}

	cycleKey := snapshot.SessionID + "\x00" + snapshot.Ref
	if budget.visiting == nil {
		budget.visiting = make(map[string]bool)
	}
	if budget.visiting[cycleKey] {
		projected.Branch.Error = fmt.Sprintf("activity cycle at session %q", snapshot.SessionID)
		projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
		return projected
	}
	budget.visiting[cycleKey] = true
	defer delete(budget.visiting, cycleKey)

	entryIndex := 0
	records := activityOwnedRecords(snapshot.SessionID, mergeActivityRecords(snapshot.Jobs, snapshot.LiveJobs))
	// Reported once for the whole list, counted: one sentence per record would
	// make Branch.Error grow with the journal, an unbounded envelope input. It
	// is hoisted out of the loop so an early truncation return cannot make a
	// page's error depend on how far it got.
	if message := unsupportedActivityJobTypesError(records); message != "" {
		appendActivityBranchError(&projected.Branch, message)
	}
	for _, rec := range records {
		// Unsupported records (anything but a shell job) are counted once for
		// the whole list by unsupportedActivityJobTypesError above, not
		// rendered and not re-reported here.
		if rec == nil || rec.Type != jobstore.JobShell {
			continue
		}
		if entryIndex < effectiveResumeIndex {
			entryIndex++
			continue
		}
		if !activityConsumeWorkUnit(budget, 1) {
			markActivitySessionTruncated(&projected, budget, snapshot.SessionID, path, entryIndex, snapshot.epochs())
			projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
			return projected
		}
		entryIndex++
		job := projectActivityJob(rec, snapshot.Ref)
		projected.Entries = append(projected.Entries, appwire.JobActivityEntry{Kind: "shell", Job: &job})
	}
	for _, delegateID := range sortedStableActivityDelegateIDs(snapshot.StableDelegates) {
		if entryIndex < effectiveResumeIndex {
			entryIndex++
			continue
		}
		if !activityConsumeWorkUnit(budget, 1) {
			markActivitySessionTruncated(&projected, budget, snapshot.SessionID, path, entryIndex, snapshot.epochs())
			projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
			return projected
		}
		entryIndex++
		delegate := projectStableActivityDelegate(snapshot, snapshot.StableDelegates[delegateID], budget, depth, path, resumeIndex)
		projected.Entries = append(projected.Entries, appwire.JobActivityEntry{Kind: "delegate", Delegate: &delegate})
	}

	projected.Counts, projected.Aggregate = aggregateActivity(projected.Entries, projected.Branch)
	return projected
}

func projectStableActivityDelegate(snapshot activitySessionSnapshot, row delegateSnapshot, budget *activityBudget, depth int, path []string, resumeIndex int) appwire.JobActivityDelegate {
	descriptor := row.descriptor
	status := projectStableDelegateStatus(budget.now, row)
	// Mandate and Task carry the same text; truncate it once rather than
	// repeating the work for each copy.
	task := truncateActivityText(descriptor.Task, activityMaxDelegateProseRunes)
	delegate := appwire.JobActivityDelegate{
		DelegateID:          row.id,
		OwnerSessionID:      descriptor.OwnerSessionID,
		RootSessionID:       snapshot.RootID,
		ChildSessionID:      descriptor.ChildSessionID,
		ChildRef:            descriptor.TranscriptRef,
		ParentDelegateID:    descriptor.ParentDelegateID,
		Type:                "delegate",
		Lifecycle:           string(row.lifecycle),
		Phase:               string(row.phase),
		Status:              string(row.lifecycle),
		ProjectionRevision:  row.revision,
		Resumable:           row.resumable,
		NotResumableReason:  truncateActivityText(row.notResumableReason, activityMaxDelegateProseRunes),
		Mandate:             task,
		Task:                task,
		Description:         truncateActivityText(descriptor.Description, activityMaxDelegateProseRunes),
		AgentType:           descriptor.AgentType,
		RequestedModel:      descriptor.RequestedModel,
		ResolvedProfileID:   descriptor.ResolvedProfileID,
		ResolvedModel:       descriptor.ResolvedModel,
		Model:               descriptor.ResolvedModel,
		ReasoningEffort:     descriptor.Config.ReasoningEffort,
		OriginTurnID:        truncateActivityText(descriptor.OriginTurnID, activityMaxLabelRunes),
		OriginToolCallID:    truncateActivityText(descriptor.OriginToolCallID, activityMaxLabelRunes),
		OriginItemID:        truncateActivityText(descriptor.OriginItemID, activityMaxLabelRunes),
		RunStartedAt:        status.RunStartedAt,
		LatestActivityAt:    status.LatestActivityAt,
		RunningForMS:        cloneInt64(status.RunningForMS),
		QuietForMS:          cloneInt64(status.QuietForMS),
		DurationMS:          cloneInt64(status.DurationMS),
		DelegationAllowance: descriptor.DelegationAllowance,
		ParentWatchGranted:  descriptor.ParentWatchGranted,
		Turns:               []appwire.JobActivityJob{},
	}
	if row.lastOutcome != nil {
		delegate.Outcome = string(row.lastOutcome.Status)
		delegate.Reason = truncateActivityText(row.lastOutcome.Reason, activityMaxDelegateProseRunes)
		delegate.Terminal = !row.currentRunOpen
		if !row.lastOutcome.EndedAt.IsZero() {
			delegate.RunEndedAt = row.lastOutcome.EndedAt.UTC().Format(time.RFC3339Nano)
		}
		delegate.ExhaustionBudget = string(row.lastOutcome.ExhaustionBudget)
		delegate.ExhaustionLimit = row.lastOutcome.ExhaustionLimit
		if row.lastOutcome.Resumable != nil {
			resumable := *row.lastOutcome.Resumable
			delegate.ExhaustionResumable = &resumable
		}
	}
	if packet := row.latestPacket; packet != nil {
		delegate.PacketKind = string(packet.Kind)
		delegate.Message = boundActivityDelegatePayload(packet.Message)
		delegate.StructuredResult = boundActivityDelegatePayload(packet.StructuredResult)
		delegate.StructuredReason = truncateActivityText(packet.StructuredResultReason, activityMaxDelegateProseRunes)
		delegate.Warnings = capActivityDelegateWarnings(packet.Warnings)
		messageDropped := len(packet.Message) > activityMaxDelegatePayloadBytes
		resultDropped := len(packet.StructuredResult) > activityMaxDelegatePayloadBytes
		if messageDropped || resultDropped {
			delegate.Diagnostics = append(delegate.Diagnostics, "terminal packet payload omitted: too large for the activity envelope")
		}
		if packet.StructuredResultValid != nil {
			valid := *packet.StructuredResultValid
			delegate.StructuredValid = &valid
		}
		if len(packet.Metadata) != 0 {
			var metadata delegateTerminalPacketMetadata
			if err := json.Unmarshal(packet.Metadata, &metadata); err != nil {
				appendActivityBranchError(&delegate.Branch, "delegate terminal metadata is invalid")
				delegate.Diagnostics = append(delegate.Diagnostics, "delegate terminal metadata is invalid")
			} else {
				delegate.Usage = activityUsageFromCumulative(metadata.CumulativeUsage)
				if metadata.Worktree != nil {
					delegate.Worktree = &appwire.JobActivityWorktree{
						Path: metadata.Worktree.Path, Branch: metadata.Worktree.Branch,
						HeadSHA: metadata.Worktree.HeadSHA, Ahead: metadata.Worktree.Ahead, Dirty: metadata.Worktree.Dirty,
					}
				}
			}
		}
	}
	childID, err := activityChildSessionForStable(row)
	if err != nil {
		appendActivityBranchError(&delegate.Branch, err.Error())
		return delegate
	}
	if err := snapshot.Errors[childID]; err != nil {
		appendActivityBranchError(&delegate.Branch, err.Error())
		return delegate
	}
	if budget != nil && budget.bounded && depth >= budget.maxDepth {
		// The bound stops here, and the branch says so and says where to
		// read it. A continuation would carry nothing a request for the
		// child itself does not: a depth-truncated token names that child as
		// a fresh root at position 0, so the page it fetches is the page a
		// direct request fetches — with generations this page cannot
		// honestly name, since nothing here loaded that child's journals.
		// Turning this diagnostic into an affordance the reader can click is
		// frontend work, tracked in #1270.
		delegate.Branch.Truncated = true
		delegate.Diagnostics = append(delegate.Diagnostics, fmt.Sprintf("depth limit reached; request session %q directly", childID))
		return delegate
	}
	child := snapshot.Children[childID]
	if child == nil {
		appendActivityBranchError(&delegate.Branch, fmt.Sprintf("child session %q unavailable", childID))
		return delegate
	}
	if child.SessionID != childID || child.Ref != descriptor.TranscriptRef {
		appendActivityBranchError(&delegate.Branch, fmt.Sprintf("delegate %q child link does not match loaded session", row.id))
		return delegate
	}
	if delegate.Usage == nil && child.Usage != nil {
		usage := *child.Usage
		delegate.Usage = &usage
	}
	childPath := appendActivityPath(path, row.id)
	projectedChild := projectActivitySessionAt(*child, budget, depth+1, childPath, resumeIndex)
	delegate.Child = &projectedChild
	return delegate
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func activityUsageFromCumulative(usage *schema.CumulativeUsage) *appwire.EvenerUsage {
	if usage == nil || *usage == (schema.CumulativeUsage{}) {
		return nil
	}
	return &appwire.EvenerUsage{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadTokens: usage.CacheReadTokens, TotalTokens: usage.TotalTokens,
	}
}

func activityOwnedRecords(sessionID string, records []*jobstore.JobRecord) []*jobstore.JobRecord {
	if sessionID == "" || len(records) == 0 {
		return records
	}
	owned := make([]*jobstore.JobRecord, 0, len(records))
	for _, rec := range records {
		if rec == nil {
			continue
		}
		if rec.OwnerSessionID != "" && rec.OwnerSessionID != sessionID && rec.Authority != jobstore.AuthorityForwardedFallback && rec.Authority != jobstore.AuthorityLegacyUnknown {
			continue
		}
		owned = append(owned, rec)
	}
	return owned
}

func activityConsumeWorkUnit(budget *activityBudget, units int) bool {
	if budget == nil || !budget.bounded {
		return true
	}
	if units < 0 {
		units = 0
	}
	if budget.usedWork+units > budget.maxWorkUnits {
		return false
	}
	budget.usedWork += units
	return true
}

// markActivitySessionTruncated marks a mid-list cutoff within sessionID's
// own entries (jobs then delegates, projectActivitySessionAt's iteration
// order) and mints a REAL, advancing continuation: resumeIndex is how many
// of sessionID's entries have now been accounted for across every page up
// to and including this one (projectActivitySessionAt's entryIndex at the
// moment the budget ran out), so a resume picks up exactly where this page
// stopped rather than re-rendering from the top. That is only possible
// because folding through historicalJobFoldCache/historicalDelegateFoldCache
// lets a resumed load pick up this session's own journal from where it
// last read it, instead of rescanning it from byte zero with a fresh
// budget. jobsEpoch/delegatesEpoch are the fold-cache generations (see
// activityContinuation) sessionID's own jobs.jsonl and the root's shared
// delegates.jsonl were at when this snapshot was built — carried in the
// continuation so a later resume can detect a rewrite that invalidates
// resumeIndex's meaning rather than silently applying it to different data
// (checked in loadActivitySnapshotForParams).
//
// A load-phase truncation can never coincide with this projection-phase
// one on the same session: loadHistoricalActivityBase always either loads
// a session's full history through the fold caches or fails loudly
// (ErrLineTooLong), never silently degrades to a partial snapshot that
// could combine with a projection-phase budget trip into a non-advancing
// continuation.
func markActivitySessionTruncated(session *appwire.JobActivitySession, budget *activityBudget, sessionID string, path []string, resumeIndex int, own activitySessionEpochs) {
	if session == nil {
		return
	}
	session.Branch.Truncated = true
	if !activityContinuationPathFits(path) {
		appendActivityDiagnosticOnce(&session.Diagnostics, activityUnreachableByPathDiagnostic(sessionID))
		return
	}
	if budget != nil && budget.rootID != "" {
		session.Branch.Continuation = encodeActivityContinuation(activityContinuation{
			Version:             activityContinuationVersion,
			RootID:              budget.rootID,
			SessionID:           sessionID,
			Path:                append([]string(nil), path...),
			ResumeIndex:         resumeIndex,
			JobsEpoch:           own.jobs,
			DelegatesEpoch:      own.delegates,
			JobsAbsent:          own.jobsAbsent,
			DelegatesAbsent:     own.delegatesAbsent,
			DelegatesUnreadable: own.delegatesUnreadable,
			Revision:            budget.revision,
		})
	}
}

// appendActivityDiagnosticOnce adds message to diagnostics unless it is
// already there. A trim drops one entry per pass and can strike the same
// session many times over a single page, and a session cut off mid-list can
// be marked by more than one of those passes, so an unguarded append reports
// the same sentence once per dropped entry.
func appendActivityDiagnosticOnce(diagnostics *[]string, message string) {
	if diagnostics == nil || slices.Contains(*diagnostics, message) {
		return
	}
	*diagnostics = append(*diagnostics, message)
}

// activityContinuationPathFits reports whether a continuation naming this
// path could be decoded at all. The mint sites ask before encoding: a token
// whose Path is longer than decodeActivityContinuation accepts is one this
// service would refuse on the next request, which strands the entries it
// claims to lead to. Reporting the session to request instead leaves the
// reader somewhere to go — that session is its own root, where the path
// starts over at zero.
func activityContinuationPathFits(path []string) bool {
	return len(path) <= activityMaxContinuationPathLength
}

// activityUnreachableByPathDiagnostic is what a session says in place of a
// continuation it cannot name.
func activityUnreachableByPathDiagnostic(sessionID string) string {
	return fmt.Sprintf("continuation path limit reached; request session %q directly", sessionID)
}

func appendActivityPath(path []string, delegateID string) []string {
	next := append([]string(nil), path...)
	if delegateID != "" {
		next = append(next, delegateID)
	}
	return next
}

func appendActivityBranchError(branch *appwire.JobActivityBranchState, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if branch.Error == "" {
		branch.Error = message
		return
	}
	branch.Error += "; " + message
}

// unsupportedActivityJobTypesError summarizes the job records this projection
// cannot render into one counted message. One sentence per record would make
// Branch.Error grow with the journal — an unbounded envelope input, and
// unreadable besides — while the count and the first offender are what a
// reader needs. A single offender keeps the original per-record wording so
// the common case reads exactly as it always did.
func unsupportedActivityJobTypesError(records []*jobstore.JobRecord) string {
	count := 0
	var firstID string
	var firstType jobstore.JobType
	for _, rec := range records {
		if rec == nil || rec.Type == jobstore.JobShell {
			continue
		}
		count++
		if count == 1 {
			firstID, firstType = rec.JobID, rec.Type
		}
	}
	switch count {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("job %q has unsupported type %q", offenderID(firstID), offenderType(firstType))
	default:
		return fmt.Sprintf("%d job records have unsupported types; first is job %q type %q", count, offenderID(firstID), offenderType(firstType))
	}
}

// offenderID and offenderType bound the identifiers a collapsed error copies.
// Job IDs and types are ordinarily short, but the error is fixed content the
// envelope cannot trim, so neither is copied without a cap.
func offenderID(id string) string { return truncateActivityText(id, activityMaxLabelRunes) }

func offenderType(typ jobstore.JobType) string {
	return truncateActivityText(string(typ), activityMaxLabelRunes)
}

func activityOutcome(status jobstore.Status) (bool, string) {
	switch status {
	case jobstore.StatusRunning:
		return false, ""
	// A command that exited nonzero or was signalled is still a FAILURE
	// for the activity rollup, exactly like a machinery failure or an
	// exhausted delegate: attention follows the run, whatever broke it.
	case jobstore.StatusFailed, jobstore.StatusCommandExitedNonzero, jobstore.StatusCommandKilled, jobstore.StatusExhausted:
		return true, "failure"
	case jobstore.StatusCompleted:
		return true, "success"
	case jobstore.StatusCancelled, jobstore.StatusStopped:
		return true, "neutral"
	default:
		return status.IsTerminal(), ""
	}
}

func aggregateActivity(entries []appwire.JobActivityEntry, branch appwire.JobActivityBranchState) (appwire.JobActivityCounts, string) {
	counts := appwire.JobActivityCounts{Complete: activityBranchComplete(branch)}
	addJob := func(job appwire.JobActivityJob) {
		if !job.Terminal {
			counts.Active++
			return
		}
		if job.Outcome == "failure" {
			counts.Failed++
			return
		}
		counts.Completed++
	}
	for _, entry := range entries {
		if entry.Job != nil {
			addJob(*entry.Job)
		}
		if entry.Delegate == nil {
			continue
		}
		if entry.Delegate.Type == "delegate" {
			addJob(appwire.JobActivityJob{Terminal: entry.Delegate.Terminal, Outcome: activityDelegateOutcome(entry.Delegate.Outcome)})
		} else {
			for _, turn := range entry.Delegate.Turns {
				addJob(turn)
			}
		}
		if !activityBranchComplete(entry.Delegate.Branch) {
			counts.Complete = false
		}
		if entry.Delegate.Child != nil {
			counts.Active += entry.Delegate.Child.Counts.Active
			counts.Failed += entry.Delegate.Child.Counts.Failed
			counts.Completed += entry.Delegate.Child.Counts.Completed
			if !entry.Delegate.Child.Counts.Complete {
				counts.Complete = false
			}
		}
	}

	switch {
	case counts.Active > 0:
		return counts, "working"
	case counts.Failed > 0:
		return counts, "failed"
	case !counts.Complete:
		return counts, "unavailable"
	case counts.Completed > 0:
		return counts, "ended"
	default:
		return counts, "idle"
	}
}

func activityDelegateOutcome(outcome string) string {
	switch delegatestore.OutcomeStatus(outcome) {
	case delegatestore.OutcomeFailed, delegatestore.OutcomeExhausted:
		return "failure"
	case delegatestore.OutcomeCompleted:
		return "success"
	case delegatestore.OutcomeCancelled, delegatestore.OutcomeStopped:
		return "neutral"
	default:
		return ""
	}
}

func activityBranchComplete(branch appwire.JobActivityBranchState) bool {
	return branch.Error == "" && !branch.Truncated && branch.Continuation == ""
}

// activityTrimResume is the position the page being trimmed started from:
// the session depth hops below the tree's own root rendered its entries
// beginning at its own entry index, because that is the session a
// continuation's ResumeIndex was applied to (projectActivitySessionAt). A
// trim there counts within the entries THIS page rendered, so it has to add
// index back to mint a position in the session's own numbering — without
// that, a size trim on a resumed page mints a token pointing back into the
// page it just returned, and the client never advances. The zero value is a
// page that began at the top of its own root, which is every load that
// carries no continuation.
type activityTrimResume struct {
	depth int
	index int
}

// targets reports whether the session reached by path from the tree's root
// is the one this page was built around: every shallower level is the
// filtered ancestor chain, and every deeper one is a subtree the page
// carries along.
func (r activityTrimResume) targets(path []string) bool {
	return len(path) == r.depth
}

// offsetAt reports the entry-index offset to apply to a session reached by
// path from the tree's root. Only the page's own target applies it; every
// other session rendered from its own top.
func (r activityTrimResume) offsetAt(path []string) int {
	if !r.targets(path) {
		return 0
	}
	return r.index
}

// shrinkActivityAncestors halves the fixed prose an ancestor chain carries,
// dropping its packet payloads and trimming its warnings, until nothing more
// can be cut or the page fits. It reports whether it changed anything, so a
// caller can tell a page that fit after shrinking from one that genuinely
// cannot fit.
//
// A continuation page's ancestor delegates are fixed parts: the size trim can
// drop entries but never an ancestor, so per-field caps alone do not bound the
// page — the SUM across up to activityMaxContinuationPathLength ancestors is
// what must fit. This measures the actual marshaled page (so control-rune JSON
// expansion counts) and shrinks only the ancestors, never an entry the reader
// could otherwise have. ancestors is how many sessions from the tree's root are
// fixed parts; the page's own target is not one of them (activityTrimResume).
func shrinkActivityAncestors(root *appwire.JobActivitySession, ancestors int) bool {
	if root == nil || ancestors <= 0 {
		return false
	}
	changed := false
	shrinkText := func(s string) string {
		if n := utf8.RuneCountInString(s); n > activityAncestorTextFloorRunes {
			changed = true
			return truncateActivityText(s, max(activityAncestorTextFloorRunes, n/2))
		}
		return s
	}
	session := root
	for level := 0; level < ancestors && session != nil; level++ {
		var delegate *appwire.JobActivityDelegate
		for _, entry := range slices.Backward(session.Entries) {
			if entry.Delegate != nil {
				delegate = entry.Delegate
				break
			}
		}
		if delegate == nil {
			return changed
		}
		delegate.Mandate = shrinkText(delegate.Mandate)
		delegate.Task = shrinkText(delegate.Task)
		delegate.Description = shrinkText(delegate.Description)
		delegate.Reason = shrinkText(delegate.Reason)
		delegate.NotResumableReason = shrinkText(delegate.NotResumableReason)
		delegate.StructuredReason = shrinkText(delegate.StructuredReason)
		if delegate.Message != nil {
			delegate.Message = nil
			changed = true
		}
		if delegate.StructuredResult != nil {
			delegate.StructuredResult = nil
			changed = true
		}
		if len(delegate.Warnings) > 1 {
			delegate.Warnings = delegate.Warnings[:len(delegate.Warnings)/2]
			changed = true
		}
		session = delegate.Child
	}
	return changed
}

// trimActivityTreeToFit repeatedly drops the tree's trailing entry until it
// encodes within activityMaxEncodedBytes. epochs — every visited session's
// own generations, keyed by session ID — together with revision and resume
// feed every continuation trimming mints, and tell it which sessions cannot
// mint one at all; see trimActivityTrailingEntry. Dropping an entry is also the only evidence
// available about WHY the page was too big: an entry that leaves the page
// within the limit is what did not fit, and is skipped when no later page
// could carry it either; one that does not is left for a page that
// re-targets its session. When the page is still over the limit with
// nothing left to drop, the response's own fixed parts — labels,
// diagnostics, the ancestor chain's delegate metadata — are what exceed it,
// and no continuation can lead anywhere.
func trimActivityTreeToFit(tree appwire.JobActivityTree, rootID string, epochs map[string]activitySessionEpochs, revision uint64, resume activityTrimResume) (appwire.JobActivityTree, error) {
	for {
		recomputeActivitySession(&tree.Root)
		raw, err := json.Marshal(tree)
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		if len(raw) <= activityMaxEncodedBytes {
			return tree, nil
		}
		// Before sacrificing entries, shrink the ancestor chain's fixed
		// content. A continuation page carries that chain as the path back to
		// the page's target, and per-field caps do not bound their SUM across
		// up to activityMaxContinuationPathLength ancestors — the more so
		// because JSON encodes a control rune as six bytes. Only fields above
		// the floor are cut, so an ordinary chain is untouched and the entry
		// trim below behaves exactly as before.
		if shrinkActivityAncestors(&tree.Root, resume.depth) {
			continue
		}
		dropped, ok := trimActivityTrailingEntry(&tree.Root, rootID, nil, epochs, revision, resume)
		if !ok {
			markActivityEnvelopeTooLarge(&tree.Root, len(raw))
			// The error is part of what makes this page incomplete, and
			// completeness is derived from the branch: aggregate again so the
			// counts a reader trusts agree with it.
			recomputeActivitySession(&tree.Root)
			return tree, nil
		}
		if !dropped.unrepresentable {
			continue
		}
		recomputeActivitySession(&tree.Root)
		without, err := json.Marshal(tree)
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		if len(without) > activityMaxEncodedBytes {
			// The entry was not what did not fit: keep trimming, and leave
			// its position for the page that re-targets this session.
			continue
		}
		// It was. Advance past it and measure the page with the token it will
		// actually carry — advancing lengthens that token, so a page weighed
		// with the position it is leaving behind has not been weighed at all.
		mintActivityTrimContinuation(dropped, rootID, dropped.index+1, epochs, revision)
		recomputeActivitySession(&tree.Root)
		advanced, err := json.Marshal(tree)
		if err != nil {
			return appwire.JobActivityTree{}, err
		}
		if len(advanced) > activityMaxEncodedBytes {
			// No room even for the position to move. The advance stands
			// anyway: a page the client can never get past is a worse
			// response than one a token's length over the limit, and the
			// omission goes where it costs the page nothing.
			slog.Warn("activity page skipped an entry with no room to carry the advance",
				"entry", dropped.ref, "session", dropped.session.SessionID,
				"bytes", len(advanced), "limit", activityMaxEncodedBytes)
			return tree, nil
		}
		if err := explainActivitySkippedEntry(&tree, dropped); err != nil {
			return appwire.JobActivityTree{}, err
		}
		return tree, nil
	}
}

// markActivityEnvelopeTooLarge reports a page that cannot carry a single
// entry and withdraws its continuation: every page this token produced would
// be this same one, so the client is told what is wrong instead of being
// handed a loop. size is the encoded length of the entry-less response.
func markActivityEnvelopeTooLarge(session *appwire.JobActivitySession, size int) {
	if session == nil {
		return
	}
	session.Branch.Continuation = ""
	appendActivityBranchError(&session.Branch, fmt.Sprintf("activity response is %d bytes with no entries rendered, over the %d-byte limit", size, activityMaxEncodedBytes))
}

// activityTrimmedEntry is one entry the size trim dropped, and enough to
// re-mint its session's continuation once the tree has been re-encoded
// without it. unrepresentable marks the drop that emptied the page's own
// target: only there does the tree tell you whether the entry itself was
// what did not fit, since nothing else remained to shrink.
type activityTrimmedEntry struct {
	session         *appwire.JobActivitySession
	path            []string
	ref             string
	index           int
	unrepresentable bool
}

// explainActivitySkippedEntry names the skipped entry on a page whose advance
// past it has already been measured, keeping the most informative message
// that still fits: the named one, then a fixed short one. A page with room
// for neither keeps the advance and reports the omission where it costs
// nothing — a token that stands still is the one failure a client cannot
// recover from, so the sentence is what gives way, never the advance.
func explainActivitySkippedEntry(tree *appwire.JobActivityTree, dropped activityTrimmedEntry) error {
	advanced := dropped.session.Branch
	for _, message := range []string{dropped.ref + activitySkippedEntrySuffix, activitySkippedEntryShortMessage} {
		dropped.session.Branch = advanced
		appendActivityBranchError(&dropped.session.Branch, message)
		recomputeActivitySession(&tree.Root)
		raw, err := json.Marshal(*tree)
		if err != nil {
			return err
		}
		if len(raw) <= activityMaxEncodedBytes {
			return nil
		}
	}
	dropped.session.Branch = advanced
	recomputeActivitySession(&tree.Root)
	slog.Warn("activity page skipped an entry it had no room to report",
		"entry", dropped.ref, "session", dropped.session.SessionID)
	return nil
}

func mintActivityTrimContinuation(dropped activityTrimmedEntry, rootID string, resumeIndex int, epochs map[string]activitySessionEpochs, revision uint64) {
	if !activityContinuationPathFits(dropped.path) {
		appendActivityDiagnosticOnce(&dropped.session.Diagnostics, activityUnreachableByPathDiagnostic(dropped.session.SessionID))
		return
	}
	own := epochs[dropped.session.SessionID]
	dropped.session.Branch.Continuation = encodeActivityContinuation(activityContinuation{
		Version:             activityContinuationVersion,
		RootID:              rootID,
		SessionID:           dropped.session.SessionID,
		Path:                append([]string(nil), dropped.path...),
		ResumeIndex:         resumeIndex,
		JobsEpoch:           own.jobs,
		DelegatesEpoch:      own.delegates,
		JobsAbsent:          own.jobsAbsent,
		DelegatesAbsent:     own.delegatesAbsent,
		DelegatesUnreadable: own.delegatesUnreadable,
		Revision:            revision,
	})
}

// trimActivityTrailingEntry drops the deepest, last entry from session's
// tree (recursing into a delegate child before trimming session's own
// entries) and marks whichever session actually lost an entry truncated,
// with a continuation resuming right after it. epochs maps EACH session's
// own SessionID to its own fold-cache generations (see
// collectActivitySessionEpochs) — trimming can strike any session in the
// tree, not just the root, and a resume checks the token against the
// generations of the session it names. revision is the root's live-clock revision at mint time (see
// activityContinuation.Revision), which is uniform across the tree.
// Carrying all three lets a resumed continuation's staleness check
// actually detect a rewrite — or, for a live root, a mutation — that raced
// this trim. resume locates the page's own starting position so a mint
// lands in the trimmed session's entry numbering — see activityTrimResume.
// The dropped entry is reported back so the caller, which owns the encoded
// size, can decide whether that entry was what did not fit — trimming from
// the tail cannot tell on its own.
func trimActivityTrailingEntry(session *appwire.JobActivitySession, rootID string, path []string, epochs map[string]activitySessionEpochs, revision uint64, resume activityTrimResume) (activityTrimmedEntry, bool) {
	if session == nil || len(session.Entries) == 0 {
		return activityTrimmedEntry{}, false
	}
	i := len(session.Entries) - 1
	entry := &session.Entries[i]
	if entry.Delegate != nil && entry.Delegate.Child != nil {
		if dropped, ok := trimActivityTrailingEntry(entry.Delegate.Child, rootID, appendActivityPath(path, entry.Delegate.DelegateID), epochs, revision, resume); ok {
			return dropped, true
		}
	}
	// An entry at index 0 of the page's own target leaves the page with
	// nothing else to shrink: whether the response still exceeds the limit
	// without it is exactly the question of whether this entry is
	// representable at all, and only the caller can answer it. A DEEPER
	// session reaching index 0 says only that this page ran out of room —
	// the continuation minted here re-targets that session, whose own page
	// carries none of the ancestors' weight.
	dropped := activityTrimmedEntry{
		session:         session,
		path:            append([]string(nil), path...),
		ref:             activityEntryRef(*entry),
		index:           resume.offsetAt(path) + i,
		unrepresentable: i == 0 && resume.targets(path),
	}
	session.Entries = session.Entries[:i]
	session.Branch.Truncated = true
	mintActivityTrimContinuation(dropped, rootID, dropped.index, epochs, revision)
	return dropped, true
}

// activityEntryRef names an entry for an operator-facing branch error: an
// entry carries no name of its own, so the underlying job or delegate ID is
// what makes the omission findable in the journals.
func activityEntryRef(entry appwire.JobActivityEntry) string {
	switch {
	case entry.Job != nil:
		return fmt.Sprintf("job %q", entry.Job.JobID)
	case entry.Delegate != nil:
		return fmt.Sprintf("delegate %q", entry.Delegate.DelegateID)
	default:
		return fmt.Sprintf("entry of kind %q", entry.Kind)
	}
}

func recomputeActivitySession(session *appwire.JobActivitySession) {
	if session == nil {
		return
	}
	for i := range session.Entries {
		delegate := session.Entries[i].Delegate
		if delegate != nil && delegate.Child != nil {
			recomputeActivitySession(delegate.Child)
		}
	}
	session.Counts, session.Aggregate = aggregateActivity(session.Entries, session.Branch)
}

// projectActivityJob projects one job record into the activity tree's job
// shape. (The flat jobs-list compatibility path this once also served was
// retired in 4993cdd53; the tree is its only consumer now.)
func projectActivityJob(rec *jobstore.JobRecord, ownerRef string) appwire.JobActivityJob {
	if rec == nil {
		return appwire.JobActivityJob{}
	}
	description := rec.Description
	if description == "" {
		description = rec.Command
	}
	if description == "" {
		description = rec.Task
	}
	terminal, outcome := activityOutcome(rec.Status)
	job := appwire.JobActivityJob{
		JobID:          rec.JobID,
		OwnerSessionID: rec.OwnerSessionID,
		OwnerRef:       ownerRef,
		// jobTranscriptRef derives the canonical "job:<id>" ref for shell jobs
		// (their records never store one) and passes through a stored ref
		// (delegate turns point at the child session) unchanged.
		TranscriptRef:    jobTranscriptRef(rec),
		Type:             string(rec.Type),
		Status:           string(rec.Status),
		Authority:        string(rec.Authority),
		Incomplete:       rec.Incomplete,
		IntegrityReasons: append([]string(nil), rec.IntegrityReasons...),
		Outcome:          outcome,
		Terminal:         terminal,
		Background:       rec.Background,
		HasOutput:        rec.OutputPath != "" || rec.OutputBytes > 0,
		Description:      description,
		Command:          rec.Command,
		Task:             rec.Task,
		Reason:           rec.Reason,
		StartedAt:        rec.StartedAt.UTC().Format(time.RFC3339),
		OutputBytes:      rec.OutputBytes,
	}
	if rec.EndedAt != nil {
		job.EndedAt = rec.EndedAt.UTC().Format(time.RFC3339)
	}
	if rec.LastActivity != nil {
		job.LastOutputAt = rec.LastActivity.UTC().Format(time.RFC3339)
	}
	if rec.ExitCode != nil {
		exit := *rec.ExitCode
		job.ExitCode = &exit
	}
	return job
}
