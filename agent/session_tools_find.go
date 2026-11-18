package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/envvars/userdirs"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// findLimitDefault and findLimitMax bound the number of matches returned. Spec
// §Input Schema: limit defaults to 10, hard max 50.
const (
	findLimitDefault = 10
	findLimitMax     = 50
)

// maxContentScan bounds the cross-session content scan: at most this many
// candidate transcripts are opened and substring-scanned per call. The scan is
// deliberately NOT unbounded — a project can accumulate thousands of sessions,
// and opening every transcript on a content query would make discovery O(corpus)
// and slow. When the scan stops before exhausting candidates, the response sets
// scan_truncated=true so the model knows coverage was partial (spec §"Discovery
// Cost"). 200 newest sessions is a generous triage window while keeping the worst
// case bounded.
const maxContentScan = 200

// snippetWidth is the approximate character width of a match excerpt.
const snippetWidth = 200

const (
	scopeCurrentProject = "current_project"
	scopeAllProjects    = "all_projects"
)

const (
	kindRoot     = "root"
	kindSubagent = "subagent"
	kindFork     = "fork"
)

// snippet is one match excerpt: the seq it is addressable by, the role of the
// matching turn, and a bounded text excerpt. Shared by both response modes.
type snippet struct {
	Seq     int    `json:"seq"`
	Role    string `json:"role"`
	Snippet string `json:"snippet"`
}

// snippetCollector accumulates snippets while collapsing pointers that resolve to
// the same seq. A TOOL_RESULTS turn is remapped to its owning ASSISTANT seq, so
// if both that ASSISTANT turn and its folded result match, they would otherwise
// burn two pointers on one addressable seq. Entries arrive seq-ascending and the
// owning ASSISTANT precedes its result, so first-seen-wins keeps the ASSISTANT
// turn's own role label — the most informative label for the heading the seq
// actually addresses.
type snippetCollector struct {
	out  []snippet
	seen map[int]struct{}
}

func newSnippetCollector(capHint int) *snippetCollector {
	return &snippetCollector{out: make([]snippet, 0, capHint), seen: make(map[int]struct{})}
}

// add records s unless a pointer for its seq was already collected; it reports
// whether the snippet was newly added.
func (c *snippetCollector) add(s snippet) bool {
	if _, dup := c.seen[s.Seq]; dup {
		return false
	}
	c.seen[s.Seq] = struct{}{}
	c.out = append(c.out, s)
	return true
}

// sessionRecord is one find_session_transcripts match. Field order follows
// spec §"Response". Only the fields explicitly listed in the spec are included;
// session_id, model, profile_id, created_at, has_transcript, and default_read
// are intentionally absent. When TranscriptRef is empty (legacy-named bucket
// whose name the ref grammar rejects), the session is still addressable by
// its bare session ID via read_transcript; the text format surfaces the ID
// so the model knows how to read it without a proj: ref.
type sessionRecord struct {
	TranscriptRef string    `json:"transcript_ref,omitempty"`
	Kind          string    `json:"kind"`
	Title         string    `json:"title"`
	UpdatedAt     time.Time `json:"updated_at"`
	ApproxTurns   int       `json:"approx_turns"`
	ParentRef     string    `json:"parent_ref,omitempty"`
	Project       string    `json:"project,omitempty"`
	IsCurrent     bool      `json:"is_current,omitempty"`
	Snippets      []snippet `json:"snippets,omitempty"`
	// SessionID is not part of the JSON wire format (intentionally absent per
	// spec) but is carried for the text-format renderer so it can tell the
	// model how to address a legacy-bucket session that has no transcript_ref.
	SessionID string `json:"-"`
}

// findSessionsEnvelope is the find_session_transcripts response. Scanned and
// ScanTruncated are only meaningful when a content scan ran (i.e. query was
// present); they are omitted in catalog (no-query) and children_of mode.
type findSessionsEnvelope struct {
	Matches       []sessionRecord `json:"matches"`
	ScopeApplied  string          `json:"scope_applied"`
	Scanned       *int            `json:"scanned,omitempty"`
	ScanTruncated *bool           `json:"scan_truncated,omitempty"`
}

func findSessionTranscriptsTool(deps *toolDeps) tool.RegisteredTool {
	return tool.RegisteredTool{
		Definition: tool.DefFindSessionTranscripts(), ReadOnly: true,
		Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			v, err := execFindSessionTranscripts(ctx, deps, args)
			if err != nil {
				return nil, err
			}
			return formatFindSessionResult(v), nil
		},
	}
}

func formatFindSessionResult(v any) any {
	env, ok := v.(findSessionsEnvelope)
	if !ok {
		return v
	}
	return formatSessionFindings(env)
}

// formatSessionFindings renders the find result as plain text: one numbered block
// per matching session — the transcript_ref handle, title, and metadata, with any
// matched snippets indented beneath — and a footer reporting the scope and (when a
// content scan ran) how many sessions were scanned. The structured envelope is the
// return value of execFindSessionTranscripts, used directly by tests.
func formatSessionFindings(env findSessionsEnvelope) string {
	if len(env.Matches) == 0 {
		return fmt.Sprintf("No matching sessions (scope: %s).", env.ScopeApplied)
	}
	var b strings.Builder
	for i, m := range env.Matches {
		if m.TranscriptRef != "" {
			fmt.Fprintf(&b, "%d. %s — %s\n", i+1, m.TranscriptRef, m.Title)
		} else {
			// No transcript_ref: the session lives in a legacy-named
			// bucket whose name the ref grammar rejects. It is still
			// addressable by its bare session ID — surface it so the
			// model can pass it to read_transcript.
			fmt.Fprintf(&b, "%d. (no ref, bare id: %s) — %s\n", i+1, m.SessionID, m.Title)
		}
		meta := fmt.Sprintf("   %s · ~%d turns · updated %s", m.Kind, m.ApproxTurns, m.UpdatedAt.Format("2006-01-02 15:04"))
		if m.Project != "" {
			meta += " · project " + m.Project
		}
		if m.IsCurrent {
			meta += " · current"
		}
		b.WriteString(meta)
		b.WriteString("\n")
		if m.ParentRef != "" {
			fmt.Fprintf(&b, "   parent: %s\n", m.ParentRef)
		}
		for _, s := range m.Snippets {
			fmt.Fprintf(&b, "   seq %d (%s): %s\n", s.Seq, s.Role, s.Snippet)
		}
	}
	footer := fmt.Sprintf("\n%d match", len(env.Matches))
	if len(env.Matches) != 1 {
		footer += "es"
	}
	footer += " (scope: " + env.ScopeApplied
	if env.Scanned != nil {
		footer += fmt.Sprintf(", scanned %d", *env.Scanned)
		if env.ScanTruncated != nil && *env.ScanTruncated {
			footer += ", scan truncated"
		}
	}
	footer += ")"
	b.WriteString(footer)
	return b.String()
}

// execFindSessionTranscripts dispatches to execFindChildren when children_of is
// set (it takes precedence over query per spec), otherwise to execFindAcrossSessions.
func execFindSessionTranscripts(ctx context.Context, deps *toolDeps, args map[string]any) (any, error) {
	query := strings.TrimSpace(stringArg(args, "query"))
	childrenOf := strings.TrimSpace(stringArg(args, "children_of"))
	limit := clampFindLimit(optionalIntArg(args, "limit"))
	// Filters are parsed (and validated) before the children_of branch so they
	// compose with it: children_of is a filter, not a separate mode.
	filters, err := parseFindFilters(args)
	if err != nil {
		return nil, err
	}
	if childrenOf != "" {
		return execFindChildren(deps, childrenOf, limit, filters) // precedes query per spec
	}
	scope := strings.TrimSpace(stringArg(args, "scope"))
	if scope == "" {
		scope = scopeCurrentProject
	}
	return execFindAcrossSessions(ctx, deps, query, scope, limit, filters)
}

// findFilters holds the optional metadata-only narrowing arguments to
// find_session_transcripts. A zero value applies no filter.
type findFilters struct {
	kind          string
	hasChildren   *bool
	minTurns      *int
	maxTurns      *int
	updatedAfter  *time.Time
	updatedBefore *time.Time
}

// parseFindFilters reads the typed metadata filters and validates them. An
// out-of-order range, an unknown kind, or an unparsable timestamp is an error
// rather than a silently ignored argument.
func parseFindFilters(args map[string]any) (findFilters, error) {
	var f findFilters
	kind, err := optionalStringArg(args, "kind")
	if err != nil {
		return findFilters{}, err
	}
	kind = strings.TrimSpace(kind)
	switch kind {
	case "", "any":
	case kindRoot, kindSubagent, kindFork:
		f.kind = kind
	default:
		return findFilters{}, fmt.Errorf("invalid_request: unknown kind %q: use root, subagent, fork, or any", kind)
	}

	hasChildren, err := optionalBoolArg(args, "has_children")
	if err != nil {
		return findFilters{}, err
	}
	f.hasChildren = hasChildren
	minTurns, err := optionalWholeIntArg(args, "min_turns")
	if err != nil {
		return findFilters{}, err
	}
	maxTurns, err := optionalWholeIntArg(args, "max_turns")
	if err != nil {
		return findFilters{}, err
	}
	f.minTurns, f.maxTurns = minTurns, maxTurns
	if f.minTurns != nil && f.maxTurns != nil && *f.minTurns > *f.maxTurns {
		return findFilters{}, fmt.Errorf("invalid_request: min_turns %d is greater than max_turns %d", *f.minTurns, *f.maxTurns)
	}

	after, err := optionalTimeArg(args, "updated_after")
	if err != nil {
		return findFilters{}, err
	}
	before, err := optionalTimeArg(args, "updated_before")
	if err != nil {
		return findFilters{}, err
	}
	if after != nil && before != nil && after.After(*before) {
		return findFilters{}, fmt.Errorf("invalid_request: updated_after %s is after updated_before %s",
			after.Format(time.RFC3339), before.Format(time.RFC3339))
	}
	f.updatedAfter, f.updatedBefore = after, before
	return f, nil
}

// filterCandidates applies the metadata-only filters before any transcript is
// opened. has_children is computed over the whole candidate set: a candidate is
// a parent when some other candidate names it as its ParentSessionID.
func filterCandidates(candidates []findCandidate, f findFilters, currentID string, currentMeta func() schema.SessionMeta) []findCandidate {
	if f.kind == "" && f.hasChildren == nil && f.minTurns == nil && f.maxTurns == nil &&
		f.updatedAfter == nil && f.updatedBefore == nil {
		return candidates
	}
	// The parent set is only needed to answer has_children, so build it only
	// when that filter is set.
	var parents map[string]struct{}
	if f.hasChildren != nil {
		parents = make(map[string]struct{}, len(candidates))
		for _, c := range candidates {
			if c.meta.ParentSessionID != "" {
				parents[c.meta.ParentSessionID] = struct{}{}
			}
		}
	}
	out := candidates[:0:0]
	for _, c := range candidates {
		if f.kind != "" && sessionKind(c.meta) != f.kind {
			continue
		}
		if f.hasChildren != nil {
			_, has := parents[c.meta.ID]
			if has != *f.hasChildren {
				continue
			}
		}
		turns, updated := candidateFreshness(c, currentID, currentMeta)
		if f.minTurns != nil && turns < *f.minTurns {
			continue
		}
		if f.maxTurns != nil && turns > *f.maxTurns {
			continue
		}
		if f.updatedAfter != nil && updated.Before(*f.updatedAfter) {
			continue
		}
		if f.updatedBefore != nil && updated.After(*f.updatedBefore) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// clampFindLimit applies the default (10) and hard max (50) to the caller-supplied
// limit. A missing, zero, or negative limit becomes the default.
func clampFindLimit(p *int) int {
	if p == nil || *p <= 0 {
		return findLimitDefault
	}
	if *p > findLimitMax {
		return findLimitMax
	}
	return *p
}

// buildSessionRecord assembles the wire record for a candidate. ParentRef is
// encoded relative to the current bucket (the ref the model can pass back).
// currentID is the live session's ID; a match sets IsCurrent. The match
// requires BOTH the current bucket (c.projectID == "", i.e. the candidate is
// in the current state dir) AND the ID — a duplicate ID in a sibling bucket must
// not get IsCurrent or the live overlay. currentMeta (when non-nil) supplies the
// live session's in-memory meta: the current session's on-disk meta is stale
// mid-run (its turn count and updated-at are only flushed at turn boundaries),
// so those freshness fields are overlaid from memory.
func buildSessionRecord(c findCandidate, snips []snippet, currentID string, currentMeta func() schema.SessionMeta, resolver ...*projectNameResolver) sessionRecord {
	turnCount, updatedAt := candidateFreshness(c, currentID, currentMeta)
	parentRef := ""
	if c.meta.ParentSessionID != "" {
		parentRef = refFor(c.projectID, c.meta.ParentSessionID)
	}
	pName := ""
	if len(resolver) > 0 && resolver[0] != nil {
		pName = resolver[0].projectName(c.meta, c.bucketDir)
	} else {
		pName = projectName(c.meta, c.bucketDir)
	}
	return sessionRecord{
		TranscriptRef: refFor(c.projectID, c.meta.ID),
		Kind:          sessionKind(c.meta),
		Title:         firstLineClamp(schema.SessionDisplayName(c.meta), 120),
		UpdatedAt:     updatedAt,
		ApproxTurns:   turnCount,
		ParentRef:     parentRef,
		Project:       pName,
		IsCurrent:     c.projectID == "" && c.meta.ID == currentID,
		Snippets:      snips,
		SessionID:     c.meta.ID,
	}
}

// candidateFreshness resolves a candidate's effective turn count and updated-at
// time. The current session's on-disk meta is stale mid-run (its turn count and
// updated-at are only flushed at turn boundaries), so when currentMeta is
// non-nil and the candidate is the live session those freshness fields are
// overlaid from memory. Both buildSessionRecord and the metadata filters use
// this, so a filtered field and a reported field always agree.
func candidateFreshness(c findCandidate, currentID string, currentMeta func() schema.SessionMeta) (int, time.Time) {
	if currentMeta != nil && c.projectID == "" && c.meta.ID == currentID {
		live := currentMeta()
		return live.TurnCount, live.UpdatedAt
	}
	return c.meta.TurnCount, c.meta.UpdatedAt
}

// sortCandidatesNewestFirst sorts candidates by UpdatedAt descending, ID ascending as
// a stable tie-break, with the current session always last. The current session
// is identified by BOTH the current bucket (projectID == "") AND the ID — a
// duplicate ID in a sibling bucket is not the current session.
func sortCandidatesNewestFirst(candidates []findCandidate, currentID string) {
	sort.Slice(candidates, func(i, j int) bool {
		isCurI := candidates[i].projectID == "" && candidates[i].meta.ID == currentID
		isCurJ := candidates[j].projectID == "" && candidates[j].meta.ID == currentID
		if isCurI != isCurJ {
			return isCurJ // current session sorts after non-current
		}
		ti, tj := candidates[i].meta.UpdatedAt, candidates[j].meta.UpdatedAt
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return candidates[i].meta.ID < candidates[j].meta.ID
	})
}

// recordsUpTo builds sessionRecords for the first limit already-sorted candidates
// that have a readable transcript on disk.
func recordsUpTo(candidates []findCandidate, snipsFor func(c findCandidate) []snippet, currentID string, limit int, currentMeta func() schema.SessionMeta) []sessionRecord {
	resolver := newProjectNameResolver()
	var out []sessionRecord
	for _, c := range candidates {
		if len(out) >= limit {
			break
		}
		if !transcriptExists(c.bucketDir, c.meta.ID) {
			continue
		}
		out = append(out, buildSessionRecord(c, snipsFor(c), currentID, currentMeta, resolver))
	}
	return out
}

// execFindAcrossSessions implements catalog (no query) and content-search (query
// present) over all buckets in scope. With no query: metadata-only, newest-first,
// current last, no scan metrics. With a query: metadata match first (cheap, no
// file open); on miss, bounded raw content scan tracking scanned/scanTruncated.
func execFindAcrossSessions(ctx context.Context, deps *toolDeps, query, scope string, limit int, filters findFilters) (any, error) {
	buckets, scopeApplied, err := findBuckets(deps.stateDir, scope)
	if err != nil {
		return nil, err
	}
	currentID := deps.sessionID

	candidates := collectCandidates(buckets, deps.stateDir)
	// Metadata-only filters run before any transcript is opened, so a filtered
	// candidate never consumes the content-scan budget.
	candidates = filterCandidates(candidates, filters, currentID, deps.currentMeta)
	sortCandidatesNewestFirst(candidates, currentID)

	var records []sessionRecord
	var scanned *int
	var scanTruncated *bool

	if query == "" {
		// Catalog: metadata-only, no scan.
		records = recordsUpTo(candidates, func(_ findCandidate) []snippet { return nil }, currentID, limit, deps.currentMeta)
	} else {
		// Content-search: track coverage.
		resolver := newProjectNameResolver()
		n := 0
		trunc := false
		needle := strings.ToLower(query)
		for _, c := range candidates {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(records) >= limit {
				break
			}
			if !transcriptExists(c.bucketDir, c.meta.ID) {
				continue
			}
			if snips, matched := matchCandidate(ctx, c, query, needle, &n, &trunc); matched {
				records = append(records, buildSessionRecord(c, snips, currentID, deps.currentMeta, resolver))
			}
		}
		// A cancellation inside the last candidate's read is swallowed as a
		// non-match, so the partial result must not be returned as complete.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		scanned = &n
		scanTruncated = &trunc
	}

	return findSessionsEnvelope{
		Matches:       records,
		ScopeApplied:  scopeApplied,
		Scanned:       scanned,
		ScanTruncated: scanTruncated,
	}, nil
}

// execFindChildren resolves the parent ref (metadata only — no transcript open),
// then lists all candidates in the parent's bucket and returns those whose
// ParentSessionID matches the parent. The metadata filters apply here too, over
// the whole bucket so a has_children filter still sees the parent relation.
func execFindChildren(deps *toolDeps, ref string, limit int, filters findFilters) (any, error) {
	bucketDir, parentID, scopeApplied, err := parentBucketAndID(ref, deps.stateDir, deps.sessionID)
	if err != nil {
		return nil, err
	}
	currentID := deps.sessionID

	candidates := collectCandidates([]string{bucketDir}, deps.stateDir)
	candidates = filterCandidates(candidates, filters, currentID, deps.currentMeta)

	// Keep only direct children of parentID.
	var children []findCandidate
	for _, c := range candidates {
		if c.meta.ParentSessionID == parentID {
			children = append(children, c)
		}
	}

	sortCandidatesNewestFirst(children, currentID)

	// parentBucketAndID may stat candidate buckets to locate the parent for a
	// bare ID, but it opens no transcript. Every returned child must still be a
	// read-able ref, so children are gated on the same transcriptExists (os.Stat,
	// not a body open) as the catalog: a spawned child whose transcript was never
	// flushed is not auditable and is excluded.
	records := recordsUpTo(children, func(findCandidate) []snippet { return nil }, currentID, limit, deps.currentMeta)

	return findSessionsEnvelope{
		Matches:      records,
		ScopeApplied: scopeApplied,
	}, nil
}

// findBuckets returns the bucket dirs to scan for the requested scope and the
// scope actually applied. current_project is just the current bucket.
// all_projects enumerates sibling buckets via stateHomeFor → enumerateBuckets;
// under a flat state dir (stateHomeFor == "") it degrades to the current bucket
// and reports current_project (spec §"Discovery Cost").
func findBuckets(currentStateDir, scope string) (buckets []string, scopeApplied string, err error) {
	return findBucketsWithEnumerate(currentStateDir, scope, enumerateBuckets)
}

func findBucketsWithEnumerate(currentStateDir, scope string, enumerate func(string) ([]string, error)) (buckets []string, scopeApplied string, err error) {
	if scope != scopeAllProjects {
		// Validate the layout prefix for the current bucket on every scope,
		// not just all_projects. The all_projects path gets prefix validation
		// via enumerateBuckets (which returns errSymlinkedLayoutPrefix); the
		// current-bucket fast path previously returned the bucket unvalidated,
		// so find reported "No matching sessions" while read_transcript and
		// all_projects surfaced the explicit symlink refusal. Propagate the
		// refusal so all three paths agree (finding 5).
		if err := validateLayoutPrefix(currentStateDir); err != nil {
			return nil, "", err
		}
		return []string{currentStateDir}, scopeCurrentProject, nil
	}
	sh := stateHomeFor(currentStateDir)
	if sh == "" {
		// Validate the layout prefix for the current bucket on the flat
		// all_projects path too. validateLayoutPrefix Lstats the bucket
		// dir itself (symlinkErrorDeep rooted at the bucket does not), so
		// a symlinked flat --state-dir is refused here rather than
		// silently returning the bucket — which find would then report as
		// "No matching sessions" while read_transcript surfaces the refusal.
		if err := validateLayoutPrefix(currentStateDir); err != nil {
			return nil, "", err
		}
		return []string{currentStateDir}, scopeCurrentProject, nil
	}
	all, enumerateErr := enumerate(sh)
	if enumerateErr != nil {
		if errors.Is(enumerateErr, errSymlinkedLayoutPrefix) {
			return nil, "", enumerateErr
		}
		// Other errors (e.g. bad glob pattern): fall back to current project.
		return []string{currentStateDir}, scopeCurrentProject, nil
	}
	if len(all) == 0 {
		return []string{currentStateDir}, scopeCurrentProject, nil
	}
	return all, scopeAllProjects, nil
}

// findCandidate pairs a session meta with the bucket it lives in, so its ref can
// be encoded relative to the current bucket and its transcript located.
type findCandidate struct {
	meta      schema.SessionMeta
	bucketDir string
	projectID string // "" when this is the current bucket (→ local: ref)
}

// collectCandidates lists SessionMetas (cheap, meta-only) over each bucket and
// pairs each with its bucket. The projectID is "" for the bucket that IS the
// current state dir (so its refs are local:), and the bucket's project ID for
// every sibling (proj: refs). The current bucket is identified by absolute-path
// equality, not list position, because enumerateBuckets returns buckets in
// arbitrary glob order.
func collectCandidates(buckets []string, currentStateDir string) []findCandidate {
	currentAbs, _ := filepath.Abs(currentStateDir)
	// Validate the layout prefix for the current bucket: it enters this
	// function directly (not via enumerateBuckets, which already validates
	// the prefix for sibling buckets). symlinkErrorDeep below is rooted at
	// the bucket, so ancestors above it (evener/, evener/projects/) are not
	// checked. A symlinked evener/ within the state root would let metas
	// from outside the state root surface in find + children_of results.
	currentPrefixOK := validateLayoutPrefix(currentStateDir) == nil
	var out []findCandidate
	for _, bucket := range buckets {
		bucketAbs, _ := filepath.Abs(bucket)
		if bucketAbs == currentAbs && !currentPrefixOK {
			continue
		}
		// Skip buckets whose sessions/ dir is a symlink pointing outside
		// the state root: ListSessionMetas uses afero.ReadDir which follows
		// symlinked directories, so metas from outside the state root would
		// surface in find + children_of results while read_transcript
		// rejects them.
		sessDir := schema.SessionsDir(bucket)
		if err := symlinkErrorDeep(sessDir, bucket); err != nil {
			continue
		}
		metas, err := schema.ListSessionMetas(bucket)
		if err != nil {
			continue
		}
		projectID := ""
		if bucketAbs, _ := filepath.Abs(bucket); bucketAbs != currentAbs {
			projectID = filepath.Base(bucket)
		}
		for _, m := range metas {
			if identifier.ValidateSessionID(m.ID) != nil {
				continue
			}
			out = append(out, findCandidate{meta: m, bucketDir: bucket, projectID: projectID})
		}
	}
	return out
}

// matchCandidate decides whether a candidate matches the query and, if it matched
// on content, returns the rendered snippets. Metadata is checked first (cheap,
// no file open). Only on a metadata miss is the transcript opened for a bounded
// content scan; scanned/scanTruncated track that coverage. scanned counts only
// transcripts actually OPENED — a metadata-miss candidate whose transcript file
// is absent does not consume the budget.
func matchCandidate(ctx context.Context, c findCandidate, query, needle string, scanned *int, scanTruncated *bool) ([]snippet, bool) {
	if metaMatches(c.meta, needle) {
		return metadataSnippets(c.meta, query, needle), true
	}
	// Content scan is bounded: once maxContentScan transcripts have been opened,
	// stop scanning further candidates and flag the partial coverage.
	if *scanned >= maxContentScan {
		*scanTruncated = true
		return nil, false
	}
	snips, opened := contentSnippets(ctx, c.bucketDir, c.meta.ID, query, needle)
	if opened {
		*scanned++
	}
	if len(snips) == 0 {
		return nil, false
	}
	return snips, true
}

// metadataSnippetSeq marks a metadata-only snippet's seq as not a turn address.
// The metadata-only fast path deliberately opens no transcript, and a subagent
// or fork can carry inherited context ahead of its own assignment, so the
// matching text need not live at turn 0 — a metadata snippet cannot name a real
// turn. The sentinel is negative so no caller can mistake it for one.
const metadataSnippetSeq = -1

// metadataSnippets renders a bounded evidence snippet for a metadata-only match
// so the record is not context-free. The initial prompt is the strongest
// evidence (it is what the user asked); the title is the fallback. A match on
// another metadata field (id, model, parent, working dir) yields no snippet.
// The seq is metadataSnippetSeq, not a turn address.
func metadataSnippets(m schema.SessionMeta, query, needle string) []snippet {
	if m.OriginalPrompt != "" && strings.Contains(strings.ToLower(m.OriginalPrompt), needle) {
		return []snippet{{Seq: metadataSnippetSeq, Role: "user", Snippet: makeSnippet(m.OriginalPrompt, query, snippetWidth)}}
	}
	// Only the generated name, not SessionDisplayName: that falls back to the
	// prompt (handled above) and then the session ID, and an ID match should not
	// be mislabelled as a title.
	if title := strings.TrimSpace(m.Name); title != "" && strings.Contains(strings.ToLower(title), needle) {
		return []snippet{{Seq: metadataSnippetSeq, Role: "title", Snippet: makeSnippet(title, query, snippetWidth)}}
	}
	return nil
}

// metaMatches reports whether the cheap SessionMeta fields contain the needle
// (already lowercased). Covers session ID, title, original prompt, model,
// profile ID, parent session ID, and recorded working directory.
func metaMatches(m schema.SessionMeta, needle string) bool {
	fields := []string{
		m.ID,
		schema.SessionDisplayName(m),
		m.OriginalPrompt,
		m.Model,
		m.ProfileID,
		m.ParentSessionID,
		m.EnvInfo.WorkingDir,
	}
	for _, f := range fields {
		if f != "" && strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

// contentSnippets does the cheap raw substring scan of one transcript's raw entry
// text and renders snippets for the matching turns. It opens and parses the
// transcript only when reached; the second return reports whether the file was
// actually opened, so the caller counts only real opens toward the scan budget.
func contentSnippets(ctx context.Context, bucketDir, sessionID, query, needle string) (snips []snippet, opened bool) {
	// Validate the layout prefix for in-layout buckets: symlinkErrorDeep
	// below is rooted at bucketDir, so ancestors above it are not checked.
	// For sibling buckets from enumerateBuckets, the prefix was already
	// validated; for the current bucket, this is the first check.
	if err := validateLayoutPrefix(bucketDir); err != nil {
		return nil, false
	}
	path := transcriptPath(bucketDir, sessionID)
	// Reject symlinked transcript files (and symlinked sessions/ dirs) before
	// reading — a symlink could point outside the state root.
	if err := symlinkErrorDeep(path, bucketDir); err != nil {
		return nil, false
	}
	_, entries, _, err := readTranscript(ctx, path, bucketDir)
	if err != nil {
		return nil, false
	}
	entries = publicTranscriptEntries(entries)
	collector := newSnippetCollector(0)
	for i := range entries {
		text := rawEntryText(entries[i].Turn)
		if text == "" || !strings.Contains(strings.ToLower(text), needle) {
			continue
		}
		seq := i
		if entries[i].Turn.Kind == schema.TurnToolResults {
			if owner, ok := owningAssistantSeq(entries, i); ok {
				seq = owner
			}
		}
		collector.add(snippet{
			Seq:     seq,
			Role:    turnRoleLabel(entries[i].Turn.Kind),
			Snippet: makeSnippet(text, query, snippetWidth),
		})
	}
	// Rank within the session: the matching user message is the evidence a
	// reader wants first, then the assistant, then tool output; sequence is the
	// stable tie-break within a rank.
	out := collector.out
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := snippetRoleRank(out[i].Role), snippetRoleRank(out[j].Role)
		if ri != rj {
			return ri < rj
		}
		return out[i].Seq < out[j].Seq
	})
	return out, true
}

// snippetRoleRank orders snippet roles by evidence value: a matching user
// message, then the assistant, then tool output, then everything else.
func snippetRoleRank(role string) int {
	switch role {
	case "user":
		return 0
	case "assistant":
		return 1
	case "tool_result":
		return 2
	default:
		return 3
	}
}

// transcriptExists is a cheap stat of the transcript JSONL file (no parse).
// Delegates to existsNonSymlink so symlinked files, symlinked sessions/ dirs,
// symlinked bucket dirs, and non-regular entries (directories, FIFOs) are all
// rejected — find must not return refs read_transcript rejects.
func transcriptExists(bucketDir, sessionID string) bool {
	return existsNonSymlink(transcriptPath(bucketDir, sessionID), bucketDir)
}

// sessionKind derives the session classification (not a stored field):
// subagent (IsSubagent) → fork (ParentSessionID set or DivergenceTurn>0, but not
// a subagent) → root otherwise.
func sessionKind(m schema.SessionMeta) string {
	if m.IsSubagent {
		return kindSubagent
	}
	if m.ParentSessionID != "" || m.DivergenceTurn > 0 {
		return kindFork
	}
	return kindRoot
}

// projectName is the display project string: basename of EnvInfo.GitOriginURL
// when present, else derived from the main repository root (for git worktrees or
// checkouts) or bucket metadata, falling back to WorkingDir basename.
// A trailing ".git" is stripped so "…/evener.git" displays as "evener".
func projectName(m schema.SessionMeta, bucketDir ...string) string {
	bDir := ""
	if len(bucketDir) > 0 {
		bDir = bucketDir[0]
	}
	return newProjectNameResolver().projectName(m, bDir)
}

type projectNameResolver struct {
	mainRootCache      map[string]string
	bucketProjectCache map[bucketCacheKey]string
	stateRootCache     []string
}

// bucketCacheKey identifies a cached bucket answer together with the sibling
// eligibility that produced it. A label is only reusable by a row that would
// have allowed the same siblings to answer: the same bucket yields a different
// answer for a row with no project requirements than for a managed lane whose
// path names a repository. Keying by the bucket alone lets a differently
// qualified lane read an answer it never earned.
type bucketCacheKey struct {
	dir  string
	mode string
}

func newProjectNameResolver() *projectNameResolver {
	return &projectNameResolver{
		mainRootCache:      make(map[string]string),
		bucketProjectCache: make(map[bucketCacheKey]string),
	}
}

func (r *projectNameResolver) projectName(m schema.SessionMeta, bucketDir string) string {
	if origin := strings.TrimSpace(m.EnvInfo.GitOriginURL); origin != "" {
		return repoBasename(origin)
	}
	wd := strings.TrimSpace(m.HomeDir())
	if wd == "" {
		wd = strings.TrimSpace(m.EnvInfo.WorkingDir)
	}
	if wd == "" {
		return ""
	}

	// 1. A state-managed lane is identified first, from the state directory that
	// records it. A lane whose checkout is gone has no .git to stop a Git walk,
	// so the walk would climb past the lane root and answer with whichever
	// repository encloses the state dir — a guess about a lane whose identity the
	// state directory recorded. The walk is therefore confined to the lane root,
	// where a live lane's own pointer still resolves.
	if _, laneRoot, _, _, isLane := r.stateWorktreeOwner(wd, bucketDir); isLane {
		if root := resolveMainRootWithin(wd, laneRoot); root != "" {
			return filepath.Base(root)
		}
		name, _ := r.worktreePathFallback(wd, bucketDir, m.ParentSessionID)
		return name
	}

	// 2. Resolve the main checkout structurally (cached by wd). A label never
	// needs a process: see resolveMainRootStructural.
	if root := r.resolveMainRoot(wd); root != "" {
		return filepath.Base(root)
	}

	// 3. ONLY if wd is structurally a worktree path whose checkout directory has no .git layout to resolve:
	if name, isWorktree := r.worktreePathFallback(wd, bucketDir, m.ParentSessionID); isWorktree {
		return name
	}

	// 4. For any ordinary directory (e.g. /home/jesse/my-notes), return filepath.Base(wd)
	return filepath.Base(wd)
}

func (r *projectNameResolver) worktreePathFallback(wd, bucketDir, parentSessionID string) (string, bool) {
	clean := filepath.Clean(wd)
	sep := string(filepath.Separator)

	// (a) In-repo worktrees (/.worktrees/<name>, /.claude/worktrees/<name>, /.git/worktrees/<name>)
	for _, marker := range []string{
		sep + ".claude" + sep + "worktrees" + sep,
		sep + ".git" + sep + "worktrees" + sep,
		sep + ".worktrees" + sep,
	} {
		if repoDir, _, ok := strings.Cut(clean, marker); ok {
			if base := filepath.Base(repoDir); base != "" && base != "." && base != sep {
				return base, true
			}
			return "", true
		}
	}
	if repoDir, ok := strings.CutSuffix(clean, sep+".worktrees"); ok {
		return filepath.Base(repoDir), true
	}

	// (b) Evener state-dir worktrees. The predicate is the layout itself, not a
	// lookalike: a managed worktree is written at <stateDir>/worktrees/<lane> and
	// a delegate's at <stateDir>/sessions/<sid>/worktrees/<lane>, where stateDir
	// is this bucket or a bucket proven to live under this state root.
	stateDir, _, ownerSessionID, managedProjectID, isStateWorktree := r.stateWorktreeOwner(clean, bucketDir)
	if !isStateWorktree {
		return "", false
	}
	if bucketDir == "" {
		return "", true
	}
	bound := filepath.Clean(bucketDir)

	// Every metadata read below comes from bound: the bucket discovery validated
	// for this row. A recorded path that points into another bucket is still a
	// state worktree — its label must not fall through to the lane leaf — but the
	// other bucket is never read, so neither its metadata nor a symlink it holds
	// can name the project. What remains available is inference inside bound.

	// Resolve the worktree's own owner first: its metadata names the project for
	// this worktree specifically. The answer is deliberately not cached — one
	// state dir holds worktrees from more than one repository, so caching an
	// owner's project would mislabel the next worktree resolved through it.
	if stateDir == bound && ownerSessionID != "" {
		if meta, ok := r.loadOwnedSessionMeta(bound, ownerSessionID); ok {
			if origin := strings.TrimSpace(meta.EnvInfo.GitOriginURL); origin != "" {
				return repoBasename(origin), true
			}
			if restore := strings.TrimSpace(meta.WorktreeRestoreRoot); restore != "" {
				p := filepath.Base(restore)
				if root := r.resolveMainRoot(restore); root != "" {
					p = filepath.Base(root)
				}
				return p, true
			}
			if home := strings.TrimSpace(meta.HomeDir()); home != "" && !strings.Contains(home, "worktrees") {
				if root := r.resolveMainRoot(home); root != "" {
					return filepath.Base(root), true
				}
				return filepath.Base(home), true
			}
		}
	}

	// A managed worktree at <stateDir>/worktrees/<projectID>/<lane> records no
	// owner session, and a delegate's lane at
	// <stateDir>/sessions/<sid>/worktrees/<lane> records one whose metadata can be
	// gone with the lane. Both record their parent, and a lane is created as a
	// worktree of its parent's repository, so a parent in the same bucket as the
	// lane names the project.
	//
	// A parent in a DIFFERENT bucket does not: its metadata describes this
	// bucket's repository, and the lane it names belongs to another one. Evidence
	// has to tie the parent to this lane, which only two things do — the parent is
	// the lane's own recorded owner, or the lane's path records the project and
	// the parent agrees with it. Without either, the label is omitted rather than
	// taken from the bucket the row happened to be discovered in.
	if parentSessionID != "" {
		tiedToLane := stateDir == bound || managedProjectID != "" || parentSessionID == ownerSessionID
		if tiedToLane {
			if parent, ok := r.loadOwnedSessionMeta(bound, parentSessionID); ok {
				if checkout, ok := r.siblingCheckout(parent, bound); ok &&
					(managedProjectID == "" ||
						identifier.ProjectFromCanonicalPath(identifier.ResolveClean(checkout)).ID == managedProjectID) {
					if origin := strings.TrimSpace(parent.EnvInfo.GitOriginURL); origin != "" {
						return repoBasename(origin), true
					}
					return filepath.Base(checkout), true
				}
			}
		}
	}

	// Sibling inference answers only for a worktree with no owner metadata, and
	// it is bucket-wide, so it is trustworthy only where the bucket holds one
	// repository. The test is the same independent identification that makes a
	// path a state bucket: its state home must be the configured one. A flat
	// --state-dir override has no state home at all, and a shared override such
	// as /srv/archive/evener/projects/shared merely spells the layout, so both
	// are skipped; every project bucket under the configured root keeps the
	// fallback, legacy names included.
	//
	// It also answers only for a worktree recorded inside this same bucket. A row
	// discovered in bucket A can record a worktree that lives in bucket B; A's
	// sibling sessions describe A's repository, and naming this worktree after
	// one of them mislabels a session from another repository.
	if stateDir != bound {
		return "", true
	}
	if home := stateHomeFor(bound); home == "" || !r.isConfiguredStateHome(home) {
		return "", true
	}
	// A managed worktree records the project it belongs to in its own path
	// (<stateDir>/worktrees/<projectID>/<lane>). The bucket's own project ID is
	// its basename, because that is where RuntimeDirForProjectWithStateHome
	// writes it: <stateRoot>/projects/<projectID>. Hashing the bucket's path
	// instead answers with a different ID for the same repository, so a lane
	// sitting in its own bucket would read as foreign. A legacy bucket name that
	// is not a project ID leaves the comparison unproven, and an unproven
	// worktree keeps the evidence requirement.
	bucketProjectID := filepath.Base(bound)
	if identifier.ValidateProjectID(bucketProjectID) != nil {
		bucketProjectID = ""
	}
	foreignProject := managedProjectID != "" && managedProjectID != bucketProjectID
	// A managed lane whose project segment IS its bucket's ID matches by name, so
	// the bucket-basename comparison alone cannot tell a lane of this repository
	// from a lane of another one that was pointed at this bucket with --state-dir.
	// The evidence that settles it is the sibling's own recorded checkout: one
	// that resolves to a different project is about another repository, and it
	// must be neither selected nor cached. A sibling that records only an origin,
	// or whose checkout no longer resolves, carries no project evidence either
	// way and still answers.
	excludeConflicting := managedProjectID != "" && !foreignProject
	// A sibling's own checkout is read through the same lane boundary the row
	// itself is, because a sibling's recorded path can be a state lane whose
	// checkout has been deleted. An unbounded walk from it climbs to whichever
	// repository encloses the state home and calls that the sibling's project,
	// which would reject the one surviving record — its saved origin — as
	// conflicting evidence. Confining the walk to the lane root leaves the
	// project unproven there, so the origin answers.
	//
	// A sibling that identifies a different project is about another repository.
	// An ordinary non-Git directory identifies one too: it is its own project, so
	// its basename must not stand in as a label for a lane from somewhere else.
	conflictsWithLane := func(sm schema.SessionMeta) bool {
		if managedProjectID == "" {
			return false
		}
		checkout, ok := r.siblingCheckout(sm, bound)
		if !ok {
			return false
		}
		return identifier.ProjectFromCanonicalPath(identifier.ResolveClean(checkout)).ID != managedProjectID
	}
	// admissible reports whether a sibling may answer for this row. A foreign
	// project needs positive evidence naming it; a bucket-matching managed lane
	// needs the absence of evidence naming a different project.
	admissible := func(sm schema.SessionMeta) bool {
		switch {
		case foreignProject:
			return r.matchesProjectID(sm, managedProjectID, bound)
		case excludeConflicting:
			return !conflictsWithLane(sm)
		default:
			return true
		}
	}
	// The cache carries the eligibility that produced its answer, so a row with
	// different requirements cannot read it. A foreign project's answer also
	// describes that project alone, and an empty answer for a bucket-matching
	// lane may only have been empty because a conflicting sibling was excluded —
	// a later row may have one that matches.
	mode := "open"
	switch {
	case foreignProject:
		mode = "foreign:" + managedProjectID
	case excludeConflicting:
		mode = "lane:" + managedProjectID
	}
	key := bucketCacheKey{dir: bound, mode: mode}
	if name, ok := r.bucketProjectCache[key]; ok {
		return name, true
	}
	// The sibling scan is bucket-wide, so apply discovery's own guard: a
	// sessions/ dir that is a symlink would otherwise be followed.
	if symlinkErrorDeep(schema.SessionsDir(bound), bound) != nil {
		return "", true
	}

	cache := func(name string) {
		if name == "" && (foreignProject || excludeConflicting) {
			return
		}
		r.bucketProjectCache[key] = name
	}

	// Scan bucket sessions in two passes:
	// Pass 1: authoritative GitOriginURL or WorktreeRestoreRoot
	metas, err := schema.ListSessionMetas(bound)
	if err == nil {
		for _, sm := range metas {
			if !admissible(sm) {
				continue
			}
			if origin := strings.TrimSpace(sm.EnvInfo.GitOriginURL); origin != "" {
				p := repoBasename(origin)
				cache(p)
				return p, true
			}
			if restore := strings.TrimSpace(sm.WorktreeRestoreRoot); restore != "" {
				checkpoint, ok := r.siblingCheckout(sm, bound)
				if !ok {
					// A state lane whose checkout is gone names no project. Its
					// recorded path ends in a lane name, which is not a project
					// label, so keep looking rather than emit it.
					if _, _, _, _, isLane := r.stateWorktreeOwner(filepath.Clean(restore), bound); isLane {
						continue
					}
				}
				p := filepath.Base(restore)
				if ok {
					p = filepath.Base(checkpoint)
				}
				cache(p)
				return p, true
			}
		}
		// Pass 2: ordinary home directory, resolved via resolveMainRoot
		for _, sm := range metas {
			if !admissible(sm) {
				continue
			}
			home := strings.TrimSpace(sm.HomeDir())
			if home == "" {
				home = strings.TrimSpace(sm.EnvInfo.WorkingDir)
			}
			if home != "" && !strings.Contains(home, "worktrees") {
				if checkpoint, ok := r.siblingCheckout(sm, bound); ok {
					p := filepath.Base(checkpoint)
					cache(p)
					return p, true
				}
				p := filepath.Base(home)
				cache(p)
				return p, true
			}
		}
	}

	cache("")
	return "", true
}

// matchesProjectID reports whether a sibling session's own recorded project is
// the project a managed worktree's path names. It is the evidence a bucket-wide
// sibling scan needs when the worktree belongs to a different repository than
// the bucket: without it, this bucket's sessions describe the wrong repository
// and their label would be a cross-repository guess. bucketDir is where the
// sibling's metadata was read from, so a recorded lane is resolved against the
// same boundary as any other row in that bucket.
func (r *projectNameResolver) matchesProjectID(m schema.SessionMeta, projectID, bucketDir string) bool {
	checkout, ok := r.siblingCheckout(m, bucketDir)
	if !ok {
		return false
	}
	return identifier.ProjectFromCanonicalPath(identifier.ResolveClean(checkout)).ID == projectID
}

// siblingCheckout resolves a session's own recorded checkout the way the row
// being labelled is resolved: through the lane boundary when the recorded path
// is a state-managed lane. A lane whose checkout is gone must not be walked
// past, because the walk would answer with whichever repository encloses the
// state directory — and that answer would then stand as evidence about the
// sibling's project. With the walk confined, a deleted lane has no project
// evidence rather than false evidence, so its saved origin stays authoritative.
//
// ok distinguishes "this checkout identifies a project" from "it identifies
// none". An ordinary directory identifies its own project even without a .git:
// it is where the session ran, so its canonical path is the project. A state
// lane identifies none once its checkout is gone, which is exactly the case
// where no evidence exists either way.
//
// The recorded restore root wins over the home directory, matching
// canonicalProjectPath.
func (r *projectNameResolver) siblingCheckout(m schema.SessionMeta, bucketDir string) (string, bool) {
	recorded := strings.TrimSpace(m.WorktreeRestoreRoot)
	if recorded == "" {
		recorded = strings.TrimSpace(m.HomeDir())
	}
	if recorded == "" {
		return "", false
	}
	clean := filepath.Clean(recorded)
	if _, laneRoot, _, _, isLane := r.stateWorktreeOwner(clean, bucketDir); isLane {
		if root := resolveMainRootWithin(clean, laneRoot); root != "" {
			return root, true
		}
		return "", false
	}
	if root := r.resolveMainRoot(clean); root != "" {
		return root, true
	}
	if exists, err := os.Stat(clean); err == nil && exists.IsDir() {
		// No .git anywhere above it, but the directory is really there: it is its
		// own project, and that project is what its canonical path names.
		return identifier.ResolveClean(clean), true
	}
	return "", false
}

// stateWorktreeOwner classifies wd against the layouts an Evener state dir
// actually writes: <stateDir>/worktrees/<lane> for a managed worktree,
// <stateDir>/worktrees/<projectID>/<lane> for a managed one, and
// <stateDir>/sessions/<sid>/worktrees/<lane> for a delegate's. It returns the
// state dir wd sits in, the session whose metadata names the project, and the
// managed worktree's project segment. ok is false for every other path —
// including an ordinary directory that merely spells a "worktrees" segment.
//
// A path is only a state worktree when its directory is independently identified
// as a state bucket (isStateBucket). A workspace that merely spells the layout is
// not one, so wd keeps its own basename instead of being read as a state tree.
func (r *projectNameResolver) stateWorktreeOwner(wd, bucketDir string) (stateDir, laneRoot, ownerSessionID, managedProjectID string, ok bool) {
	sep := string(filepath.Separator)
	clean := filepath.Clean(wd)
	parts := strings.Split(clean, sep)
	cleanBucket := filepath.Clean(bucketDir)
	for i := len(parts) - 1; i >= 1; i-- {
		// A lane must follow the worktrees segment: <lane>-less is not a worktree.
		if parts[i] != "worktrees" || i+1 >= len(parts) {
			continue
		}
		stateParts, owner, managed := parts[:i], "", ""
		laneEnd := i + 2 // <stateDir>/worktrees/<lane>
		switch {
		case i >= 2 && parts[i-2] == "sessions":
			stateParts, owner = parts[:i-2], parts[i-1]
		case i+2 < len(parts) && identifier.ValidateProjectID(parts[i+1]) == nil:
			// <projectID>/<lane>: the segment is a project identity only when the
			// path continues past it, so <stateDir>/worktrees/<lane> stays ownerless.
			managed = parts[i+1]
			laneEnd = i + 3 // <stateDir>/worktrees/<projectID>/<lane>
		}
		candidate := strings.Join(stateParts, sep)
		if !r.isStateBucket(candidate, cleanBucket) {
			continue
		}
		return candidate, strings.Join(parts[:laneEnd], sep), owner, managed, true
	}
	return "", "", "", "", false
}

// isStateBucket reports whether candidate is independently identified as a state
// bucket: the bucket this row was discovered in, or a bucket whose state home —
// the one stateHomeFor reads off the <stateHome>/evener/projects/<name> layout —
// is the home this process computes from its own configuration (stateRoots).
//
// Identification decides only whether a path is a state worktree at all. It does
// not grant a metadata read: worktreePathFallback reads from the row's own
// validated bucket and nowhere else.
func (r *projectNameResolver) isStateBucket(candidate, bucket string) bool {
	if candidate == "" || !filepath.IsAbs(candidate) {
		return false
	}
	if candidate == bucket {
		return true
	}
	home := stateHomeFor(candidate)
	if home == "" {
		return false
	}
	return r.isConfiguredStateHome(home)
}

// isConfiguredStateHome reports whether home is the state home this process
// computes from its own configuration. It is the one independent
// identification of the state tree, so a path that merely spells the layout
// under some other archive root is not part of it.
//
// Both spellings of that home are accepted: the configured one and its
// canonical form. When $XDG_STATE_HOME is itself a symlink, startup records
// buckets under the physical path while the configuration keeps the link's
// spelling, and comparing only one form would reject a genuine project bucket.
// Both sides still come from this process's own configuration, so accepting
// either spelling does not widen what counts as the state tree.
func (r *projectNameResolver) isConfiguredStateHome(home string) bool {
	configuredHome := filepath.Clean(home)
	canonicalHome := identifier.ResolveClean(configuredHome)
	for _, root := range r.stateRoots() {
		rootHome := filepath.Dir(root)
		if configuredHome == rootHome || canonicalHome == identifier.ResolveClean(rootHome) {
			return true
		}
	}
	return false
}

// stateRoots returns the Evener state roots this process is configured with:
// $XDG_STATE_HOME/evener, else ~/.local/state/evener. They are computed here
// rather than inferred from the path under test, which is what makes them an
// independent identification of the state tree.
func (r *projectNameResolver) stateRoots() []string {
	if r.stateRootCache != nil {
		return r.stateRootCache
	}
	r.stateRootCache = []string{}
	if root, err := userdirs.DefaultStateRoot(); err == nil && root != "" {
		r.stateRootCache = append(r.stateRootCache, filepath.Clean(root))
	}
	return r.stateRootCache
}

// loadOwnedSessionMeta reads a worktree owner's SessionMeta only when the meta
// path is proven safe to open: a regular file, not a symlink, under a bucket in
// the configured state root. A FIFO there must never be opened — LoadSessionMeta
// deliberately blocks on one — so the check is a non-blocking Lstat performed
// before the read, and a path that fails it is simply not consulted.
func (r *projectNameResolver) loadOwnedSessionMeta(stateDir, sessionID string) (schema.SessionMeta, bool) {
	if identifier.ValidateSessionID(sessionID) != nil {
		return schema.SessionMeta{}, false
	}
	metaPath := schema.SessionMetaPath(stateDir, sessionID)
	if !existsNonSymlink(metaPath, stateDir) {
		return schema.SessionMeta{}, false
	}
	meta, err := schema.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		return schema.SessionMeta{}, false
	}
	return meta, true
}

func (r *projectNameResolver) resolveMainRoot(dir string) string {
	if root, ok := r.mainRootCache[dir]; ok {
		return root
	}
	root := resolveMainRootStructural(dir)
	r.mainRootCache[dir] = root
	return root
}

// resolveMainRootStructural resolves dir's main checkout from the .git layout
// alone, never by running Git.
//
// Transcript search is read-only and its inputs are workspace-controlled: a
// recorded restore root can sit inside a repository the user checked out, and
// the command runner prepends workspace directories to PATH, so any `git`
// invocation here would run a repository-supplied executable with the daemon's
// permissions. None of that is acceptable for a label, so this walk has no
// fall-through at all: it answers, or it returns "" and the caller uses the
// recorded directory's own name. Each ".git" shape is enumerated here.
//
//   - .git directory, or a symlink whose target is a directory: the checkout's
//     own Git directory, so the directory is its own main root. os.Stat follows
//     the link, so a symlink to a directory arrives as the directory case.
//   - regular pointer file, or a symlink to one: a linked-worktree pointer
//     names the main checkout, a submodule pointer names this checkout. Both
//     are validated against the layout before either answer is used.
//   - symlink that does not resolve, FIFO, socket, device: "" — a refusal, not
//     an invitation to try something else.
//   - pointer larger than any pointer, or unreadable: "" for the same reason.
//   - no .git in this directory or any ancestor: "".
//
// Every branch returns; nothing here can leave the walk to a second resolver.
func resolveMainRootStructural(dir string) string {
	clean := filepath.Clean(dir)
	for {
		if root, found := mainRootAt(clean); found {
			return root
		}
		parent := filepath.Dir(clean)
		if parent == clean {
			return ""
		}
		clean = parent
	}
}

// resolveMainRootWithin is resolveMainRootStructural confined to root: it asks
// root and the directories beneath it and never climbs past root. A
// state-managed lane stops here, because the lane's identity is recorded in the
// state directory, not in whichever repository happens to enclose it: once the
// lane's checkout is deleted it has no .git to stop an unbounded walk, and the
// walk would climb past the lane root and answer with the enclosing checkout.
//
// A live lane still resolves, because its own .git pointer names the main
// checkout and that pointer is read here.
func resolveMainRootWithin(dir, root string) string {
	clean := filepath.Clean(dir)
	root = filepath.Clean(root)
	for {
		if resolved, found := mainRootAt(clean); found {
			return resolved
		}
		if clean == root {
			return ""
		}
		parent := filepath.Dir(clean)
		if parent == clean {
			return ""
		}
		clean = parent
	}
}

// mainRootAt answers for one directory from its own .git entry. found is false
// only when that entry is absent: an entry that exists and is refused answers
// with the empty root and found true, so no caller climbs past a checkout whose
// shape has already been decided. Absence is os.IsNotExist, never any other
// error, because a read that failed did not establish that nothing is there.
func mainRootAt(dir string) (root string, found bool) {
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Lstat(gitPath)
	switch {
	case err != nil:
		return "", !os.IsNotExist(err)
	case info.IsDir():
		return identifier.ResolveClean(dir), true
	case info.Mode()&os.ModeSymlink != 0:
		// A symlink is followed once, here, so the two shapes a legitimate link
		// can hold are decided explicitly: a directory (a relocated .git
		// directory) and a regular pointer file. Anything else it resolves to —
		// or a link that resolves to nothing — is refused.
		target, evalErr := filepath.EvalSymlinks(gitPath)
		if evalErr != nil {
			return "", true
		}
		targetInfo, statErr := os.Stat(target)
		if statErr != nil {
			return "", true
		}
		if targetInfo.IsDir() {
			return identifier.ResolveClean(dir), true
		}
		if !targetInfo.Mode().IsRegular() {
			return "", true
		}
		return mainRootFromPointerFile(dir, gitPath), true
	default:
		return mainRootFromPointerFile(dir, gitPath), true
	}
}

// mainRootFromPointerFile answers for a regular ".git" pointer file: a
// linked-worktree pointer names the main checkout, a submodule pointer names
// this checkout, and anything else is "". The read is bounded and nonblocking
// and never runs a process; a refusal is final here and is never passed to a
// second resolver.
func mainRootFromPointerFile(dir, gitPath string) string {
	content, readErr := execenv.ReadGitPointerFile(gitPath)
	if readErr != nil {
		return ""
	}
	pointer := string(content)
	if root, ok := identifier.MainRootFromGitdirPointer(pointer, dir); ok {
		if identifier.ValidateLinkedWorktreePointer(pointer, dir, root) == nil {
			return identifier.ResolveClean(root)
		}
		return ""
	}
	if _, ok := identifier.ParseGitdirPointer(pointer); ok {
		if identifier.ValidateSubmodulePointer(pointer, dir) != nil {
			return ""
		}
		return identifier.ResolveClean(dir)
	}
	return ""
}

// repoBasename returns the final path segment of a git origin URL, stripping a
// trailing ".git". It handles both scp-style ("git@host:owner/repo.git") and URL
// forms by splitting on both "/" and ":".
func repoBasename(origin string) string {
	origin = strings.TrimSuffix(origin, "/")
	origin = strings.TrimSuffix(origin, ".git")
	if i := strings.LastIndexAny(origin, "/:"); i >= 0 {
		origin = origin[i+1:]
	}
	return origin
}

// turnRoleLabel maps a turn kind to the lowercased role label used in snippets.
func turnRoleLabel(kind schema.TurnKind) string {
	switch kind {
	case schema.TurnUserInput:
		return "user"
	case schema.TurnAssistant:
		return "assistant"
	case schema.TurnToolResults, schema.TurnTool:
		return "tool_result"
	case schema.TurnSteering:
		return "steering"
	case schema.TurnSummary:
		return "summary"
	case schema.TurnCheckpoint:
		return "checkpoint"
	case schema.TurnSystem:
		return "system"
	default:
		return strings.ToLower(string(kind))
	}
}

// rawEntryText concatenates the FULL, un-summarized searchable text of a turn:
// assistant text, thinking, full tool-call arguments (NOT toolInputSummary), and
// full tool-result bodies. Used by the cross-session content scan so a query
// appearing only in a long command tail or a written file body IS found.
func rawEntryText(t schema.Turn) string {
	if !t.Kind.PublicTranscript() {
		return ""
	}
	var parts []string
	for i := range t.Message.Content {
		p := &t.Message.Content[i]
		switch p.Kind {
		case llm.ContentText:
			if p.Text != "" {
				parts = append(parts, p.Text)
			}
		case llm.ContentThinking:
			if p.Thinking != nil && p.Thinking.Text != "" {
				parts = append(parts, p.Thinking.Text)
			}
		case llm.ContentToolCall:
			if p.ToolCall != nil {
				parts = append(parts, p.ToolCall.Name, string(p.ToolCall.Arguments))
			}
		case llm.ContentToolResult:
			if p.ToolResult != nil {
				parts = append(parts, fmt.Sprint(p.ToolResult.Content))
			}
		}
	}
	return strings.Join(parts, "\n")
}

// makeSnippet returns a ~width-char excerpt of text centered on the first
// case-insensitive occurrence of query, with ellipses where the excerpt is
// clipped. Newlines are collapsed to spaces so the snippet stays one line. When
// query is not found (only reachable via odd callers), the leading width chars
// are returned.
func makeSnippet(text, query string, width int) string {
	flat := strings.Join(strings.Fields(text), " ")
	lower := strings.ToLower(flat)
	idx := strings.Index(lower, strings.ToLower(query))
	if idx < 0 {
		return truncRunes(flat, width)
	}

	runes := []rune(flat)
	// Convert the byte index into a rune index for safe slicing. idx is a byte
	// offset into lower, which may differ in byte length from flat (ToLower can
	// re-encode a rune to a different byte count, e.g. an invalid byte → RuneError),
	// so count runes in lower's prefix — ToLower preserves rune count, so the rune
	// index is valid for flat's rune slice.
	matchRune := len([]rune(lower[:idx]))
	qLen := len([]rune(query))

	half := max((width-qLen)/2, 0)
	start := max(matchRune-half, 0)
	end := start + width
	if end > len(runes) {
		end = len(runes)
		start = max(end-width, 0)
	}

	excerpt := string(runes[start:end])
	if start > 0 {
		excerpt = "…" + excerpt
	}
	if end < len(runes) {
		excerpt += "…"
	}
	return excerpt
}
