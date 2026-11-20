package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
// that have a readable transcript on disk. bucketMetas, when supplied, is the
// metadata discovery already read, which the project-name resolver reuses.
func recordsUpTo(candidates []findCandidate, snipsFor func(c findCandidate) []snippet, currentID string, limit int, currentMeta func() schema.SessionMeta, bucketMetas ...map[string][]schema.SessionMeta) []sessionRecord {
	resolver := newProjectNameResolver(bucketMetas...)
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

	candidates, bucketMetas := collectCandidates(buckets, deps.stateDir)
	// Metadata-only filters run before any transcript is opened, so a filtered
	// candidate never consumes the content-scan budget.
	candidates = filterCandidates(candidates, filters, currentID, deps.currentMeta)
	sortCandidatesNewestFirst(candidates, currentID)

	var records []sessionRecord
	var scanned *int
	var scanTruncated *bool

	if query == "" {
		// Catalog: metadata-only, no scan.
		records = recordsUpTo(candidates, func(_ findCandidate) []snippet { return nil }, currentID, limit, deps.currentMeta, bucketMetas)
	} else {
		// Content-search: track coverage.
		resolver := newProjectNameResolver(bucketMetas)
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

	candidates, bucketMetas := collectCandidates([]string{bucketDir}, deps.stateDir)
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
	records := recordsUpTo(children, func(findCandidate) []snippet { return nil }, currentID, limit, deps.currentMeta, bucketMetas)

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
//
// bucketMetas is the metadata as read, keyed by bucket. It is returned rather
// than discarded because the project-name resolver needs exactly this data to
// infer a label from siblings: handing it over keeps a search from decoding the
// same bucket once per labelled row.
func collectCandidates(buckets []string, currentStateDir string) ([]findCandidate, map[string][]schema.SessionMeta) {
	currentAbs, _ := filepath.Abs(currentStateDir)
	// Validate the layout prefix for the current bucket: it enters this
	// function directly (not via enumerateBuckets, which already validates
	// the prefix for sibling buckets). symlinkErrorDeep below is rooted at
	// the bucket, so ancestors above it (evener/, evener/projects/) are not
	// checked. A symlinked evener/ within the state root would let metas
	// from outside the state root surface in find + children_of results.
	currentPrefixOK := validateLayoutPrefix(currentStateDir) == nil
	var out []findCandidate
	bucketMetas := make(map[string][]schema.SessionMeta)
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
		bucketMetas[bucket] = metas
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
	return out, bucketMetas
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

// mainRootResult is one directory's complete structural answer. The refusal is
// stored as its own field, not folded into an empty root, because "no repository
// here" and "a repository here that could not be read" are different answers and
// only the first licenses treating the directory as a project of its own.
type mainRootResult struct {
	root    string
	refused bool
}

type projectNameResolver struct {
	stateRootCache []string
	// bucketMetas is the session metadata candidate discovery already read for
	// each bucket. The sibling scan needs every row in the bucket, and discovery
	// has just read all of them; rereading them once per labelled row decodes the
	// same directory once per result. It is discovery's snapshot, not a cache this
	// resolver maintains, so a bucket that did not take part in the search is
	// still read when it is needed.
	bucketMetas map[string][]schema.SessionMeta
	// siblingEvidence caches each bucket's SIBLING evidence — the candidate
	// answers its sessions produce — for the life of one search. It is not a
	// label cache: eligibility is a property of the row asking, and is applied to
	// this evidence per row rather than baked into it. What it saves is the
	// filesystem work of resolving every sibling's recorded checkout once per
	// bucket instead of once per result row.
	siblingEvidence map[siblingEvidenceKey][]projectEvidence
	// pathResolutions counts the recorded paths this resolver turned into project
	// evidence. It is diagnostic, not bookkeeping: two orders of the same evidence
	// produce the same LABEL, and only the work differs. A test that has to prove a
	// lookup avoided a filesystem walk — the walk an unavailable mount stalls —
	// asserts on this rather than on the answer, which cannot tell the two orders
	// apart.
	pathResolutions int
	// mainRoots is each directory's complete structural answer, for the life of
	// one search. A search labels its rows in one loop, so a plain map is enough.
	mainRoots map[string]mainRootResult
	// mainRootLookups counts the structural walks this resolver ran. It is
	// diagnostic, not bookkeeping: a repeated walk rewrites the same mainRoots
	// key, so the map's length cannot tell a reused answer from a repeated walk,
	// and the LABEL cannot tell them apart either. A test that has to prove the
	// walk was reused asserts on this.
	mainRootLookups int
}

// siblingEvidenceKey names one bucket's sibling snapshot. needProjectID is part
// of the identity because it decides what the snapshot contains: the form that
// resolves each sibling's recorded path carries project IDs, and the form that
// does not carries labels only. They are not interchangeable.
type siblingEvidenceKey struct {
	bucketDir     string
	needProjectID bool
}

// bucketKind classifies the state directory a row was discovered in. Membership
// in the state tree is proven by the configured state root, never by a path's
// spelling, and the four outcomes are the only four the rest of the resolver
// needs to distinguish.
type bucketKind int

const (
	// bucketUnrelated is not under the configured state root at all: a flat
	// --state-dir, an unrelated shared override, or an ordinary directory. Its
	// sessions can be from any repository, so bucket-wide inference is never
	// sound here.
	bucketUnrelated bucketKind = iota
	// bucketLegacy is under the configured state root but does not encode a
	// project ID in its name, which older state dirs still look like. It is a
	// recognized project bucket — one repository per bucket — so origin-only
	// sibling evidence still answers. The project ID itself is unknown, and
	// unknown is not a conflict.
	bucketLegacy
	// bucketShared is the shared-override name under the configured state root: a
	// directory where sessions from several repositories are deliberately pooled,
	// which is what pointing --state-dir at one is for. It is part of the state
	// tree, so it stays searchable and every row-specific record in it still
	// answers, but nothing about it says its sessions served ONE repository, so
	// an unverified candidate there may not speak for a row.
	bucketShared
	// bucketProject is under the configured state root with its project ID in its
	// basename, which is where RuntimeDirForProjectWithStateHome writes it.
	bucketProject
)

// sharedBucketName is the basename the state tree reserves for a shared
// override: <stateHome>/evener/projects/shared. It is a name, not a layout: the
// directory sits exactly where a project bucket does, and only the name says
// that its sessions were pooled from more than one repository.
const sharedBucketName = "shared"

// projectEvidence is one candidate answer for a row's project label, with the
// strength of the evidence that produced it. The resolver collects every
// candidate it can reach and returns the strongest. A candidate that is refused
// or unreadable contributes nothing at all — it is no evidence about this row's
// project, never evidence against it, so it must not veto the others.
//
// projectID is the project the source itself names, or "" when the source
// carries no project identity (a saved Git origin does not). It is what decides
// whether the candidate is admissible for a lane whose own path records a
// project.
type projectEvidence struct {
	name      string
	projectID string
	rank      int
}

// The evidence ranking, strongest first. Lower ranks win.
const (
	// The project ID recorded in a managed lane's own path. It outlives the
	// checkout, so it is the strongest record of which project a lane served —
	// as the filter that admits or refuses the other candidates.
	evidenceRankRecorded = 10
	// The worktree owner's own metadata: the session this worktree was made for.
	// Its saved origin is authoritative and is returned before ranking, so it has
	// no rank here; its restore root is next and a bare home directory the weakest
	// thing it can say.
	evidenceRankOwnerRestore   = 21
	evidenceRankOwnerHome      = 22
	evidenceRankParentOrigin   = 30
	evidenceRankParentCheckout = 31
	// A sibling's saved origin outranks its recorded restore root, which
	// outranks a main checkout resolved from its Git pointer. That is the order
	// the two-pass scan established: an explicit record of the repository beats a
	// directory that merely happens to resolve to one.
	evidenceRankSiblingOrigin   = 40
	evidenceRankSiblingRestore  = 41
	evidenceRankSiblingCheckout = 42
	// The path's own basename, which says nothing about projects at all.
	evidenceRankBasename = 50
)

// newProjectNameResolver builds a resolver for one search. bucketMetas is the
// metadata candidate discovery already read, when the caller has it: the sibling
// scan needs every row in a bucket, and passing it in keeps the search from
// decoding the same bucket once per labelled row.
func newProjectNameResolver(bucketMetas ...map[string][]schema.SessionMeta) *projectNameResolver {
	// The sibling-evidence map is allocated HERE, with the resolver, rather than
	// lazily on the success path. It has two write sites — the symlink refusal and
	// the completed scan — and a lazy allocation before only one of them left the
	// other writing to a nil map, which panics rather than merely mislabelling.
	// Allocating once at construction removes the ordering dependency instead of
	// adding a second early return that a later edit can miss.
	r := &projectNameResolver{
		siblingEvidence: make(map[siblingEvidenceKey][]projectEvidence),
		mainRoots:       make(map[string]mainRootResult),
	}
	if len(bucketMetas) > 0 {
		r.bucketMetas = bucketMetas[0]
	}
	return r
}

// Project-label precedence.
//
// The label is chosen from several sources, and the order among them is one
// rule, applied at every site that reads evidence. Stated once, here, because
// each site below is an instance of it rather than a rule of its own:
//
//  1. What the ROW itself states: its own saved origin, then its own record's
//     project ID (a managed lane's path), then a live checkout it names.
//  2. What a record the row is TIED TO states: its owner, then its verified
//     parent. Row-specific evidence must name the lane's project when the lane
//     records one.
//  3. Bucket-wide inference from siblings. It describes the bucket, not the
//     lane, so it only answers once nothing row-specific did.
//  4. The path's own basename.
//
// Two properties hold at every site:
//
// LOCAL EVIDENCE OUTRANKS A REMOTE READ. A saved origin is a string in the
// metadata; resolving a recorded path is a filesystem call that can block on an
// unavailable mount. A site that needs only a name reads the string first and
// walks only if that fails. A site that needs a project ID has to walk, because
// the ID is what the walk produces.
//
// VERIFIED EVIDENCE OUTRANKS UNVERIFIED. A candidate that states a project ID
// can be checked against the lane's; one that states none cannot, and so may
// only answer where the bucket has independently verified per-project scope and
// the row has no project of its own to contradict. A row-specific source that
// can be verified always beats a bucket-wide source that cannot.
func (r *projectNameResolver) projectName(m schema.SessionMeta, bucketDir string) string {
	// An origin whose last segment is empty once ".git" is stripped names no
	// repository, so it is no evidence. Returning it would answer the row with an
	// empty name and skip every field after it that still can.
	if origin := strings.TrimSpace(m.EnvInfo.GitOriginURL); origin != "" {
		if name := repoBasename(origin); name != "" {
			return name
		}
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
	//
	// The alias is resolved BEFORE that decision, because it is what chooses the
	// boundary. A recorded working directory can be a symlink to a lane whose own
	// directory survives; classified by the alias, the path is not a lane at all,
	// so the walk would be unbounded, would climb out of the lane, and would name
	// the repository enclosing the state directory before the verified parent was
	// consulted. The same resolved form is what the bounded walk and the metadata
	// fallback below both see.
	//
	// Every recorded path is tried for that classification, not just the display
	// one. A session launched through a symlink to its lane records the symlink as
	// its working directory and the lane itself as its worktree path; once the lane
	// is removed the alias dangles, and reading only the alias would walk Git from
	// a name that resolves nowhere and label the transcript with whatever encloses
	// it. The lane that the record names is the identity, so it is found wherever
	// the record kept it.
	//
	// A lane that answers nothing is REMEMBERED, not returned from. The record
	// carries the root the session entered from as well, and a pruned lane whose
	// checkout survives names the project through that root. Answering "" for the
	// lane would discard a field that still says which project the session
	// belonged to, which is the same mistake as letting a field that establishes
	// nothing veto the ones after it.
	//
	// A recorded path that is not absolute names no checkout at all: it would be
	// walked relative to this process's directory, which is not where the session
	// ran. Records are written absolute; one that is not is no evidence here.
	laneSeen := false
	lanePath := ""
	laneProjectID := ""
	for _, recorded := range recordPaths(m) {
		if !filepath.IsAbs(recorded) {
			continue
		}
		recorded = identifier.ResolveClean(recorded)
		if _, laneRoot, _, managedProjectID, isLane := r.stateWorktreeOwner(recorded, bucketDir); isLane {
			if root := resolveMainRootWithin(recorded, laneRoot); root != "" {
				return filepath.Base(root)
			}
			if !laneSeen {
				laneSeen = true
				laneProjectID = managedProjectID
				// The path is REMEMBERED, not resolved. Its fallback is bucket-wide
				// inference — an origin an owner, a parent or a sibling saved — and
				// resolving those candidates walks recorded paths. The row still has
				// fields after this one, and its own record may still name the
				// project; asking the fallback here would do that work before either
				// question was put, and the work is what an unavailable mount stalls.
				lanePath = recorded
			}
			continue
		}
		// An ordinary recorded path answers only while the directory it names is
		// still there. Its Git ancestry describes the space the directory occupied,
		// and once it is gone that space belongs to whatever encloses it — an
		// affiliation the directory never had. projectAt is where that rule lives,
		// so this asks it rather than testing existence again here.
		if checkout, _, ok, _, hasGitRoot := r.projectAt(recorded, bucketDir); ok && checkout != "" {
			// A surviving directory that resolves to no Git root is its own project,
			// and its basename is that project's name — unless the path spells an
			// in-repository worktree. The Git metadata that named the repository is
			// what a worktree directory leans on, and when the worktree's own pointer
			// AND the enclosing repository's .git are both gone, the directory still
			// sits where the worktree was. Its leaf is a lane name, so the marker
			// fallback answers instead, exactly as it does for the same path when the
			// record reaches step 3.
			//
			// Only the LABEL changes here. The canonical directory stays the project
			// identity for sibling eligibility, because a hash of the surviving
			// directory is what a lane under it records, and the display name is not
			// part of that comparison.
			if !hasGitRoot {
				if name, isWorktree := inRepoWorktreeProject(checkout); isWorktree {
					return name
				}
			}
			return filepath.Base(checkout)
		}
	}
	if laneSeen {
		// The lane answered nothing on disk, but the record also carries the root
		// the session entered from. When that root is an ordinary directory whose
		// recorded absolute path hashes to the same project ID the lane's own path
		// carries, the recording identifies the lane's project and its basename is
		// the label. This is the same claim the sibling and parent scans accept, so
		// refusing it only here loses a label the rest of the resolver keeps.
		//
		// It is asked BEFORE the lane fallback because the two are not the same kind
		// of claim. A matching restore root is the row's own record positively
		// identifying the project its lane path names. The fallback is bucket-wide
		// inference — an origin another session in the bucket saved — and a bucket
		// that has served more than one repository still holds that origin after the
		// sibling that saved it is gone. Letting the unverified inference answer first
		// is what replaced a label backed by the row's own record with an unrelated
		// sibling's. The fallback still answers when no matching evidence supplies a
		// name, which is what keeps a lane whose only evidence is a sibling labelled.
		//
		// The lane's own leaf stays out of the result: it is a lane name, and only
		// an ordinary restore root names a repository. A lane with no recorded
		// project has nothing to match against, so it keeps the empty answer.
		if laneProjectID != "" {
			if gone, projectID := r.recordedOrdinaryProject(m, bucketDir); projectID != "" && projectID == laneProjectID {
				return filepath.Base(gone)
			}
		}
		// Only now, with every field of the row's own record asked and none of
		// them answering, does bucket-wide inference run. It is the weakest
		// evidence the resolver has, so it answers last rather than first.
		laneFallback, _ := r.worktreePathFallback(lanePath, bucketDir, m.ParentSessionID)
		return laneFallback
	}

	// 2. Resolve the main checkout from the .git layout. A label never needs a
	// process: see mainRootOutcomeClean. Gated on the directory existing for the
	// reason step 1 gives: a walk from a path that is gone describes its
	// surroundings, not the checkout that used to sit there.
	//
	// It is gated on being absolute for the reason step 1 gives, and that reason
	// applies to wd even though step 1 skipped it: a relative record names no
	// checkout of its own. Walking "." resolves against THIS process, so a legacy
	// row that saved --dir . would take the label of whichever repository the
	// search happens to be running in.
	if filepath.IsAbs(wd) && directoryExists(wd) {
		if root := r.resolveMainRoot(wd); root != "" {
			return filepath.Base(root)
		}
	}

	// 3. ONLY if wd is structurally a worktree path whose checkout directory has no .git layout to resolve:
	if name, isWorktree := r.worktreePathFallback(wd, bucketDir, m.ParentSessionID); isWorktree {
		return name
	}

	// 4. For any ordinary directory (e.g. /home/jesse/my-notes), return
	// filepath.Base(wd). A record that is not absolute names no checkout, which is
	// the rule steps 1 and 3 already apply to every recorded path; answering "."
	// here would label the row with a directory name that is not a name at all,
	// and one that every relative record shares.
	if !filepath.IsAbs(wd) {
		return ""
	}
	return filepath.Base(wd)
}

// inRepoWorktreeProject answers the label an IN-REPOSITORY worktree path spells.
// It is matched against the path's spelling, never against anything on disk, so
// its callers must have established that the path is meaningful on its own.
//
// It is one function because it is one rule asked in two places, and the two
// answers must not drift: the worktree fallback asks it for a state-dir row, and
// projectName asks it for a surviving ordinary directory that has no Git root of
// its own. A worktree directory whose Git metadata and whose enclosing
// repository's Git metadata are both gone is still the repository's worktree as
// far as the record shows, and its leaf is a lane name, not a project.
//
// isWorktree reports that a marker matched at all, including when the prefix
// before it has no usable basename: the path IS a worktree path, so the caller
// must not fall through to a basename that is a lane name.
func inRepoWorktreeProject(clean string) (name string, isWorktree bool) {
	sep := string(filepath.Separator)
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
	return "", false
}

// worktreePathFallback answers a state worktree's project label. It evaluates
// every candidate interpretation of the evidence independently and returns the
// strongest: returning on the first structural match would let a candidate that
// is rejected take the still-valid evidence down with it.
//
// Evidence, strongest first:
//
//  1. the project ID recorded in a managed lane's own path — outlives the
//     checkout, and filters every weaker candidate by project identity
//  2. the worktree owner's metadata: its saved origin, then its restore root
//  3. the verified parent row's saved origin, then its resolved checkout
//  4. a sibling session's saved origin, then its resolved main checkout
//  5. the path's own basename, which says nothing about projects
//
// A candidate that is absent, unreadable or refused is NO evidence: it neither
// answers nor vetoes the others. Only a candidate that positively names a
// different project than the lane's recorded one is excluded, and it is excluded
// as an inadmissible answer rather than as a reason to stop looking.
//
// Admissibility is decided before rank, and the two questions are separate: a
// candidate that cannot name a project is not a rival to one that can, and an
// empty answer is never admitted as a match. What that requires depends on what
// the evidence claims. Row-specific evidence — a parent row — is a claim about
// this lane, so for a lane whose path records a project it must positively
// identify that project. Bucket-wide evidence — a sibling — describes the
// bucket and not the lane, so a sibling that records only an origin still
// answers; that fallback is why deleting a worktree does not lose its label.
// Ranking then decides only among candidates that were already admissible.
func (r *projectNameResolver) worktreePathFallback(wd, bucketDir, parentSessionID string) (string, bool) {
	// A relative record names no checkout: the markers below are matched against
	// the path's SPELLING, so `old-repo/.worktrees/lane` would report `old-repo`
	// as a project from a directory this process has never seen. Absoluteness is
	// tested before the markers rather than only at the basename fallback, since
	// the marker branches return early.
	if !filepath.IsAbs(wd) {
		return "", false
	}
	// The path is resolved before it is classified. A recorded working directory
	// can be a symlink into a checkout, and its lexical parents then describe the
	// symlink rather than the repository: a Git walk from the alias climbs the
	// alias's parents, misses the repository's .git, and the later canonicalization
	// hands the subdirectory a project of its own. Classifying and walking where
	// the path really points is what makes the two agree.
	clean := identifier.ResolveClean(wd)

	// (a) An Evener state-managed lane is classified FIRST, from the state
	// directory that records it, and never falls through to the ancestry-based
	// markers below. Those markers answer from the path's spelling: a state dir
	// that happens to sit inside a repository's own checkout — say under
	// /repo/.worktrees/lane/... — contains an ancestral ".worktrees" segment for
	// every lane in it, so the marker loop would name the enclosing repository
	// before the lane's verified parent could name the lane's own project.
	stateDir, _, ownerSessionID, managedProjectID, isStateWorktree := r.stateWorktreeOwner(clean, bucketDir)

	// (b) In-repo worktrees (/.worktrees/<name>, /.claude/worktrees/<name>, /.git/worktrees/<name>)
	// are considered only for paths that are not recognized state lanes.
	if !isStateWorktree {
		if name, isWorktree := inRepoWorktreeProject(clean); isWorktree {
			return name, true
		}
	}

	// (c) Evener state-dir worktrees. The predicate is the layout itself, not a
	// lookalike: a managed worktree is written at <stateDir>/worktrees/<lane> and
	// a delegate's at <stateDir>/sessions/<sid>/worktrees/<lane>, where stateDir
	// is this bucket or a bucket proven to live under this state root.
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
	var found []projectEvidence

	// A managed lane records the project it belongs to in its own path, so the
	// bucket's own project ID is its basename — that is where
	// RuntimeDirForProjectWithStateHome writes it, <stateRoot>/projects/<id>.
	// Hashing the bucket's path instead answers with a different ID for the same
	// repository, making a lane in its own bucket read as foreign.
	//
	// A legacy bucket records no project, and that is UNKNOWN rather than
	// conflicting: its sessions still all describe one repository, so origin-only
	// sibling evidence answers there. Calling the unknown ID a conflict marked
	// every managed lane in a legacy bucket foreign and threw that fallback away.
	bucketProjectID, bucketProjectKnown := r.bucketProjectID(bound)
	// foreignProject is a managed lane that belongs to another repository than the
	// one the bucket records, which only --state-dir pointed at the wrong bucket
	// produces. When the bucket records no project there is nothing to conflict
	// with, so the lane is not foreign — it is merely unconfirmed.
	foreignProject := managedProjectID != "" && bucketProjectKnown && managedProjectID != bucketProjectID

	// Admissibility is a question about identity, asked before rank: a candidate
	// that is about a different repository is dropped, and dropping it must leave
	// the others standing. It filters candidates rather than returning from the
	// scan for exactly that reason.
	admissible := func(projectID string) bool {
		switch {
		case managedProjectID == "" || projectID == "":
			return !foreignProject
		case projectID != managedProjectID:
			return false
		default:
			return true
		}
	}
	// namesTheLane is stricter, and only row-specific evidence has to meet it.
	// A parent is a claim about THIS lane, so for a lane whose path records a
	// project the parent must positively identify that same project: an empty
	// project ID is "this source knows nothing", not a match, and admitting it
	// lets a parent from another repository's bucket name the lane. Bucket-wide
	// sibling evidence is a different claim — it describes the bucket, not the
	// lane — so it keeps the origin-only fallback admissible() allows.
	namesTheLane := func(projectID string) bool {
		return managedProjectID == "" || projectID == managedProjectID
	}

	// The strongest evidence there can be, and the one that outlives every
	// checkout: the project ID in a managed lane's own path. It carries no
	// display name of its own, so it is a filter here and a name only when
	// something else agrees with it.
	if managedProjectID != "" {
		found = append(found, projectEvidence{projectID: managedProjectID, rank: evidenceRankRecorded})
	}

	// The worktree's own owner: the session this worktree was made for. Its
	// metadata names the project for this worktree specifically. The answer is
	// deliberately not cached — one state dir holds worktrees from more than one
	// repository, so caching an owner's project would mislabel the next worktree
	// resolved through it.
	//
	// A refused or unreadable owner is no evidence: the owner may be gone with the
	// lane, and that must not stop the parent or a sibling from answering.
	if sameBucket(stateDir, bound) && ownerSessionID != "" {
		if meta, ok := r.loadOwnedSessionMeta(bound, ownerSessionID); ok {
			ownerOrigin := strings.TrimSpace(meta.EnvInfo.GitOriginURL)
			// The saved origin is the strongest thing this owner can say, and it is
			// the only candidate here that needs nothing on disk. It is returned
			// before anything below is touched: reading the owner's record resolves
			// its recorded paths, which is a filesystem call on a directory that may
			// be gone, or on a network mount that has stalled, and returning here also
			// skips the parent metadata read and the bucket-wide sibling scan. A
			// read-only search must not wait on a mount to answer a question its own
			// metadata already answered.
			//
			// Nothing admissible can outrank it. Every other owner-derived candidate
			// ranks below the origin (the restore root and the home directory), and a
			// parent or sibling ranks below the owner outright. The one source stronger
			// than a display name — the project ID in a managed lane's own path — is a
			// filter rather than a name, and never coexists with an owner, because a
			// managed lane records no owner session. So this is rank 0 by another
			// name: it wins, and it carries no entry in the rank table.
			//
			// An inadmissible origin does not license the shortcut, so the record is
			// still read for it: it may hold the only evidence this row accepts. An
			// origin whose last segment is empty names no repository either, and
			// returning it would answer the row with a name that is not one while
			// the record still holds its restore root.
			if ownerOrigin != "" && admissible("") && repoBasename(ownerOrigin) != "" {
				return repoBasename(ownerOrigin), true
			}
			// The owner's record is read as a record, through the same lookup as
			// every other record. Its restore root leads because it is the preferred
			// display source, but a field that establishes nothing does not veto the
			// fields after it: an owner launched with a relative --dir records a
			// restore root that names no checkout while its working directory still
			// names one, and a session launched through a symlink records an alias
			// that dangles once the lane is removed while its saved lane path still
			// records the project.
			//
			// Every path is read through the same lane boundary. When the state home
			// sits inside a checkout, an unbounded walk from a deleted lane finds that
			// checkout's .git and names the transcript after the repository that
			// happens to enclose the state dir, before the parent lookup can recover
			// the project the lane belonged to. A recognized lane therefore answers
			// nothing here — its leaf is a lane name, never a repository — and the
			// parent or a sibling is what still answers.
			checkout, recorded, _, isLane := r.projectRecord(meta, bound)
			switch {
			case checkout != "":
				found = append(found, projectEvidence{name: filepath.Base(checkout), projectID: recorded, rank: evidenceRankOwnerRestore})
			case isLane:
				// A recognized lane contributes nothing, so the parent or a sibling
				// can still answer.
			default:
				// An ordinary recorded directory that no longer resolves: its basename
				// is the recorded-path fallback, and it is not a claim about projects.
				// It has to be absolute for the reason every other recorded path does:
				// a relative one names no checkout, and "." would label the row.
				if display := strings.TrimSpace(meta.HomeDir()); display != "" && filepath.IsAbs(display) {
					found = append(found, projectEvidence{name: filepath.Base(display), rank: evidenceRankOwnerHome})
				}
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
	// the lane's own recorded owner, or the lane's path records the project. A
	// parent whose own checkout is gone still offers its saved origin: the origin
	// names a repository without needing anything on disk.
	if parentSessionID != "" {
		tiedToLane := sameBucket(stateDir, bound) || managedProjectID != "" || parentSessionID == ownerSessionID
		// The whole lookup is gated, not only the shortcut below. Reading the
		// parent's record resolves every path it carries, and when the owner has
		// already answered, that resolution cannot change the result: the owner is
		// the row's own record and outranks a parent at every rank a parent can
		// reach. A parent recorded on an unavailable mount would otherwise hold a
		// search whose answer was settled before the read.
		if tiedToLane && !evidenceWins(found, admissible, evidenceRankParentOrigin) {
			if parent, ok := r.loadOwnedSessionMeta(bound, parentSessionID); ok {
				// LOCAL BEFORE REMOTE, and the parent is row-specific evidence, so it
				// outranks the bucket-wide scan below. A verified parent's saved origin
				// is a string in its metadata and needs nothing on disk. When the lane
				// records no project of its own there is no ID to check that origin
				// against, so it settles the label outright and the parent's recorded
				// paths — a walk that can block on an unavailable mount — are never
				// touched.
				//
				// A managed lane does need the walk: its path records a project, and a
				// parent names the lane only by positively identifying that same
				// project. The origin cannot supply an ID, so that case falls through.
				if managedProjectID == "" && !foreignProject {
					if origin := strings.TrimSpace(parent.EnvInfo.GitOriginURL); origin != "" && repoBasename(origin) != "" {
						return repoBasename(origin), true
					}
				}
				checkout, recorded, resolved, _ := r.recordedProject(parent, bound)
				// A parent whose checkout has moved names nothing on disk, but its
				// recorded absolute path still hashes to the project ID a lane under it
				// would carry. Offer that as the parent's project so the admissibility
				// test judges it like any other evidence: a recording that matches the
				// lane identifies it, and one that names another project is refused.
				if recorded == "" {
					if _, projectID := r.recordedOrdinaryProject(parent, bound); projectID != "" {
						recorded = projectID
					}
				}
				// A managed lane records its project in its path, so a parent must
				// positively identify that same project before it can name the
				// lane. A session-owned lane records no project, so there the
				// owner relation ties the parent and its evidence answers as it is.
				if namesTheLane(recorded) && admissible(recorded) {
					if origin := strings.TrimSpace(parent.EnvInfo.GitOriginURL); origin != "" {
						found = append(found, projectEvidence{name: repoBasename(origin), projectID: recorded, rank: evidenceRankParentOrigin})
					}
					if resolved && checkout != "" {
						found = append(found, projectEvidence{name: filepath.Base(checkout), projectID: recorded, rank: evidenceRankParentCheckout})
					}
					// The parent's checkout may have moved with the record still naming
					// where it was. The recording is evidence even then: an absolute path
					// hashes to the project ID a lane under it would carry, so offering
					// that ID lets the row's own admissibility decide whether this parent
					// names the lane. Without it a pruned lane in a flat --state-dir
					// loses the label its parent's recording still supplies, because the
					// recorded-path fallback lived only in the sibling scan.
					if !resolved || checkout == "" {
						if gone, projectID := r.recordedOrdinaryProject(parent, bound); projectID != "" {
							found = append(found, projectEvidence{name: filepath.Base(gone), projectID: projectID, rank: evidenceRankParentCheckout})
						}
					}
				}
			}
		}
	}

	// Sibling inference answers only for a worktree recorded inside this same
	// bucket: a row discovered in bucket A can record a worktree that lives in
	// bucket B, and A's sessions describe A's repository. Where the bucket may be
	// used at all is classifyBucket's answer, not a second test here.
	//
	// The scan is skipped outright once admissible row-specific evidence already
	// holds the strongest rank available to it. The scan is bucket-wide, so its
	// cost is one checkout resolution per sibling; paying that for a row an owner
	// or a verified parent has already answered is what turned a 1,000-sibling
	// bucket into 1,000 walks per result.
	siblingsAllowed := sameBucket(stateDir, bound) && r.classifyBucket(bound) != bucketUnrelated
	if siblingsAllowed && evidenceWins(found, admissible, evidenceRankSiblingOrigin) {
		siblingsAllowed = false
	}
	if siblingsAllowed {
		// A managed lane verifies a candidate's project ID, so its sibling snapshot
		// has to carry IDs. Every other row is asking for a name, and reading
		// origins alone answers it.
		//
		// A candidate that states NO project is unverified: nothing in it names a
		// repository, so the only thing that can tie it to this row's project is
		// the bucket's own verified scope. A project bucket has one — its name is
		// the project ID — and a flat or shared bucket does not. Admitting an
		// unverified label in a bucket without verified scope is how a pruned lane
		// came to take a different repository's saved origin.
		//
		// The scope is what licenses the candidate, so the SAME test applies to a
		// row that records no project of its own: having none only means it cannot
		// refuse the candidate, not that it may inherit anything the bucket ever
		// saw. A shared override pools sessions from several repositories, and its
		// last writer's origin is no claim about an ownerless lane there. A legacy
		// bucket keeps the fallback for exactly those rows, because its sessions
		// describe one repository even though its name does not record which; a
		// managed lane there still compares project IDs, and an unverified
		// candidate gives it nothing to compare.
		kind := r.classifyBucket(bound)
		verifiedScope := kind == bucketProject
		legacyNameOnly := kind == bucketLegacy && managedProjectID == ""
		for _, cand := range r.siblingEvidenceFor(bound, managedProjectID != "") {
			// The row's own eligibility, applied to evidence the bucket produced
			// once. This is why the evidence is not a cached label: the same
			// sibling is admissible for one row and not for another.
			if cand.projectID == "" && !verifiedScope && !legacyNameOnly {
				continue
			}
			if admissible(cand.projectID) {
				found = append(found, cand)
			}
		}
	}

	// Rank the candidates and take the strongest. A candidate whose project
	// contradicts the lane's recorded project never reaches here: it is the same
	// repository test applied to every source, so a refused interpretation of one
	// source leaves the others untouched.
	//
	// A verified identifier outranks an inference, and that outranking crosses
	// ranks rather than only breaking ties. A candidate whose project ID is empty is
	// unknown-project evidence: admissible, and the fallback that keeps a pruned
	// lane's label alive, but not a claim about THIS lane. One that positively names
	// the lane's recorded project IS that claim. Comparing them by rank alone lets
	// the empty one win — an origin recorded in a bucket that later served another
	// repository outranks a sibling that resolves to the lane's own checkout — and
	// the answer then depends on which sibling happened to be read first.
	//
	// So a positive match is tried first and rank decides only among the candidates
	// of the same standing. The unverified candidates remain the answer whenever no
	// positive match exists, which is what keeps origin-only inference working.
	best := projectEvidence{rank: evidenceRankBasename + 1}
	bestMatches := false
	for _, cand := range found {
		if cand.name == "" || !admissible(cand.projectID) {
			continue
		}
		matches := managedProjectID != "" && cand.projectID == managedProjectID
		switch {
		case matches && !bestMatches:
			best = cand
			bestMatches = true
		case matches == bestMatches && cand.rank < best.rank:
			best = cand
		}
	}
	if best.name == "" {
		return "", true
	}
	return best.name, true
}

// bucketMetaSnapshot returns the session metadata for a bucket: discovery's own
// snapshot when the search supplied it, and a fresh read otherwise. A search
// labels many rows from one bucket and the sibling scan needs every row's
// metadata, so rereading it per labelled row decodes the same directory once per
// result — the cost the review measured at one full decode per row.
//
// The read is never cached here. A bucket discovery did not enumerate is read
// when a row actually needs it, and a failure is not remembered, so a transient
// error cannot become a label.
func (r *projectNameResolver) bucketMetaSnapshot(bucketDir string) ([]schema.SessionMeta, error) {
	if metas, ok := r.bucketMetas[bucketDir]; ok {
		return metas, nil
	}
	return schema.ListSessionMetas(bucketDir)
}

// classifyBucket is the one place that decides what kind of state directory a
// row was discovered in, and whether bucket-wide sibling evidence may be used
// there at all. Every other site asks this rather than re-deriving eligibility
// from a path or a basename, which is how the same path came to be classified
// one way for the owner lookup and another way for the sibling scan.
//
// The test is the state root's own identification (stateHomeFor plus
// isConfiguredStateHome), never a spelling: a directory that merely looks like
// <root>/projects/<id> is not part of the state tree unless the root says so. A
// flat --state-dir therefore lands in bucketUnrelated, and so does a shared
// override that spells the layout.
//
// Inside the tree, the basename decides between the three recognized kinds. The
// shared override's name is one of them, because it is the one name that says
// its directory was pointed at several repositories on purpose; every other
// non-project name is a legacy bucket, which by convention held one.
func (r *projectNameResolver) classifyBucket(bucketDir string) bucketKind {
	home := stateHomeFor(bucketDir)
	if home == "" || !r.isConfiguredStateHome(home) {
		return bucketUnrelated
	}
	if filepath.Base(bucketDir) == sharedBucketName {
		return bucketShared
	}
	if identifier.ValidateProjectID(filepath.Base(bucketDir)) != nil {
		return bucketLegacy
	}
	return bucketProject
}

// bucketProjectID is the project a bucket records about itself, and whether it
// records one at all. A legacy bucket has none: its name is not a project ID, so
// its project is UNKNOWN, which is a different answer from "its project is
// something else". Treating unknown as a conflicting ID made every managed lane
// stored in a legacy bucket read as foreign and lose the origin-only sibling
// fallback that the bucket genuinely supports.
func (r *projectNameResolver) bucketProjectID(bucketDir string) (id string, known bool) {
	if r.classifyBucket(bucketDir) != bucketProject {
		return "", false
	}
	return filepath.Base(bucketDir), true
}

// evidenceWins reports whether an admissible candidate already holds an answer
// stronger than everything a more expensive source could contribute. When one
// does, reading that source cannot change the result, and its cost — one
// filesystem resolution per candidate it would examine — buys nothing.
//
// floor is the best rank the expensive source could reach, so the question is
// "does something admissible already beat it". It is phrased as a rank
// comparison rather than a list of sources, because the sources are many and the
// ordering between them is one rule: the sibling scan passes the rank its own
// evidence starts at, and the parent branch passes the rank a parent's evidence
// starts at.
func evidenceWins(found []projectEvidence, admissible func(string) bool, floor int) bool {
	for _, cand := range found {
		if cand.name == "" || !admissible(cand.projectID) {
			continue
		}
		if cand.rank < floor {
			return true
		}
	}
	return false
}

// siblingEvidenceFor returns the candidate answers a bucket's sessions produce,
// resolving each sibling's recorded checkout exactly once per search. Eligibility
// is deliberately NOT baked in: the same sibling is admissible for a
// session-owned row and inadmissible for a managed lane of another project, so
// each row applies its own rules to this evidence instead of reading a label
// that was decided for a different row.
//
// needProjectID says the asking row compares project IDs, so each sibling's
// recorded path has to be resolved to supply one. A row that only needs a name —
// a session-owned lane with no project of its own to check against — passes
// false, and then an origin-carrying sibling contributes its label without any
// checkout being resolved at all. That is what keeps a historical checkout on a
// stalled mount from stalling a search whose answer the metadata already held.
//
// A bucket whose sessions/ dir is a symlink is rejected here, once, with
// discovery's own guard rather than a second one written here.
func (r *projectNameResolver) siblingEvidenceFor(bucketDir string, needProjectID bool) []projectEvidence {
	// Reuse is sound only where the underlying metadata is already frozen for this
	// search: discovery read the bucket and handed its snapshot in. Without a
	// snapshot the resolver is reading a live directory, and a cached answer would
	// outlive the write that should have changed it — so that case reads fresh
	// every time and keeps no memory of what it saw.
	_, snapshot := r.bucketMetas[bucketDir]
	// The key carries needProjectID because it decides what this snapshot
	// CONTAINS, not merely who may read it: the ID-bearing form resolves paths the
	// name-only form never touches. Serving one under the other would either drop
	// a project ID a managed lane needs to verify, or walk checkouts for a row that
	// asked for nothing but a name.
	key := siblingEvidenceKey{bucketDir: bucketDir, needProjectID: needProjectID}
	if snapshot {
		if cached, ok := r.siblingEvidence[key]; ok {
			return cached
		}
	}
	if symlinkErrorDeep(schema.SessionsDir(bucketDir), bucketDir) != nil {
		if snapshot {
			r.siblingEvidence[key] = nil
		}
		return nil
	}
	metas, err := r.bucketMetaSnapshot(bucketDir)
	if err != nil {
		// A failed read is not remembered: a transient error must not become a
		// permanent "this bucket has no siblings".
		return nil
	}
	var evidence []projectEvidence
	// A name-only lookup is answered by the first usable origin in the bucket's
	// metadata, and asking for that first is what keeps an originless sibling's
	// checkout from being resolved before it. Resolving a recorded path is the
	// only filesystem work in this scan, and on an unavailable network mount that
	// work blocks the whole read-only search even though the metadata already
	// held the answer. The origin is a saved string, so this pass touches
	// nothing.
	//
	// Only the name-only form takes this shortcut. A caller that compares project
	// IDs needs each sibling resolved to supply one, so there it is the ID that
	// answers and the walk is the point.
	if !needProjectID {
		for _, sm := range metas {
			origin := strings.TrimSpace(sm.EnvInfo.GitOriginURL)
			if origin == "" {
				continue
			}
			name := repoBasename(origin)
			// An origin whose last segment is empty once ".git" is stripped — a
			// local mirror spelled as a path ending in "/.git" — names no
			// repository, so it is no evidence. Answering it would stop the pass
			// with an empty name before a sibling that can answer is read.
			if name == "" {
				continue
			}
			evidence = []projectEvidence{{name: name, rank: evidenceRankSiblingOrigin}}
			if snapshot {
				r.siblingEvidence[key] = evidence
			}
			return evidence
		}
	}
	for _, sm := range metas {
		// A saved origin is authoritative and needs nothing on disk, so it is read
		// BEFORE the checkout is resolved. The other order is what made a single
		// historical checkout on a stalled mount stall the whole search even though
		// the bucket's own metadata already answered: recordedProject resolves every
		// recorded path, and an origin-carrying sibling does not need that resolution
		// to contribute a name.
		//
		// The project ID is the one thing an origin cannot supply on its own — an
		// origin is a URL, not a path — so it is resolved only for the rows that
		// compare IDs. That is a property of the ASKING ROW, not of the bucket, which
		// is why it is part of this snapshot's cache key.
		//
		// An origin whose last segment is empty once ".git" is stripped names no
		// repository, so it is no evidence. It must not be treated as one either
		// way: not appended as an empty answer, and not allowed to skip the rest of
		// the record, because the sibling's recorded restore root and checkout can
		// still name its project.
		if origin := strings.TrimSpace(sm.EnvInfo.GitOriginURL); origin != "" && repoBasename(origin) != "" {
			recorded := ""
			if needProjectID {
				_, recorded, _, _ = r.recordedProject(sm, bucketDir)
			}
			evidence = append(evidence, projectEvidence{name: repoBasename(origin), projectID: recorded, rank: evidenceRankSiblingOrigin})
			continue
		}
		checkout, recorded, _, isLane := r.recordedProject(sm, bucketDir)
		// An explicit restore root is the next best record, and it answers even when
		// the directory it names has moved: the recording is the evidence, not the
		// checkout. Both are ranked above a checkout that merely resolves.
		if restore := strings.TrimSpace(sm.WorktreeRestoreRoot); restore != "" {
			// A restore root that still resolves answers with the checkout it
			// resolves to — a restore root in a repository subdirectory names the
			// repository, not the subdirectory. When it does not resolve the
			// recording is the evidence: an ordinary recorded directory keeps its
			// basename even after it moves, while a recognized state lane's leaf is
			// a lane name and answers nothing.
			switch {
			case checkout != "":
				evidence = append(evidence, projectEvidence{name: filepath.Base(checkout), projectID: recorded, rank: evidenceRankSiblingRestore})
			case isLane:
				// The lane's own leaf is a lane name, so it never labels anything. The
				// restore root is a SEPARATE field, though, and it can still name the
				// repository: a sibling cut from a checkout and then moved into a lane
				// carries both the pruned lane path — which is where its project ID comes
				// from — and the absolute root it was cut from. When that root's canonical
				// path hashes to the very ID the lane path records, the two fields agree,
				// and their agreement is the claim this candidate makes. Discarding it
				// only because the lane path was read first drops a label another
				// originless lane in the bucket depends on.
				//
				// It stays a candidate with an ID rather than a bare name, so a row of a
				// different project still refuses it: the ID is matched at the same
				// admissibility test as every other ID-bearing candidate.
				if recorded != "" && filepath.IsAbs(restore) {
					canonicalRoot := resolveExistingPrefix(restore)
					if identifier.ProjectFromCanonicalPath(canonicalRoot).ID == recorded {
						evidence = append(evidence, projectEvidence{name: filepath.Base(canonicalRoot), projectID: recorded, rank: evidenceRankSiblingRestore})
					}
				}
			default:
				// A relative restore root names no directory: it would be read against
				// this process's directory, and "." is a label every such record
				// shares. The structural lookup already refuses it; the basename
				// fallback must not reintroduce it.
				if filepath.IsAbs(restore) {
					evidence = append(evidence, projectEvidence{name: filepath.Base(restore), projectID: recorded, rank: evidenceRankSiblingRestore})
				}
			}
			continue
		}
		// Otherwise the sibling's own checkout names the repository. A state lane
		// whose checkout is gone names no directory to read a label from, so it
		// supplies nothing rather than its lane name.
		if checkout != "" {
			evidence = append(evidence, projectEvidence{name: filepath.Base(checkout), projectID: recorded, rank: evidenceRankSiblingCheckout})
		} else if !isLane {
			// The checkout is gone and the record names an ordinary directory, so
			// nothing on disk can say what it was. The recording still can: hashing
			// its absolute path yields the project ID a managed lane would have been
			// created under. Offering that ID rather than a bare name is what keeps
			// this the same predicate as every other candidate — the ROW decides
			// whether the ID is its project, so a sibling from another repository is
			// still refused, and a lane name never reaches here at all.
			if gone, projectID := r.recordedOrdinaryProject(sm, bucketDir); projectID != "" {
				evidence = append(evidence, projectEvidence{name: filepath.Base(gone), projectID: projectID, rank: evidenceRankSiblingCheckout})
			}
		}
	}
	if snapshot {
		r.siblingEvidence[key] = evidence
	}
	return evidence
}

// recordedProject answers what project a session's own recorded path names. It
// is the one predicate for "evidence is the path, not the checkout", because a
// recognized managed path is evidence even when what it points at is gone. Both
// the row's own bounds and a sibling's are decided by it, so the two can never
// disagree about what a recorded path proves.
//
// A lane whose checkout is gone must not be walked past: the walk answers with
// whichever repository encloses the state directory, which would both invent an
// affiliation the lane never had and reject its surviving record — the saved
// origin — as conflicting. Confined to the lane root, a deleted lane still
// names the project its path records.
func (r *projectNameResolver) recordedProject(m schema.SessionMeta, bucketDir string) (checkout, projectID string, ok, isLane bool) {
	return r.projectRecord(m, bucketDir)
}

// recordPaths lists the paths a session record carries, strongest first. A record
// is not one path: a session that entered a worktree keeps the root it entered
// from, the lane it entered, and the directory it runs in, and any of them can be
// the one that still says which project it belongs to. The restore root leads
// because it is the preferred display source, and the working directory trails
// because it is the loosest claim — but a field that establishes nothing is
// skipped rather than allowed to mask the ones after it.
func recordPaths(m schema.SessionMeta) []string {
	var paths []string
	for _, candidate := range []string{m.WorktreeRestoreRoot, m.WorktreePath, m.EnvInfo.WorkingDir} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if !slices.Contains(paths, candidate) {
			paths = append(paths, candidate)
		}
	}
	return paths
}

// recordedOrdinaryProject names the project an ORDINARY recorded path belonged
// to after the path itself is gone. Nothing on disk can answer then, so the
// recording does: an absolute path hashes to the same project ID a managed lane
// for it would have been created under, so offering that ID is a claim the row
// can check rather than a bare name it must trust.
//
// The hash is taken over the path with its SURVIVING PREFIX RESOLVED, because
// that is the form the ID was computed from when the lane was created. A
// checkout recorded through an existing symlinked ancestor — every macOS
// /var/... path, and any explicit link elsewhere — hashes to two different IDs
// depending on whether the link is followed: the canonical one while the
// checkout exists, and the lexical one once it is gone if the resolution falls
// back to the whole path. Resolving the prefix keeps the recovered ID equal to
// the ID the lane's own path stores, so the two can still be compared.
//
// It answers only for a path that is gone, and only for one that is not a state
// lane. A lane's leaf is a lane name, not a repository, and hashing a pruned
// lane's path would hand the row the lane name as a project label — a claim no
// evidence supports, and one a session-owned child has no project ID to refuse.
// The classification therefore comes from the structural lookup, like every
// other lane test in this file, rather than from the path's spelling.
//
// An existing directory's project comes from its Git ancestry, which is
// projectAt's answer and stays there: reading a surviving checkout here would
// give a second, weaker way to name the same thing, and the two could disagree.
func (r *projectNameResolver) recordedOrdinaryProject(m schema.SessionMeta, bucketDir string) (gone, projectID string) {
	for _, recorded := range recordPaths(m) {
		if !filepath.IsAbs(recorded) {
			continue
		}
		// The surviving prefix is resolved BEFORE the identity is hashed, so a
		// recorded path keeps the canonical spelling it had while it existed.
		clean := resolveExistingPrefix(recorded)
		if directoryExists(clean) {
			return "", ""
		}
		if _, _, _, _, isLane := r.stateWorktreeOwner(clean, bucketDir); isLane {
			continue
		}
		return clean, identifier.ProjectFromCanonicalPath(clean).ID
	}
	return "", ""
}

// projectRecord answers what a session's record names, trying each recorded path
// in strength order. The first path that names a project answers. A path that
// establishes nothing — relative to a directory nobody recorded, gone, refused,
// or a lane whose project its path does not carry — is no evidence about the
// record's project, and it does not veto the fields after it.
//
// This is the one place that decides it, for the row itself, for its parent and
// for a sibling: a record consulted through a single preferred field answers
// differently depending on which field a display preference picked.
func (r *projectNameResolver) projectRecord(m schema.SessionMeta, bucketDir string) (checkout, projectID string, ok, isLane bool) {
	recordIsLane := false
	for _, recorded := range recordPaths(m) {
		checkout, projectID, ok, lane, _ := r.projectAt(recorded, bucketDir)
		r.pathResolutions++
		if ok {
			return checkout, projectID, true, lane
		}
		if lane {
			// Still remembered when nothing answers: the record names a state lane
			// in at least one field, so its leaf is a lane name and never a
			// repository, whatever the other fields say.
			recordIsLane = true
		}
	}
	return "", "", false, recordIsLane
}

// projectAt is recordedProject's rule, applied to a path. checkout is the
// project's root, empty when the path names no directory that still exists.
// projectID is the project the recording itself declares, and is never empty
// when ok is true: a managed lane keeps <stateDir>/worktrees/<projectID>/<lane>
// after its checkout is deleted, so the ID a lane belonged to survives the
// deletion even though nothing is left on disk to resolve. ok distinguishes
// "this path identifies a project" from "it identifies none". An ordinary
// directory identifies its own project; a state lane whose checkout is gone
// identifies the project its path records, which is why deleting a worktree does
// not erase which one it served.
//
// isLane reports that the recording names a state-managed lane rather than an
// ordinary directory, decided once by the structural lookup that reads it. Every
// caller that has to tell those two apart takes the answer from here: an alias to
// a lane is still a lane, and re-deriving the classification from the recorded
// path would call the alias an ordinary directory and offer its leaf as a
// project.
//
// hasGitRoot reports that a Git layout, not the bare directory, supplied
// checkout. It is the difference between "this directory belongs to a repository
// that named it" and "this directory is all that is left" — two answers that
// share a checkout value but not a display name, because only the first has a
// repository name to show. A caller deciding a LABEL asks; a caller deciding an
// ID does not, since the canonical directory is the identity either way.
func (r *projectNameResolver) projectAt(recorded, bucketDir string) (checkout, projectID string, ok, isLane, hasGitRoot bool) {
	recorded = strings.TrimSpace(recorded)
	if recorded == "" {
		return "", "", false, false, false
	}
	// A relative record names no checkout. Project identity is a hash of an
	// absolute canonical path, so hashing a relative one invents an identity that
	// belongs to no repository — and the answer would depend on this process's
	// working directory, which is not the directory the session ran in. Records are
	// written absolute now; a record from before that is no evidence, not a
	// conflicting one, so the parent or a sibling can still answer.
	if !filepath.IsAbs(recorded) {
		return "", "", false, false, false
	}
	// Resolve the alias before anything walks this path. A recorded working
	// directory can be a symlink into a checkout, and a Git walk over the
	// symlink's own lexical parents misses the repository's .git entirely; the
	// surviving directory is then canonicalized as if it were a project of its
	// own. Walking where the path really points keeps a sibling's saved origin from
	// being rejected as conflicting evidence.
	//
	// A path that cannot be resolved as a whole falls back to its lexical Clean,
	// which can still name a link; resolveFullyClean resolves that spelling too.
	clean := resolveFullyClean(recorded)
	if _, laneRoot, _, managedProjectID, isLane := r.stateWorktreeOwner(clean, bucketDir); isLane {
		if root := resolveMainRootWithin(clean, laneRoot); root != "" {
			return root, identifier.ProjectFromCanonicalPath(identifier.ResolveClean(root)).ID, true, true, true
		}
		if managedProjectID != "" {
			return "", managedProjectID, true, true, false
		}
		return "", "", false, true, false
	}
	// Outside a recognized state lane, a Git ancestor only describes this path if
	// the path is still there. A recorded checkout that has since been removed
	// leaves nothing behind, and the walk then finds whichever repository happens
	// to enclose the empty space — an affiliation the deleted directory never had.
	// Absence of a path proves nothing about its project, so it is no evidence:
	// not a root, and not a conflict that would veto a sibling's saved origin.
	//
	// A lane keeps its recorded project ID across deletion and is handled above,
	// which is why this check is confined to the ordinary-directory branch.
	exists, statErr := os.Stat(clean)
	if statErr != nil || !exists.IsDir() {
		return "", "", false, false, false
	}
	root, refused := r.mainRootOutcomeClean(clean)
	if root != "" {
		return root, identifier.ProjectFromCanonicalPath(identifier.ResolveClean(root)).ID, true, false, true
	}
	if refused {
		// The walk reached a directory whose .git entry exists and was refused — a
		// dangling symlink, a FIFO, an oversized or malformed pointer. That is not
		// "no repository here": it is a checkout whose identity could not be read.
		// Treating the surviving directory as a project of its own would invent an
		// identity for it, and then contradict a sibling's saved origin that names
		// the repository this checkout actually belongs to. So this path proves
		// nothing either way, and the origin stays eligible.
		return "", "", false, false, false
	}
	// No .git anywhere above it, but the directory is really there — the stat above
	// established that: it is its own project, and that project is what its
	// canonical path names.
	canonical := identifier.ResolveClean(clean)
	return canonical, identifier.ProjectFromCanonicalPath(canonical).ID, true, false, false
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
//
// The layouts are candidate interpretations, and each is validated on its own: a
// rejected reading must never consume the one that would have answered. A
// shared override such as --state-dir /srv/sessions/shared makes the path
// /srv/sessions/shared/worktrees/<projectID>/<lane> look session-owned, because
// "sessions" sits two components before "worktrees" and "shared" would be the
// owner. Validating that reading against /srv fails — and then the managed
// reading against the bucket the caller validated is still available.
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
		// A managed lane, <stateDir>/worktrees/<projectID>/<lane>. This is tried
		// first: it is the shape that records a project, so reading it as
		// session-owned would discard the strongest evidence a lane carries.
		if i+2 < len(parts) && identifier.ValidateProjectID(parts[i+1]) == nil {
			if candidate := strings.Join(parts[:i], sep); r.isStateBucket(candidate, cleanBucket) {
				return candidate, strings.Join(parts[:i+3], sep), "", parts[i+1], true
			}
		}
		// A delegate lane, <stateDir>/sessions/<sid>/worktrees/<lane>. Its own
		// candidate is the state dir above sessions/, and it is validated here
		// rather than assumed from the spelling.
		if i >= 2 && parts[i-2] == "sessions" {
			if candidate := strings.Join(parts[:i-2], sep); r.isStateBucket(candidate, cleanBucket) {
				return candidate, strings.Join(parts[:i+2], sep), parts[i-1], "", true
			}
		}
		// An ownerless lane, <stateDir>/worktrees/<lane>.
		if candidate := strings.Join(parts[:i], sep); r.isStateBucket(candidate, cleanBucket) {
			return candidate, strings.Join(parts[:i+2], sep), "", "", true
		}
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
	if sameBucket(candidate, bucket) {
		return true
	}
	home := stateHomeFor(candidate)
	if home == "" {
		return false
	}
	return r.isConfiguredStateHome(home)
}

// sameBucket reports whether two spellings name the same state directory.
//
// A historical record keeps the spelling the session used, and a symlinked
// $XDG_STATE_HOME is the ordinary way that differs from what discovery opens:
// startup writes buckets under the physical path while a session launched
// through the link records the link. Comparing the strings then says "different
// bucket" about one directory, which discards that bucket's owner and sibling
// evidence from a lane that lives in it.
//
// Only the prefixes that still exist are resolved. EvalSymlinks fails on a whole
// path once any component is gone, and a deleted lane is exactly the case this
// has to answer, so resolving the full path would answer "different" for the one
// input that matters most. Walking up to the nearest existing ancestor and
// resolving that gives the same directory when both spellings reach the same
// place on disk, and leaves two genuinely different buckets different.
func sameBucket(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	return identifier.ResolveClean(existingPrefix(a)) == identifier.ResolveClean(existingPrefix(b))
}

// existingPrefix returns path with every missing tail component kept, so the
// result names the same directory but reaches an ancestor that still exists.
// Resolving a whole path fails once any component is gone, and a deleted lane is
// the case sameBucket has to answer.
func existingPrefix(path string) string {
	clean := filepath.Clean(path)
	var missing []string
	for {
		if _, err := os.Lstat(clean); err == nil {
			return filepath.Join(append([]string{clean}, missing...)...)
		}
		parent := filepath.Dir(clean)
		if parent == clean {
			// Nothing along the path exists, not even the root. The cleaned form is
			// the whole answer and it matches nothing else.
			return filepath.Join(append([]string{clean}, missing...)...)
		}
		missing = append([]string{filepath.Base(clean)}, missing...)
		clean = parent
	}
}

// pathWithin reports whether path is root itself or sits beneath it, comparing
// whole path components. A string prefix comparison would call /a/rooted a
// child of /a/root; this does not.
func pathWithin(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	if root == "" || root == string(filepath.Separator) {
		return filepath.IsAbs(path)
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// resolveExistingPrefix resolves every component of path that exists and keeps
// the missing tail in its lexical spelling, so two paths that name the same
// place get the same answer even when their last components are gone.
//
// existingPrefix alone cannot do this: EvalSymlinks fails for the whole path as
// soon as one component is missing, so passing the joined result through
// ResolveClean leaves even the ancestor in its original spelling. Splitting the
// two gives the ancestor the resolution it deserves.
func resolveExistingPrefix(path string) string {
	clean := filepath.Clean(path)
	var missing []string
	for {
		if _, err := os.Lstat(clean); err == nil {
			resolved := identifier.ResolveClean(clean)
			if len(missing) == 0 {
				return resolved
			}
			return filepath.Join(append([]string{resolved}, missing...)...)
		}
		parent := filepath.Dir(clean)
		if parent == clean {
			if len(missing) == 0 {
				return clean
			}
			return filepath.Join(append([]string{clean}, missing...)...)
		}
		missing = append([]string{filepath.Base(clean)}, missing...)
		clean = parent
	}
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
	// Delegated, not duplicated: this wrapper exists for callers that only want
	// the display name, and it must not get its own cache or its own walk. The
	// resolver's memo is the one cache, so refusal survives in one place.
	root, _ := r.mainRootOutcome(dir)
	return root
}

// resolveFullyClean resolves path through its symlinks, including the spelling
// identifier.ResolveClean falls back to when the whole path cannot be resolved.
// That fallback is a plain Clean, and it can still name a symlink itself: a
// recorded path ending in a removed component followed by ".." cleans to the
// link, and a walk started there climbs the link's lexical parents instead of
// the checkout the link points into. A path that resolves on the first attempt
// is returned unchanged.
func resolveFullyClean(path string) string {
	for {
		clean, resolved := identifier.ResolveCleanOK(path)
		if resolved || clean == path {
			return clean
		}
		path = clean
	}
}

// mainRootOutcome answers dir's main checkout, resolving the spelling fully
// first so a caller may pass the recorded path as it was written.
func (r *projectNameResolver) mainRootOutcome(dir string) (root string, refused bool) {
	return r.mainRootOutcomeClean(resolveFullyClean(dir))
}

// mainRootOutcomeClean answers a fully resolved directory's main checkout through
// this search's per-directory reuse, walking from clean upward on a miss. clean
// must be resolveFullyClean output: a spelling that only resolved lexically can
// still name a link. The key is the resolved spelling, so two spellings of one
// directory share one entry.
//
// The walk resolves the main checkout from the .git layout alone, never by
// running Git. Transcript search is read-only and its inputs are
// workspace-controlled: a recorded restore root can sit inside a repository the
// user checked out, and the command runner prepends workspace directories to
// PATH, so any `git` invocation here would run a repository-supplied executable
// with the daemon's permissions. None of that is acceptable for a label, so
// there is no fall-through to a second resolver: this answers, or it reports a
// refusal, or it reports absence. Every ".git" shape has one row in mainRootAt.
//
//   - .git directory, or a symlink whose target is a directory: the checkout's
//     own Git directory, so the directory is its own main root.
//   - regular pointer file, or a symlink to one: a linked-worktree pointer
//     names the main checkout, a submodule pointer names this checkout. Both
//     are validated against the layout before either answer is used.
//   - symlink that does not resolve, FIFO, socket, device: refused.
//   - pointer larger than any pointer, or unreadable: refused.
//   - no .git in this directory or any ancestor: absent.
//
// An entry that was read and refused is terminal: it never collapses into "no
// repository here", because only absence licenses treating the directory as a
// project of its own. Refusal is stored beside the root, never folded into it.
func (r *projectNameResolver) mainRootOutcomeClean(clean string) (root string, refused bool) {
	if outcome, ok := r.mainRoots[clean]; ok {
		return outcome.root, outcome.refused
	}
	dir := clean
	for {
		if resolved, found := mainRootAt(dir); found {
			root, refused = resolved, resolved == ""
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	r.mainRootLookups++
	r.mainRoots[clean] = mainRootResult{root: root, refused: refused}
	return root, refused
}

// resolveMainRootWithin is mainRootOutcomeClean's walk confined to root: it asks
// root and the directories beneath it and never climbs past root. A
// state-managed lane stops here, because the lane's identity is recorded in the
// state directory, not in whichever repository happens to enclose it: once the
// lane's checkout is deleted it has no .git to stop an unbounded walk, and the
// walk would climb past the lane root and answer with the enclosing checkout.
//
// A live lane still resolves, because its own .git pointer names the main
// checkout and that pointer is read here.
func resolveMainRootWithin(dir, root string) string {
	// Resolved here as well as at the callers, because a boundary computed from an
	// unresolved path describes the link rather than the lane: the walk would
	// compare an alias against a lane root it does not lexically contain, climb
	// past the boundary, and leave the lane's identity to whatever encloses it.
	//
	// Each side is resolved through its EXISTING components only. Full path
	// resolution fails once any component is missing, and a recorded directory
	// that names a DELETED subdirectory of a surviving lane is exactly that: the
	// missing tail keeps its original spelling while the lane root resolves to its
	// physical one, the two stop comparing equal, and the walk climbs straight
	// past the boundary into the checkout enclosing the state dir.
	//
	// Resolving the nearest existing ancestor and re-appending the missing
	// components puts both sides in the same spelling. Doing it by passing the
	// joined path to EvalSymlinks would not: that call fails for the whole path,
	// so even the ancestor would come back unresolved.
	clean := resolveExistingPrefix(dir)
	root = resolveExistingPrefix(root)
	if !pathWithin(clean, root) {
		// Containment is the boundary's whole guarantee, so it is checked before
		// the first step rather than trusted from the comparison above. A recorded
		// path that does not sit at or under its lane root is not that lane's
		// directory, and walking it would answer with whatever encloses it.
		return ""
	}
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
