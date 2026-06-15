package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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
// are intentionally absent.
type sessionRecord struct {
	TranscriptRef string    `json:"transcript_ref"`
	Kind          string    `json:"kind"`
	Title         string    `json:"title"`
	UpdatedAt     time.Time `json:"updated_at"`
	ApproxTurns   int       `json:"approx_turns"`
	ParentRef     string    `json:"parent_ref,omitempty"`
	Project       string    `json:"project,omitempty"`
	IsCurrent     bool      `json:"is_current,omitempty"`
	Snippets      []snippet `json:"snippets,omitempty"`
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
		Tool: llm.Tool{Definition: tool.DefFindSessionTranscripts(), ReadOnly: true},
		Exec: func(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			v, err := execFindSessionTranscripts(deps, args)
			if err != nil {
				return nil, err
			}
			env, ok := v.(findSessionsEnvelope)
			if !ok {
				return v, nil
			}
			return formatSessionFindings(env), nil
		},
	}
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
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, m.TranscriptRef, m.Title)
		meta := fmt.Sprintf("   %s · ~%d turns · updated %s", m.Kind, m.ApproxTurns, m.UpdatedAt.Format("2006-01-02 15:04"))
		if m.Project != "" {
			meta += " · project " + m.Project
		}
		if m.IsCurrent {
			meta += " · current"
		}
		b.WriteString(meta + "\n")
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
func execFindSessionTranscripts(deps *toolDeps, args map[string]any) (any, error) {
	query := strings.TrimSpace(stringArg(args, "query"))
	childrenOf := strings.TrimSpace(stringArg(args, "children_of"))
	limit := clampFindLimit(optionalIntArg(args, "limit"))
	if childrenOf != "" {
		return execFindChildren(deps, childrenOf, limit) // precedes query per spec
	}
	scope := strings.TrimSpace(stringArg(args, "scope"))
	if scope == "" {
		scope = scopeCurrentProject
	}
	return execFindAcrossSessions(deps, query, scope, limit)
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
// currentID is the live session's ID; a match sets IsCurrent. currentMeta (when
// non-nil) supplies the live session's in-memory meta: the current session's
// on-disk meta is stale mid-run (its turn count and updated-at are only flushed
// at turn boundaries), so those freshness fields are overlaid from memory.
func buildSessionRecord(c findCandidate, snips []snippet, currentID string, currentMeta func() schema.SessionMeta) sessionRecord {
	turnCount := c.meta.TurnCount
	updatedAt := c.meta.UpdatedAt
	if currentMeta != nil && c.meta.ID == currentID {
		live := currentMeta()
		turnCount = live.TurnCount
		updatedAt = live.UpdatedAt
	}
	parentRef := ""
	if c.meta.ParentSessionID != "" {
		parentRef = encodeRef(c.bucketHash, c.meta.ParentSessionID)
	}
	return sessionRecord{
		TranscriptRef: encodeRef(c.bucketHash, c.meta.ID),
		Kind:          sessionKind(c.meta),
		Title:         firstLineClamp(schema.SessionDisplayName(c.meta), 120),
		UpdatedAt:     updatedAt,
		ApproxTurns:   turnCount,
		ParentRef:     parentRef,
		Project:       projectName(c.meta),
		IsCurrent:     c.meta.ID == currentID,
		Snippets:      snips,
	}
}

// sortCandidatesNewestFirst sorts candidates by UpdatedAt descending, ID ascending as
// a stable tie-break, with the current session always last.
func sortCandidatesNewestFirst(candidates []findCandidate, currentID string) {
	sort.Slice(candidates, func(i, j int) bool {
		isCurI := candidates[i].meta.ID == currentID
		isCurJ := candidates[j].meta.ID == currentID
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
	var out []sessionRecord
	for _, c := range candidates {
		if len(out) >= limit {
			break
		}
		if !transcriptExists(c.bucketDir, c.meta.ID) {
			continue
		}
		out = append(out, buildSessionRecord(c, snipsFor(c), currentID, currentMeta))
	}
	return out
}

// execFindAcrossSessions implements catalog (no query) and content-search (query
// present) over all buckets in scope. With no query: metadata-only, newest-first,
// current last, no scan metrics. With a query: metadata match first (cheap, no
// file open); on miss, bounded raw content scan tracking scanned/scanTruncated.
func execFindAcrossSessions(deps *toolDeps, query, scope string, limit int) (any, error) {
	buckets, scopeApplied := findBuckets(deps.stateDir, scope)
	currentID := deps.sessionID

	candidates := collectCandidates(buckets, deps.stateDir)
	sortCandidatesNewestFirst(candidates, currentID)

	var records []sessionRecord
	var scanned *int
	var scanTruncated *bool

	if query == "" {
		// Catalog: metadata-only, no scan.
		records = recordsUpTo(candidates, func(_ findCandidate) []snippet { return nil }, currentID, limit, deps.currentMeta)
	} else {
		// Content-search: track coverage.
		n := 0
		trunc := false
		needle := strings.ToLower(query)
		for _, c := range candidates {
			if len(records) >= limit {
				break
			}
			if !transcriptExists(c.bucketDir, c.meta.ID) {
				continue
			}
			if snips, matched := matchCandidate(c, query, needle, &n, &trunc); matched {
				records = append(records, buildSessionRecord(c, snips, currentID, deps.currentMeta))
			}
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
// ParentSessionID matches the parent.
func execFindChildren(deps *toolDeps, ref string, limit int) (any, error) {
	bucketDir, parentID, scopeApplied, err := parentBucketAndID(ref, deps.stateDir, deps.sessionID)
	if err != nil {
		return nil, err
	}
	currentID := deps.sessionID

	candidates := collectCandidates([]string{bucketDir}, deps.stateDir)

	// Keep only direct children of parentID.
	var children []findCandidate
	for _, c := range candidates {
		if c.meta.ParentSessionID == parentID {
			children = append(children, c)
		}
	}

	sortCandidatesNewestFirst(children, currentID)

	// Resolving the parent opens no transcript (parentBucketAndID is stat-free), but
	// every returned child must still be a read-able ref, so children are gated on
	// the same transcriptExists (os.Stat, not a body open) as the catalog: a spawned
	// child whose transcript was never flushed is not auditable and is excluded.
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
func findBuckets(currentStateDir, scope string) (buckets []string, scopeApplied string) {
	if scope != scopeAllProjects {
		return []string{currentStateDir}, scopeCurrentProject
	}
	sh := stateHomeFor(currentStateDir)
	if sh == "" {
		return []string{currentStateDir}, scopeCurrentProject
	}
	all, err := enumerateBuckets(sh)
	if err != nil || len(all) == 0 {
		return []string{currentStateDir}, scopeCurrentProject
	}
	return all, scopeAllProjects
}

// findCandidate pairs a session meta with the bucket it lives in, so its ref can
// be encoded relative to the current bucket and its transcript located.
type findCandidate struct {
	meta       schema.SessionMeta
	bucketDir  string
	bucketHash string // "" when this is the current bucket (→ local: ref)
}

// collectCandidates lists SessionMetas (cheap, meta-only) over each bucket and
// pairs each with its bucket. The bucketHash is "" for the bucket that IS the
// current state dir (so its refs are local:), and the bucket's basename hash for
// every sibling (proj: refs). The current bucket is identified by absolute-path
// equality, not list position, because enumerateBuckets returns buckets in
// arbitrary glob order.
func collectCandidates(buckets []string, currentStateDir string) []findCandidate {
	currentAbs, _ := filepath.Abs(currentStateDir)
	var out []findCandidate
	for _, bucket := range buckets {
		metas, err := schema.ListSessionMetas(bucket)
		if err != nil {
			continue
		}
		hash := ""
		if bucketAbs, _ := filepath.Abs(bucket); bucketAbs != currentAbs {
			hash = filepath.Base(bucket)
		}
		for _, m := range metas {
			out = append(out, findCandidate{meta: m, bucketDir: bucket, bucketHash: hash})
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
func matchCandidate(c findCandidate, query, needle string, scanned *int, scanTruncated *bool) ([]snippet, bool) {
	if metaMatches(c.meta, needle) {
		return nil, true
	}
	// Content scan is bounded: once maxContentScan transcripts have been opened,
	// stop scanning further candidates and flag the partial coverage.
	if *scanned >= maxContentScan {
		*scanTruncated = true
		return nil, false
	}
	snips, opened := contentSnippets(c.bucketDir, c.meta.ID, query, needle)
	if opened {
		*scanned++
	}
	if len(snips) == 0 {
		return nil, false
	}
	return snips, true
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
func contentSnippets(bucketDir, sessionID, query, needle string) (snips []snippet, opened bool) {
	path := transcriptPath(bucketDir, sessionID)
	_, entries, _, err := readTranscript(path)
	if err != nil {
		return nil, false
	}
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
	return collector.out, true
}

// transcriptExists is a cheap stat of the transcript JSONL file (no parse).
func transcriptExists(bucketDir, sessionID string) bool {
	_, err := os.Stat(transcriptPath(bucketDir, sessionID))
	return err == nil
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
// when present, else basename of EnvInfo.WorkingDir.
// A trailing ".git" is stripped so "…/serf.git" displays as "serf".
func projectName(m schema.SessionMeta) string {
	if origin := strings.TrimSpace(m.EnvInfo.GitOriginURL); origin != "" {
		return repoBasename(origin)
	}
	if wd := strings.TrimSpace(m.EnvInfo.WorkingDir); wd != "" {
		return filepath.Base(wd)
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
	// Convert the byte index into a rune index for safe slicing.
	matchRune := len([]rune(flat[:idx]))
	qLen := len([]rune(query))

	half := (width - qLen) / 2
	if half < 0 {
		half = 0
	}
	start := matchRune - half
	if start < 0 {
		start = 0
	}
	end := start + width
	if end > len(runes) {
		end = len(runes)
		start = end - width
		if start < 0 {
			start = 0
		}
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
