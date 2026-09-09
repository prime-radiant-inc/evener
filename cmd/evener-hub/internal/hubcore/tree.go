package hubcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"
)

// Tree is the navigation data model.
//
//   - NeedsYou is the cross-project triage tier the sidebar renders at the top:
//     every awaiting/warning/errored session, errors first then oldest-blocked
//     first, hidden when empty. Archived sessions are excluded even if live.
//   - Projects is the flat, recency-ordered list of active projects (those with
//     at least one non-archived session and not manually archived). Each
//     project's sessions are split into Current / Recent / Archived tiers.
//   - ArchivedProjects is the collapsed group at the bottom: projects that are
//     manually archived or whose every session is archived.
//   - Live is the flat live list consumed by the navigation projection and
//     rendered as the rail's "Live" section. Archived sessions are excluded:
//     an archived-but-still-running session is reachable under its project's
//     Archived tier instead, because archive is a clearing verb.
type Tree struct {
	NeedsYou         []TreeNode
	Live             []TreeNode
	Projects         []TreeProject
	ArchivedProjects []TreeProject
	favoriteLive     []TreeNode
}

// Snapshot returns a deep immutable copy of a tree, including the uncapped
// private tier slices retained for pagination. Consumers that retain a tree
// beyond their input snapshot must use this instead of copying Tree values.
func (t Tree) Snapshot() Tree {
	out, _ := t.SnapshotContext(context.Background())
	return out
}

// SnapshotContext is Snapshot with cancellation checks at every collection and
// recursive node boundary. It lets bounded navigation builds stop before a
// large retained tree has been copied in full.
func (t Tree) SnapshotContext(ctx context.Context) (Tree, error) {
	needsYou, err := cloneTreeNodesContext(ctx, t.NeedsYou)
	if err != nil {
		return Tree{}, err
	}
	live, err := cloneTreeNodesContext(ctx, t.Live)
	if err != nil {
		return Tree{}, err
	}
	projects, err := cloneTreeProjectsContext(ctx, t.Projects)
	if err != nil {
		return Tree{}, err
	}
	archived, err := cloneTreeProjectsContext(ctx, t.ArchivedProjects)
	if err != nil {
		return Tree{}, err
	}
	favorites, err := cloneTreeNodesContext(ctx, t.favoriteLive)
	if err != nil {
		return Tree{}, err
	}
	return Tree{NeedsYou: needsYou, Live: live, Projects: projects, ArchivedProjects: archived, favoriteLive: favorites}, nil
}

func cloneTreeProjectsContext(ctx context.Context, projects []TreeProject) ([]TreeProject, error) {
	out := make([]TreeProject, len(projects))
	for index, project := range projects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out[index] = project
		var err error
		if out[index].Current, err = cloneTreeNodesContext(ctx, project.Current); err != nil {
			return nil, err
		}
		if out[index].Recent, err = cloneTreeNodesContext(ctx, project.Recent); err != nil {
			return nil, err
		}
		if out[index].Archived, err = cloneTreeNodesContext(ctx, project.Archived); err != nil {
			return nil, err
		}
		if out[index].allCurrent, err = cloneTreeNodesContext(ctx, project.allCurrent); err != nil {
			return nil, err
		}
		if out[index].allRecent, err = cloneTreeNodesContext(ctx, project.allRecent); err != nil {
			return nil, err
		}
		if out[index].allArchived, err = cloneTreeNodesContext(ctx, project.allArchived); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func cloneTreeNodesContext(ctx context.Context, nodes []TreeNode) ([]TreeNode, error) {
	if nodes == nil {
		return nil, ctx.Err()
	}
	out := make([]TreeNode, len(nodes))
	for index, node := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out[index] = node
		out[index].RunningJobs = appwire.CloneEvenerJobs(node.RunningJobs)
		out[index].CompletedJobs = appwire.CloneEvenerJobs(node.CompletedJobs)
		children, err := cloneTreeNodesContext(ctx, node.Children)
		if err != nil {
			return nil, err
		}
		out[index].Children = children
	}
	return out, nil
}

// FavoriteCandidates returns every uncapped, top-level session row that is
// eligible for the pinned tier. It deliberately reads the retained full tier
// slices, while excluding archived projects and synthetic cluster rows.
func (t Tree) FavoriteCandidates() []TreeNode {
	var out []TreeNode
	seen := make(map[string]struct{})
	appendNode := func(node TreeNode) {
		if node.ID == "" {
			return
		}
		if _, ok := seen[node.ID]; ok {
			return
		}
		seen[node.ID] = struct{}{}
		out = append(out, node)
	}
	for _, node := range t.favoriteLive {
		appendNode(node)
	}
	for _, project := range t.Projects {
		for _, rows := range [][]TreeNode{project.allCurrent, project.allRecent} {
			for _, node := range rows {
				switch node.Kind {
				case "session", "fork":
					appendNode(node)
				case "cluster":
					for _, child := range node.Children {
						if child.Kind == "session" || child.Kind == "fork" {
							appendNode(child)
						}
					}
				}
			}
		}
	}
	return out
}

// PinCandidates returns every uncapped, authoritative top-level session that
// may populate a named pin section. Unlike FavoriteCandidates, named pins stay
// reachable when their session or whole project is archived. Synthetic cluster
// rows and fork rows are excluded, while real session children grouped beneath
// a synthetic cluster remain candidates under their canonical identities.
func (t Tree) PinCandidates() []TreeNode {
	var out []TreeNode
	seen := make(map[string]struct{})
	appendNode := func(node TreeNode) {
		if node.ID == "" || node.Kind != "session" {
			return
		}
		if _, ok := seen[node.ID]; ok {
			return
		}
		seen[node.ID] = struct{}{}
		out = append(out, node)
	}
	appendRows := func(rows []TreeNode) {
		for _, node := range rows {
			switch node.Kind {
			case "session":
				appendNode(node)
			case "cluster":
				for _, child := range node.Children {
					appendNode(child)
				}
			}
		}
	}
	for _, node := range t.favoriteLive {
		appendNode(node)
	}
	appendProject := func(project TreeProject) {
		appendRows(project.allCurrent)
		appendRows(project.allRecent)
		appendRows(project.allArchived)
	}
	for _, project := range t.Projects {
		appendProject(project)
	}
	for _, project := range t.ArchivedProjects {
		appendProject(project)
	}
	return out
}

// FavoriteNodeAuthorities exposes node kinds from the uncapped tree rows so
// read-time favorite classification can recognize current synthetic clusters.
// The caller supplies source and lineage completeness for session identities;
// these node facts are only about the current presentation classification.
func (t Tree) FavoriteNodeAuthorities() []FavoriteNodeAuthority {
	var out []FavoriteNodeAuthority
	appendRows := func(rows []TreeNode) {
		for _, node := range rows {
			if node.ID == "" {
				continue
			}
			out = append(out, FavoriteNodeAuthority{
				ID:      node.ID,
				Kind:    FavoriteNodeKind(node.Kind),
				Quality: FavoriteAuthorityComplete,
			})
		}
	}
	appendProject := func(project TreeProject) {
		appendRows(project.allCurrent)
		appendRows(project.allRecent)
		appendRows(project.allArchived)
	}
	for _, project := range t.Projects {
		appendProject(project)
	}
	for _, project := range t.ArchivedProjects {
		appendProject(project)
	}
	return out
}

// TreeProject groups sessions by canonical project identity. Its sessions are
// split into activity tiers (Current / Recent / Archived) computed from each
// session's last activity and archive decision.
type TreeProject struct {
	Name string
	// Key is the canonical identifier.Project.ID (or "no-project" for pathless
	// sessions). Name stays the basename for display.
	Key        string
	WorkingDir string // absolute path of the project's working directory; used to prefill the spawn form
	// Sessions split by activity tier:
	//   - Current  – last activity ≤ 24h, not archived.
	//   - Recent   – last activity 24h–2wk, not archived.
	//   - Archived – effective-archived (auto >2wk, or manual decision).
	Current  []TreeNode
	Recent   []TreeNode
	Archived []TreeNode
	// IsArchived is true when the project lives in the bottom Archived-projects
	// group: manually archived, or no non-archived (Current/Recent) sessions.
	IsArchived bool
	// IsTestRun is true when the project has at least one session and every
	// session in it carries Origin=="test" (EVENER_SESSION_ORIGIN=test): the
	// whole project is agentic-testing output rather than real work. The hub
	// routes such projects into a dedicated "Test runs" group, taking
	// precedence over IsArchived.
	IsTestRun bool
	// LastActivity is the most recent last-touched moment (max last-activity,
	// i.e. OrderUpdatedAt) across the project's top-level sessions; active
	// projects are ordered by it, desc.
	LastActivity time.Time
	RollupState  string // highest-attention state across this project's live sessions; "" if none
	// Magnitude rollup: how many of the project's live sessions are working
	// (RollupLive) vs. awaiting input (RollupAttn). The header renders these as
	// "⟳N · ◆M" so "how many need me" is legible without expanding — the single
	// rollup dot couldn't say that. Idle/ended sessions count toward neither.
	RollupLive int
	RollupAttn int
	// Expanded is the sidebar's auto-open rule: a project's children render
	// inline (and its disclosure starts open) only when it has a live session —
	// RollupLive > 0 || RollupAttn > 0. Everything else starts collapsed and
	// lazy-loads its children on first expand, which keeps the default payload
	// small when hundreds of idle/archived projects exist.
	Expanded bool
	// More* hold the per-tier overflow beyond maxSidebarSessionsPerTier. The
	// kept rows are the most-recent N; the sidebar shows "+N older" for the rest.
	MoreCurrent  int
	MoreRecent   int
	MoreArchived int
	// The uncapped tier slices remain available for an explicit page request.
	// They are intentionally private: the baseline tree only exposes the kept
	// rows and additive overflow counts, while Page is the controlled seam for
	// revealing older rows.
	allCurrent  []TreeNode
	allRecent   []TreeNode
	allArchived []TreeNode
	Age         string // pre-formatted relative age of LastActivity ("now", "2m", "3h", "5d")
	// Worktrees is the count of distinct non-empty WorktreePath values across
	// the project's sessions, surfaced in the delete confirmation.
	Worktrees int
}

// TotalSessionCount returns the authoritative number of top-level rows in a
// project. It uses the uncapped tier slices retained for pagination rather
// than adding the capped-away counts, which are pagination metadata.
func (p TreeProject) TotalSessionCount() int {
	if p.allCurrent != nil || p.allRecent != nil || p.allArchived != nil {
		return len(p.allCurrent) + len(p.allRecent) + len(p.allArchived)
	}
	return len(p.Current) + len(p.Recent) + len(p.Archived)
}

const (
	currentWindow = 24 * time.Hour
	archiveWindow = 14 * 24 * time.Hour
	// maxSidebarSessionsPerTier caps how many session rows each tier
	// (Current/Recent/Archived) emits in the sidebar. A project with hundreds
	// of one-shot runs would otherwise bloat the partial; the kept rows are the
	// most-recent N and the overflow is summarised as a quiet "+N older" note.
	maxSidebarSessionsPerTier = 50
	// SidebarSessionPageSize is the maximum number of capped-away rows a
	// single rail pagination request may reveal.
	SidebarSessionPageSize = maxSidebarSessionsPerTier
)

// capTier keeps the first n rows (the input is already most-recent first) and
// returns the kept slice plus the overflow count.
func capTier(rows []TreeNode, n int) ([]TreeNode, int) {
	if len(rows) <= n {
		return rows, 0
	}
	return rows[:n], len(rows) - n
}

// Page returns one bounded, ordered slice from a project's uncapped tier and
// the number still beyond the returned end. The baseline tree keeps only the
// newest 50 rows, so callers use the same offset to reveal the next page
// without changing the tier's ordering contract.
func (p TreeProject) Page(tier string, offset, limit int) ([]TreeNode, int, bool) {
	if offset < 0 || limit <= 0 {
		return nil, 0, false
	}
	rows, ok := p.TierRows(tier)
	if !ok {
		return nil, 0, false
	}
	if offset >= len(rows) {
		return []TreeNode{}, 0, true
	}
	end := offset + limit
	end = min(end, len(rows))
	return rows[offset:end], len(rows) - end, true
}

// TierRows returns a project's authoritative, ordered tier rows. It exposes
// the retained uncapped slices to immutable snapshot consumers without making
// them reconstruct a page by scanning or using an artificial offset.
func (p TreeProject) TierRows(tier string) ([]TreeNode, bool) {
	switch tier {
	case "current":
		if p.allCurrent != nil {
			return p.allCurrent, true
		}
		return p.Current, true
	case "recent":
		if p.allRecent != nil {
			return p.allRecent, true
		}
		return p.Recent, true
	case "archived":
		if p.allArchived != nil {
			return p.allArchived, true
		}
		return p.Archived, true
	default:
		return nil, false
	}
}

// classifySession returns a session's sidebar tier from its last activity and
// archive decision. A user decision (archive/unarchive) overrides the auto rule;
// otherwise inactivity older than archiveWindow auto-archives.
func classifySession(decision *bool, lastActivity, now time.Time) string {
	archived := now.Sub(lastActivity) > archiveWindow
	if decision != nil {
		archived = *decision
	}
	if archived {
		return "archived"
	}
	if now.Sub(lastActivity) <= currentWindow {
		return "current"
	}
	return "recent"
}

// TreeNode represents a row in the sidebar.
//
// Kind:
//   - "session"  – top-level session
//   - "subagent" - created by delegate (purple dot, indented)
//   - "fork"     – branched session (⎇ glyph, same indent as session, dim)
//   - "cluster"  – a fold of N same-titled idle sessions (mockup #10/#C); the
//     individual runs are the cluster's Children and ClusterCount is N.
type TreeNode struct {
	ID string
	// Ref is the canonical navigation identity advertised by a live daemon. It
	// is empty when the metadata ID is already the canonical navigation identity.
	Ref        string
	Title      string
	Project    string
	Branch     string // git branch at session start; empty when unknown
	State      string // "errored" | "awaiting" | "active" | "warning" | "idle" | "ended"
	AskPending bool   // true while the daemon reports an unanswered ask_user question
	// Dormant is true for a session that has never run: no model response and
	// no accepted user input. An empty-prompt spawn creates one, and it reports
	// State "idle" — the same word a session that ran and finished reports — so
	// without this fact the two are indistinguishable to every consumer.
	//
	// It sits BESIDE State rather than inside that vocabulary because the two
	// answer independent questions: a session prompted a moment ago is
	// legitimately "active" with nothing in its history yet. Keeping them apart
	// is also what leaves rollup state, AttentionRank and NeedsYouBand
	// untouched — none of them gains a case to learn.
	Dormant      bool
	Kind         string // "session" | "subagent" | "fork" | "cluster"
	ClusterCount int    // for Kind=="cluster": number of folded same-titled runs
	// MoreSubagents is the number of subagent children omitted by the sidebar
	// cap. The client folds this into the parent's inactive-child disclosure so
	// capped children are counted rather than silently disappearing.
	MoreSubagents int
	// RunningJobs and CompletedJobs are the non-delegate jobs owned by this
	// session. Delegate jobs remain represented by Children as subagents.
	RunningJobs   []appwire.EvenerJobInfo
	CompletedJobs []appwire.EvenerJobInfo
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Age           string // pre-formatted "now", "2m", "3h", "5d"
	Children      []TreeNode
}

// AgeString formats a duration since t as a human-readable string.
func AgeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// EffectiveWorkingDir returns the directory the hub should treat as a
// session's home for grouping, spawn prefill, and resume. When the session
// is actively inside a worktree (managed or path-entered — native worktree
// tools spec §7 "Hub consumers of the persisted working dir must migrate"),
// that's the restore root the worktree was entered from, not the worktree
// path itself: grouping/prefilling/resuming by the worktree path would land
// on a phantom project named after the worktree leaf (e.g. "dlg_01H...") or
// hub-driven `--dir` the session straight into the worktree, bypassing the
// lock and validation rules Task 18's resume re-entry applies. Otherwise
// it's just the session's persisted working directory.
func EffectiveWorkingDir(m schema.SessionMeta) string {
	if m.WorktreePath != "" && m.WorktreeRestoreRoot != "" {
		return m.WorktreeRestoreRoot
	}
	return m.EnvInfo.WorkingDir
}

// projectName returns the sidebar project label for a session meta.
func projectName(m schema.SessionMeta) string {
	wd := EffectiveWorkingDir(m)
	if wd == "" {
		return "(no project)"
	}
	return filepath.Base(wd)
}

// nodeTitle computes the display title for a tree node.
//
// A session with neither a generated name nor a prompt falls back to a short,
// human-friendlier rendering of its ID rather than the full 22-character
// UUIDv7 base62 payload, which clutters the sidebar. Older sessions persisted
// before OriginalPrompt was captured are one such case; an empty-prompt spawn
// (kata ytpa), which starts a session dormant and unnamed, is the ordinary one.
//
// The fallback triggers on SessionDisplayName having nothing better to offer
// than the ID itself, not on it returning "" — returning the bare ID IS its
// documented last resort, so an emptiness check could only ever fire for a
// session with no ID at all. Testing for the ID also keeps this row agreeing
// with the live tier, which renders ShortID directly while the past index
// catches up: without it a freshly spawned session showed the short form for a
// few seconds and then replaced it with the raw payload.
func nodeTitle(m schema.SessionMeta, kind string) string {
	base := truncateTitle(schema.SessionDisplayName(m))
	if base == "" || base == strings.TrimSpace(m.ID) {
		base = ShortID(strings.TrimSpace(m.ID))
	}
	if kind == "fork" && m.ForkLabel != "" {
		return base + " · " + m.ForkLabel
	}
	return base
}

// maxTitleRunes caps sidebar node titles. Titles fall back to the session's
// full OriginalPrompt, which can run to tens of kilobytes; a one-line sidebar
// row only ever shows the first couple hundred characters, and shipping the
// full prompt for every archived session made the navigation payload
// megabytes heavier than it needed to be. The full prompt remains available
// from the session detail endpoints.
const maxTitleRunes = 200

// truncateTitle caps s at maxTitleRunes runes, appending an ellipsis when it
// truncates. Rune-safe: never splits a multi-byte character.
func truncateTitle(s string) string {
	r := []rune(s)
	if len(r) <= maxTitleRunes {
		return s
	}
	return string(r[:maxTitleRunes-1]) + "…"
}

// ShortID renders an unnamed session ID compactly.
func ShortID(id string) string {
	if len(id) <= 14 {
		return id
	}
	return "session " + id[len(id)-6:]
}

// NormalizeState accepts Codex thread status terms and maps them to hub UI
// display states.
func NormalizeState(s string) string {
	switch s {
	case "":
		return "idle"
	case appwire.ThreadStatusAwaiting:
		return "awaiting"
	case appwire.ThreadStatusActive:
		return "active"
	case appwire.ThreadStatusSystemError:
		return "errored" // first-class red error lane (spec v5) — no longer grouped with awaiting
	case appwire.ThreadStatusRestartRequired:
		return appwire.ThreadStatusRestartRequired
	case appwire.ThreadStatusWarning:
		return "warning"
	case appwire.ThreadStatusIdle:
		return "idle"
	case appwire.ThreadStatusClosed, "ended":
		return "ended"
	case "errored":
		return "errored"
	case appwire.ThreadStatusNotLoaded:
		return "notLoaded"
	default:
		return "notLoaded"
	}
}

// nodeKind returns "fork", "subagent", or "session" for a meta.
//
// A "fork" is the *snapshotted original* of an edit — the older branch that
// was preserved when the user edited a prior message. It carries the
// user-supplied ForkLabel.  The newer (active) branch carries
// ParentSessionID but no ForkLabel and renders as a normal session.
func nodeKind(m schema.SessionMeta) string {
	if m.IsSubagent {
		return "subagent"
	}
	if m.ForkLabel != "" {
		return "fork"
	}
	return "session"
}

// nestedSessionIDs computes, from metas alone (no project resolution, no
// disk I/O), which session IDs render nested under another top-level row
// instead of getting one of their own: subagents (nested under their
// parent) and fork-superseded parents (ForkLabel set — the snapshotted
// original of an edited message, see nodeKind's doc — nested under the
// active branch that superseded it). forkChildren (origin_id -> latest
// non-subagent child ID) is also returned since assembling the tree needs
// it again to find which top-level row a fork-superseded parent attaches
// under.
//
// BuildTree's project/tier assembly and DeriveAttention's tier-eligible
// check (attention.go's tierEligible) both call this one function, so a
// session can never be top-level to one and nested (hence excluded) to the
// other.
func nestedSessionIDs(metas []schema.SessionMeta) (nested map[string]struct{}, forkChildren map[string]string) {
	forkChildren = make(map[string]string)
	for _, m := range metas {
		if m.ParentSessionID != "" && !m.IsSubagent {
			forkChildren[m.ParentSessionID] = m.ID
		}
	}
	nested = make(map[string]struct{})
	for _, m := range metas {
		switch {
		case m.IsSubagent && m.ParentSessionID != "":
			nested[m.ID] = struct{}{}
		case m.ForkLabel != "":
			if _, ok := forkChildren[m.ID]; ok {
				nested[m.ID] = struct{}{}
			}
		}
	}
	return nested, forkChildren
}

// liveParentOrForkContinuation returns the ID of the row a nested session
// attaches under: a subagent's direct parent, or a fork-superseded parent's
// active continuation (the child whose ParentSessionID names it). Returns ""
// when the meta is neither a subagent nor a fork-superseded parent. The Live
// tier uses this to decide whether a nested live session has a live parent to
// nest under; if not, it keeps its own top-level row rather than vanishing.
func liveParentOrForkContinuation(m schema.SessionMeta, forkChildren map[string]string) string {
	if m.IsSubagent && m.ParentSessionID != "" {
		return m.ParentSessionID
	}
	if childID, ok := forkChildren[m.ID]; ok {
		return childID
	}
	return ""
}

// pruneLiveSubtree keeps only live descendants of node (live = present in
// liveMap, or an in-process child in runningSubagentIDs). The node itself is
// always kept — the Live tier calls this only on live top-level rows.
// MoreSubagents is zeroed: buildNode capped subagent children before pruning,
// so the count reflected a live/non-live mix; after pruning only live children
// remain and a live-only cap is meaningless on a tier that is itself all-live.
func pruneLiveSubtree(node TreeNode, liveMap map[string]LiveEntry, runningSubagentIDs map[string]bool) TreeNode {
	kept := make([]TreeNode, 0, len(node.Children))
	for _, child := range node.Children {
		if !isLiveID(child.ID, liveMap, runningSubagentIDs) {
			continue
		}
		pruned := pruneLiveSubtree(child, liveMap, runningSubagentIDs)
		kept = append(kept, pruned)
	}
	node.Children = kept
	node.MoreSubagents = 0
	return node
}

func isLiveID(id string, liveMap map[string]LiveEntry, runningSubagentIDs map[string]bool) bool {
	if _, ok := liveMap[id]; ok {
		return true
	}
	return runningSubagentIDs[id]
}

// TopLevelSessionIDs returns the session IDs that the navigation tree treats
// as independently addressable rows. It is intentionally based on the full
// metadata snapshot so callers do not mistake a capped rail projection for
// the complete set of valid top-level sessions.
func TopLevelSessionIDs(metas []schema.SessionMeta) map[string]struct{} {
	nested, _ := nestedSessionIDs(metas)
	ids := make(map[string]struct{}, len(metas))
	for _, m := range metas {
		if m.ID == "" || m.IsSubagent {
			continue
		}
		if _, ok := nested[m.ID]; ok {
			continue
		}
		ids[m.ID] = struct{}{}
	}
	return ids
}

// BuildTree assembles the sidebar Tree from all session metas, the
// currently-live set, and the user's explicit archive decisions, using the
// current wall clock for tier classification.
func BuildTree(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool) Tree {
	return BuildTreeAt(metas, live, decisions, time.Now())
}

// BuildTreeWithProjects builds a tree from identities resolved by the caller
// at ingestion. The project map is keyed by the effective working directory;
// grouping and ordering do not perform filesystem or Git resolution.
func BuildTreeWithProjects(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool, projects map[string]identifier.Project) Tree {
	return BuildTreeAtWithProjects(metas, live, decisions, time.Now(), projects)
}

// BuildTreeAt is BuildTree with an injected clock so tier classification
// (Current/Recent/Archived, by last-activity age) is deterministic in tests.
func BuildTreeAt(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool, now time.Time) Tree {
	projects := ResolveProjectMap(metas, live)
	return BuildTreeAtWithProjects(metas, live, decisions, now, projects)
}

// ResolveProjectMap resolves every distinct live/past working directory once
// at the tree ingestion boundary. Failed resolutions are intentionally absent;
// pathless entries remain in the presentation-only no-project bucket.
func ResolveProjectMap(metas []schema.SessionMeta, live []LiveEntry) map[string]identifier.Project {
	projects, _ := resolveProjectMap(metas, live, false)
	return projects
}

// ResolveProjectMapStrict resolves the same working-directory identities as
// ResolveProjectMap, but returns an error if any non-empty path cannot be
// resolved. Destructive callers use this form so they cannot act on a partial
// view of canonical project membership.
func ResolveProjectMapStrict(metas []schema.SessionMeta, live []LiveEntry) (map[string]identifier.Project, error) {
	return resolveProjectMap(metas, live, true)
}

func resolveProjectMap(metas []schema.SessionMeta, live []LiveEntry, strict bool) (map[string]identifier.Project, error) {
	projects := make(map[string]identifier.Project)
	for _, m := range metas {
		path := EffectiveWorkingDir(m)
		if path == "" {
			continue
		}
		if _, ok := projects[path]; ok {
			continue
		}
		project, err := identifier.ResolveProject(path)
		if err != nil {
			if strict {
				return nil, fmt.Errorf("resolve project %q: %w", path, err)
			}
			continue
		}
		projects[path] = project
	}
	for _, le := range live {
		path := le.WorkingDir
		if path == "" {
			continue
		}
		if _, ok := projects[path]; ok {
			continue
		}
		if le.Project.ID != "" {
			projects[path] = le.Project
			continue
		}
		project, err := identifier.ResolveProject(path)
		if err != nil {
			if strict {
				return nil, fmt.Errorf("resolve project %q: %w", path, err)
			}
			continue
		}
		projects[path] = project
	}
	return projects, nil
}

// treeMetasWithLive combines indexed metadata with live-only project sessions.
func treeMetasWithLive(metas []schema.SessionMeta, live []LiveEntry, now time.Time, resolvedProjects map[string]identifier.Project) ([]schema.SessionMeta, map[string]bool) {
	superseded := supersededSessionIDs(live)
	replacementSources := replacementMetadataSources(metas, live)
	effectiveMetas := make([]schema.SessionMeta, 0, len(metas))
	sessionIsIndexed := make(map[string]bool, len(metas))
	for _, m := range metas {
		if _, replaced := superseded[m.ID]; replaced {
			continue
		}
		effectiveMetas = append(effectiveMetas, m)
		if m.ID == "" {
			continue
		}
		sessionIsIndexed[m.ID] = true
	}
	// A newly-created daemon can appear in the roster before the past index's
	// next rebuild. Give live sessions with a resolved project identity a
	// temporary metadata record so they still populate that project's catalog.
	// Pathless and unresolved live sessions remain in the flat Live tier until
	// they acquire metadata or a project identity, except for a replacement
	// whose retired metadata can provide the same temporary project anchor.
	for _, le := range live {
		if le.SessionID == "" || le.WorkingDir == "" {
			continue
		}
		_, replacement := replacementSources[le.SessionID]
		if _, resolved := resolvedProjects[le.WorkingDir]; !resolved && !replacement {
			continue
		}
		if _, exists := sessionIsIndexed[le.SessionID]; exists {
			continue
		}
		startedAt := le.StartedAt
		if startedAt.IsZero() {
			startedAt = now
		}
		meta := schema.SessionMeta{
			ID:        le.SessionID,
			CreatedAt: startedAt,
			UpdatedAt: startedAt,
			EnvInfo:   schema.EnvironmentInfo{WorkingDir: le.WorkingDir},
		}
		if source, ok := replacementSources[le.SessionID]; ok {
			// The replacement is a new thread instance. Carry only the
			// environment/project identity needed to keep it catalogued while
			// its own metadata lands; its title, prompt, and lineage belong to
			// the retired instance and must not be copied into the new row.
			meta.EnvInfo = source.EnvInfo
			meta.Origin = source.Origin
			meta.WorktreePath = source.WorktreePath
			meta.WorktreeManaged = source.WorktreeManaged
			meta.WorktreeRestoreRoot = source.WorktreeRestoreRoot
			if le.WorkingDir != "" {
				meta.EnvInfo.WorkingDir = le.WorkingDir
			}
		}
		effectiveMetas = append(effectiveMetas, meta)
		sessionIsIndexed[le.SessionID] = false
	}
	return effectiveMetas, sessionIsIndexed
}

func liveWorkspaceIdentity(entry LiveEntry) (string, appwire.Ref, bool) {
	currentID := strings.TrimSpace(entry.SessionID)
	ref, err := appwire.ParseRef(strings.TrimSpace(entry.WorkspaceRef))
	if err != nil || currentID == "" || (entry.SourceID != "" && ref.SourceID != entry.SourceID) {
		return "", appwire.Ref{}, false
	}
	return currentID, ref, true
}

// supersededSessionIDs identifies persisted instance IDs that a live daemon
// has replaced under a stable workspace ref. Keeping those stale metadata rows
// in the navigation tree would render the same logical session twice.
func supersededSessionIDs(live []LiveEntry) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, entry := range live {
		currentID, ref, ok := liveWorkspaceIdentity(entry)
		if !ok || ref.ThreadID == currentID {
			continue
		}
		ids[ref.ThreadID] = struct{}{}
	}
	return ids
}

func replacementMetadataSources(metas []schema.SessionMeta, live []LiveEntry) map[string]schema.SessionMeta {
	byID := make(map[string]schema.SessionMeta, len(metas))
	for _, meta := range metas {
		if meta.ID == "" {
			continue
		}
		current, exists := byID[meta.ID]
		if !exists || sessionMetaLess(meta, current) {
			byID[meta.ID] = meta
		}
	}
	sources := make(map[string]schema.SessionMeta)
	for _, entry := range live {
		currentID, ref, ok := liveWorkspaceIdentity(entry)
		if !ok || ref.ThreadID == currentID {
			continue
		}
		if source, ok := byID[ref.ThreadID]; ok {
			sources[currentID] = source
		}
	}
	return sources
}

func projectKeyForPath(path string, resolvedProjects map[string]identifier.Project) string {
	if project := resolvedProjects[path]; project.ID != "" {
		return project.ID
	}
	return "no-project"
}

// BuildTreeAtWithProjects assembles a tree using projects resolved by the
// caller. The map is keyed by EffectiveWorkingDir; it prevents resolver calls
// from leaking into grouping/sorting helpers.
func BuildTreeAtWithProjects(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool, now time.Time, resolvedProjects map[string]identifier.Project) Tree {
	effectiveMetas, sessionIsIndexed := treeMetasWithLive(metas, live, now, resolvedProjects)
	return buildTreeAtWithProjects(effectiveMetas, live, decisions, now, resolvedProjects, sessionIsIndexed)
}

func buildTreeAtWithProjects(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool, now time.Time, resolvedProjects map[string]identifier.Project, sessionIsIndexed map[string]bool) Tree {
	sort.SliceStable(metas, func(i, j int) bool {
		return sessionMetaLess(metas[i], metas[j])
	})
	// Metadata can be malformed or concurrently assembled with duplicate IDs.
	// The sorted (newest-first) order makes the canonical policy deterministic:
	// retain the first record for each ID, then build all lineage indexes from
	// that canonical set so a duplicate cannot be emitted or hoisted elsewhere.
	canonicalMetas := metas[:0]
	seenMetaIDs := make(map[string]struct{}, len(metas))
	for _, m := range metas {
		if _, seen := seenMetaIDs[m.ID]; seen {
			continue
		}
		seenMetaIDs[m.ID] = struct{}{}
		canonicalMetas = append(canonicalMetas, m)
	}
	metas = canonicalMetas

	// Index live entries by SessionID.
	liveMap := make(map[string]LiveEntry, len(live))
	liveRefMap := make(map[string]string, len(live))
	runningSubagentIDs := make(map[string]bool)
	runningSubagentStates := make(map[string]string)
	for _, le := range live {
		if le.SessionID != "" {
			liveMap[le.SessionID] = le
			if _, ref, ok := liveWorkspaceIdentity(le); ok {
				liveRefMap[le.SessionID] = ref.String()
			}
		}
		for _, childID := range le.RunningSubagentIDs {
			if childID != "" {
				runningSubagentIDs[childID] = true
				if state := le.RunningSubagentStates[childID]; state != "" {
					runningSubagentStates[childID] = state
				}
			}
		}
	}
	metaMap := make(map[string]schema.SessionMeta, len(metas))
	for _, m := range metas {
		if m.ID != "" {
			metaMap[m.ID] = m
		}
	}

	// runningSubagentState resolves a live in-process child's display state:
	// the child's own projected status when its daemon carried one, else "idle".
	// Liveness alone must not read as activity — a settled, resumable delegate
	// stays listed while doing nothing, and an old daemon that carried no state
	// is no excuse to present a quiet child as working. Folding to idle keeps a
	// no-state child in the rail's inactive list where it belongs, one click
	// away — the conservative side, matching CURRENT_SUBAGENT_STATES' own logic.
	runningSubagentState := func(id string) string {
		if state, ok := runningSubagentStates[id]; ok {
			return NormalizeState(state)
		}
		return "idle"
	}

	// stateFor resolves the display state for a session ID.
	stateFor := func(id string) string {
		if le, ok := liveMap[id]; ok {
			return NormalizeState(le.Status)
		}
		if runningSubagentIDs[id] {
			return runningSubagentState(id)
		}
		return "ended"
	}

	// askPendingFor resolves the ask-pending marker for a session ID from the
	// same live map stateFor reads — every TreeNode builder below must set
	// AskPending from this, not just the NeedsYou triage tier, or a session
	// rendered elsewhere in the sidebar disagrees with its own NeedsYou tile.
	askPendingFor := func(id string) bool {
		return liveMap[id].PendingAsk
	}

	// dormantFor resolves "this session has never run" for a session ID, from
	// the same metaMap every builder below already consults — one closure, for
	// the same reason stateFor and askPendingFor are: a session listed in both
	// the Live tier and under its project must report the identical fact in
	// both places (tree_live_agreement_test.go).
	//
	// Deliberately conservative — dormant only when the meta records NEITHER a
	// model response NOR an accepted user input. A session that ran before
	// AcceptedInputTurns was persisted still carries a TurnCount, so it is
	// never mislabelled; and a session whose first turn is in flight, or which
	// failed before any response, carries an accepted input, so the row never
	// denies that the user asked it something. A synthetic live-only meta (or a
	// live entry the past index has not caught up with) reports false: the claim
	// is only ever made from indexed evidence.
	dormantFor := func(id string) bool {
		m, ok := metaMap[id]
		if !ok {
			return false
		}
		indexed, known := sessionIsIndexed[id]
		if !known || !indexed {
			return false
		}
		return m.TurnCount == 0 && m.AcceptedInputTurns == 0
	}

	// Group metas by canonical project identity while preserving each record's
	// explicit parent lineage.
	type projectAccum struct {
		name       string // basename for display
		topLevel   []schema.SessionMeta
		workingDir string          // the full grouping path ("" for no-project)
		worktrees  map[string]bool // distinct WorktreePath set
		count      int             // number of sessions seen for this project
		anyNonTest bool            // true once any session's Origin != "test"
		project    identifier.Project
		path       string
	}
	projects := make(map[string]*projectAccum) // keyed by canonical project ID or presentation path
	projectOrder := []string{}                 // insertion order for stable output
	resolveProject := func(path string) identifier.Project {
		if path == "" {
			return identifier.Project{}
		}
		return resolvedProjects[path]
	}
	// nestedMetaIDs/forkChildren: the shared lineage nesting (subagents, plus
	// fork-superseded parents) attention.go's tierEligible also draws from —
	// see nestedSessionIDs — so a session can't be top-level here and
	// tier-eligible there, or vice versa. forkChildren maps origin_id ->
	// latest_child_id: which session forked from each origin, used below to
	// attach a "snapshotted original" (the parent meta with ForkLabel set)
	// under the new active branch (the child whose ParentSessionID matches).
	// Per spec: the new active branch is top-level; the original is the dim
	// sibling.
	nestedMetaIDs, forkChildren := nestedSessionIDs(metas) // IDs attached below a top-level row
	childrenByParent := make(map[string][]schema.SessionMeta)

	// A subagent's persisted working directory may be an isolated worktree or
	// another effective directory. Its explicit parent is still authoritative
	// for sidebar lineage, so assign the record to the root parent's project
	// accumulator while retaining its own metadata for title/state rendering.
	lineageProjectPath := func(m schema.SessionMeta) string {
		path := EffectiveWorkingDir(m)
		seen := map[string]bool{m.ID: true}
		for m.IsSubagent && m.ParentSessionID != "" {
			parent, ok := metaMap[m.ParentSessionID]
			if !ok || seen[parent.ID] {
				break
			}
			seen[parent.ID] = true
			m = parent
			path = EffectiveWorkingDir(m)
		}
		return path
	}

	for _, m := range metas {
		path := lineageProjectPath(m)
		project := resolveProject(path)
		groupKey := path
		if project.ID != "" {
			groupKey = project.ID
		}
		acc := projects[groupKey]
		if acc == nil {
			displayPath := path
			if project.CanonicalPath != "" {
				displayPath = project.CanonicalPath
			}
			name := "(no project)"
			if displayPath != "" {
				name = filepath.Base(displayPath)
			}
			acc = &projectAccum{name: name, worktrees: map[string]bool{}, project: project, path: path}
			projects[groupKey] = acc
			projectOrder = append(projectOrder, groupKey)
		}
		// A carried fallback identity can know the project's ID without its
		// canonical path (the recorded working directory no longer resolves —
		// e.g. a deleted worktree). When a sibling session's identity for the
		// same project does carry the canonical path, adopt it: the group takes
		// the resolved checkout's name and working directory, not the dead
		// directory's leaf.
		if project.ID != "" && project.CanonicalPath != "" &&
			(acc.project.ID == "" || (acc.project.ID == project.ID && acc.project.CanonicalPath == "")) {
			acc.project = project
			acc.name = filepath.Base(project.CanonicalPath)
			acc.workingDir = project.CanonicalPath
		}
		if acc.project.ID == "" && project.ID != "" {
			acc.project = project
		}
		if acc.workingDir == "" && project.CanonicalPath != "" {
			acc.workingDir = project.CanonicalPath
		} else if acc.workingDir == "" && acc.path != "" {
			acc.workingDir = acc.path
		}
		if m.WorktreePath != "" {
			acc.worktrees[m.WorktreePath] = true
		}
		acc.count++
		if m.Origin != "test" {
			acc.anyNonTest = true
		}
		switch {
		case m.IsSubagent && m.ParentSessionID != "":
			// Subagents nest under their origin (membership: nestedSessionIDs).
			childrenByParent[m.ParentSessionID] = append(childrenByParent[m.ParentSessionID], m)
		case m.ForkLabel != "":
			// This meta is the snapshotted original of a fork. The active
			// branch (the meta whose ParentSessionID == m.ID) is top-level;
			// this one becomes the dim child of that active branch (membership:
			// nestedSessionIDs).
			if newID, ok := forkChildren[m.ID]; ok {
				childrenByParent[newID] = append(childrenByParent[newID], m)
			} else {
				// No active branch references this — keep top-level.
				acc.topLevel = append(acc.topLevel, m)
			}
		default:
			// Top-level: either no parent, or the active branch of a fork
			// (parent set but no ForkLabel of its own).
			acc.topLevel = append(acc.topLevel, m)
		}
	}

	// buildNode is the single recursive path for top-level rows and their
	// children. The path-local visited set prevents malformed lineage cycles
	// from recursing forever or hoisting a cycle member elsewhere.
	var buildNode func(schema.SessionMeta, string, *projectAccum, map[string]bool, bool) TreeNode
	buildNode = func(m schema.SessionMeta, kind string, acc *projectAccum, path map[string]bool, parentDead bool) TreeNode {
		path[m.ID] = true
		defer delete(path, m.ID)

		state := stateFor(m.ID)
		askPending := askPendingFor(m.ID)
		if parentDead {
			state = "ended"
			askPending = false
		}
		// A subagent's state already resolved through stateFor above: its own
		// live entry's status when it has one, else the parent's carried state
		// (or idle when the daemon carried none — liveness is not activity).
		// The previous override here was redundant for children without their
		// own live entry (stateFor already calls runningSubagentState) and wrong
		// for children WITH one (it overwrote the child's own daemon-reported
		// status with the parent's projection).
		node := TreeNode{
			ID:            m.ID,
			Ref:           liveRefMap[m.ID],
			Title:         nodeTitle(m, kind),
			Project:       acc.name,
			Branch:        m.EnvInfo.GitBranch,
			State:         state,
			AskPending:    askPending,
			Dormant:       dormantFor(m.ID),
			Kind:          kind,
			CreatedAt:     OrderCreatedAt(m.CreatedAt, m.UpdatedAt),
			UpdatedAt:     OrderUpdatedAt(m.UpdatedAt, m.CreatedAt),
			Age:           AgeString(OrderUpdatedAt(m.UpdatedAt, m.CreatedAt)),
			RunningJobs:   appwire.CloneEvenerJobs(liveMap[m.ID].RunningJobs),
			CompletedJobs: appwire.CloneEvenerJobs(liveMap[m.ID].CompletedJobs),
		}

		childMetas := childrenByParent[m.ID]
		var subagents, forks []schema.SessionMeta
		for _, c := range childMetas {
			if c.IsSubagent {
				subagents = append(subagents, c)
			} else if c.ForkLabel != "" {
				forks = append(forks, c)
			}
		}
		sort.SliceStable(subagents, func(i, j int) bool {
			return sessionMetaLess(subagents[i], subagents[j])
		})
		moreSubagents := 0
		if len(subagents) > maxSidebarSessionsPerTier {
			moreSubagents = len(subagents) - maxSidebarSessionsPerTier
			subagents = subagents[:maxSidebarSessionsPerTier]
		}
		node.MoreSubagents = moreSubagents
		sort.SliceStable(forks, func(i, j int) bool {
			return sessionMetaLess(forks[i], forks[j])
		})

		seenChildren := make(map[string]struct{}, len(subagents)+len(forks))
		appendChildren := func(children []schema.SessionMeta, childKind string) {
			for _, c := range children {
				if path[c.ID] {
					continue
				}
				if _, seen := seenChildren[c.ID]; seen {
					continue
				}
				seenChildren[c.ID] = struct{}{}
				childParentDead := childKind == "subagent" && node.State == "ended"
				node.Children = append(node.Children, buildNode(c, childKind, acc, path, childParentDead))
			}
		}
		appendChildren(subagents, "subagent")
		appendChildren(forks, "fork")
		return node
	}

	// Build the Projects slice.
	treeProjects := make([]TreeProject, 0, len(projectOrder))
	for _, path := range projectOrder {
		acc := projects[path]

		// Sort top-level sessions by the Hub session ordering contract.
		sort.SliceStable(acc.topLevel, func(i, j int) bool {
			return sessionMetaLess(acc.topLevel[i], acc.topLevel[j])
		})

		sessions := make([]TreeNode, 0, len(acc.topLevel))
		for _, m := range acc.topLevel {
			kind := nodeKind(m)
			sessions = append(sessions, buildNode(m, kind, acc, map[string]bool{}, false))
		}

		// Rollup: highest-attention state (for the dot fallback) plus the
		// magnitude counts the header renders. Each top-level session and its
		// children form one task tree: child activity keeps the project working,
		// but cannot inflate the count beyond one for that task tree.
		rollup := ""
		rollupLive, rollupAttn := 0, 0
		for _, s := range sessions {
			taskState := s.State
			var includeDescendants func(TreeNode)
			includeDescendants = func(node TreeNode) {
				if len(node.RunningJobs) > 0 && hubapi.RollupRank("active") > hubapi.RollupRank(taskState) {
					taskState = "active"
				}
				for _, child := range node.Children {
					if hubapi.RollupRank(child.State) > hubapi.RollupRank(taskState) {
						taskState = child.State
					}
					includeDescendants(child)
				}
			}
			includeDescendants(s)
			if hubapi.RollupRank(taskState) > hubapi.RollupRank(rollup) {
				rollup = taskState
			}
			switch taskState {
			case "awaiting", "warning", "errored", appwire.ThreadStatusRestartRequired:
				rollupAttn++
			case "active":
				rollupLive++
			}
		}

		// lastActivity: the project's most recent last-touched moment (max
		// last-activity across its top-level sessions). s.UpdatedAt is already
		// the normalized last-activity value (OrderUpdatedAt(m.UpdatedAt,
		// m.CreatedAt), applied when each node was built above), so this just
		// takes the max of it. Used to order active projects by last activity,
		// not by when a session merely started.
		var lastActivity time.Time
		for _, s := range sessions {
			if s.UpdatedAt.After(lastActivity) {
				lastActivity = s.UpdatedAt
			}
		}

		// Split the project's (clustered) rows into activity tiers. A session's
		// tier is its effective-archived classification: a user decision overrides
		// the auto rule, otherwise inactivity > 2 weeks auto-archives.
		var current, recent, archived []TreeNode
		for _, row := range clusterRepeatedTitles(sessions) {
			switch classifySession(decisionFor(decisions, row.ID), row.UpdatedAt, now) {
			case "current":
				current = append(current, row)
			case "recent":
				recent = append(recent, row)
			default:
				archived = append(archived, row)
			}
		}

		// Project placement: a project is archived when manually archived or when
		// it has no non-archived (Current/Recent) sessions.
		projectKey := acc.project.ID
		if projectKey == "" {
			projectKey = "no-project"
			acc.workingDir = ""
		}
		isArchived := projectArchivedDecision(decisions, projectKey) ||
			(len(current) == 0 && len(recent) == 0)

		// Cap each tier so a project with hundreds of runs can't bloat the
		// sidebar payload; the kept rows are the most-recent N (already ordered).
		allCurrent, allRecent, allArchived := current, recent, archived
		current, moreCurrent := capTier(allCurrent, maxSidebarSessionsPerTier)
		recent, moreRecent := capTier(allRecent, maxSidebarSessionsPerTier)
		archived, moreArchived := capTier(allArchived, maxSidebarSessionsPerTier)

		treeProjects = append(treeProjects, TreeProject{
			Name:         acc.name,
			Key:          projectKey,
			WorkingDir:   acc.workingDir,
			Worktrees:    len(acc.worktrees),
			Current:      current,
			Recent:       recent,
			Archived:     archived,
			IsArchived:   isArchived,
			IsTestRun:    acc.count > 0 && !acc.anyNonTest,
			LastActivity: lastActivity,
			RollupState:  rollup,
			RollupLive:   rollupLive,
			RollupAttn:   rollupAttn,
			// Auto-open only live projects (a working or awaiting session).
			Expanded:     rollupLive > 0 || rollupAttn > 0,
			MoreCurrent:  moreCurrent,
			MoreRecent:   moreRecent,
			MoreArchived: moreArchived,
			allCurrent:   allCurrent,
			allRecent:    allRecent,
			allArchived:  allArchived,
			Age:          AgeString(lastActivity),
		})
	}

	// Split active projects from archived ones; order each newest-first by the
	// project's last activity. SliceStable keeps the insertion order (already
	// recency-sorted via sessionMetaLess) as the tiebreak when last-activity
	// times are equal.
	activeProjects := make([]TreeProject, 0, len(treeProjects))
	archivedProjects := make([]TreeProject, 0)
	for _, p := range treeProjects {
		if p.IsArchived {
			archivedProjects = append(archivedProjects, p)
		} else {
			activeProjects = append(activeProjects, p)
		}
	}
	byLastActivityDesc := func(ps []TreeProject) func(i, j int) bool {
		return func(i, j int) bool { return ps[i].LastActivity.After(ps[j].LastActivity) }
	}
	sort.SliceStable(activeProjects, byLastActivityDesc(activeProjects))
	sort.SliceStable(archivedProjects, byLastActivityDesc(archivedProjects))

	// Build the Live slice: every live, top-level session, with its subagent
	// children nested the same way the Projects tier builds them (buildNode),
	// sorted by attention rank desc, then the Hub session ordering contract.
	// Archived sessions are filtered out after the sort, below.
	//
	// Only top-level sessions get their own row. Subagents and
	// fork-superseded parents (nested under their live continuation, per
	// nestedSessionIDs) nest under the row that owns their task tree, so they
	// render with the same foldout and active-status color as every other
	// section — not as flat, parentless top-level rows. This is the same
	// membership rule the NeedsYou tier (tierEligible) and the project
	// accumulator (topLevel) apply; all three read the one
	// nestedSessionIDs/forkChildren result, so they can never disagree about
	// which sessions are top-level.
	liveNodes := make([]TreeNode, 0, len(live))
	for _, le := range live {
		if le.SessionID == "" {
			continue
		}
		// A nested session (subagent, or a fork-superseded parent whose active
		// continuation is itself live) is NOT a top-level Live row when the
		// row it nests under is also live: it renders under that parent's
		// foldout instead. When the parent/continuation is NOT live, though,
		// the nested session has no live row to nest under — so it keeps its
		// own top-level Live row, the same way the project accumulator keeps a
		// fork-superseded parent top-level when no active branch references it.
		metaValue, hasMeta := metaMap[le.SessionID]
		if hasMeta {
			if parent := liveParentOrForkContinuation(metaValue, forkChildren); parent != "" {
				if _, ok := liveMap[parent]; ok {
					continue
				}
			}
		}
		// A live-only session the past index has not caught up with has no
		// meta; build a flat leaf node the way the original loop did. buildNode
		// needs a meta to resolve kind/title/project, and a session with none
		// has no lineage to recurse into.
		if !hasMeta {
			node := TreeNode{
				ID:            le.SessionID,
				Ref:           liveRefMap[le.SessionID],
				State:         stateFor(le.SessionID),
				AskPending:    askPendingFor(le.SessionID),
				Dormant:       dormantFor(le.SessionID),
				Kind:          "session",
				Title:         ShortID(le.SessionID),
				CreatedAt:     le.StartedAt,
				UpdatedAt:     le.StartedAt,
				Age:           AgeString(le.StartedAt),
				RunningJobs:   appwire.CloneEvenerJobs(le.RunningJobs),
				CompletedJobs: appwire.CloneEvenerJobs(le.CompletedJobs),
			}
			liveNodes = append(liveNodes, node)
			continue
		}
		kind := nodeKind(metaValue)
		node := buildNode(metaValue, kind, &projectAccum{name: projectName(metaValue)}, map[string]bool{}, false)
		// buildNode attaches every child the metadata knows about, including
		// non-live subagents and forks. The Live tier is only live sessions.
		node = pruneLiveSubtree(node, liveMap, runningSubagentIDs)
		liveNodes = append(liveNodes, node)
	}
	sort.SliceStable(liveNodes, func(i, j int) bool {
		ri, rj := hubapi.AttentionRank(liveNodes[i].State), hubapi.AttentionRank(liveNodes[j].State)
		if ri != rj {
			return ri > rj
		}
		return treeNodeLess(liveNodes[i], liveNodes[j], metaMap, liveMap)
	})

	// Drop archived sessions from the Live tier: an explicit session
	// decision, age-based auto-archive, or a manually archived project all
	// clear the row. Archive is a clearing verb (spec v5, round-4 A4/B7), so
	// an archived-but-still-running session must not linger in the live
	// rail; it stays reachable under its project's Archived tier. This is
	// the one exclusion rule favorite candidates already applied, so
	// favoriteLive below is exactly the filtered Live slice.
	unarchivedLive := make([]TreeNode, 0, len(liveNodes))
	for _, node := range liveNodes {
		entry := liveMap[node.ID]
		if entry.Project.ID != "" && projectArchivedDecision(decisions, entry.Project.ID) {
			continue
		}
		if decision := decisionFor(decisions, node.ID); decision != nil && *decision {
			continue
		}
		if _, hasMeta := metaMap[node.ID]; hasMeta && classifySession(decisionFor(decisions, node.ID), node.UpdatedAt, now) == "archived" {
			continue
		}
		unarchivedLive = append(unarchivedLive, node)
	}
	liveNodes = unarchivedLive

	// Build the NeedsYou triage tier: every top-level live session in the
	// awaiting|warning|errored family, plus any other live session promoted by
	// a pending sandbox-exemption escalation (M7), flat across all projects,
	// errors first and then oldest-blocked first so the queue works top-down.
	// Both "promoted" and "top-level" share their definition with
	// attention.go's AttentionSummary — promotedAttentionLevel and
	// tierEligible respectively — one function each, not a second,
	// independently-maintained copy, so the tier and the badge can never
	// drift apart again. Subagents and fork-superseded parents (nested under
	// their live continuation) are excluded via tierEligible, along with
	// plain working/idle sessions — this tier is strictly "what needs me
	// right now." An archived session is suppressed even while live: archive
	// is a clearing verb (spec v5, round-4 A4/B7), so an
	// archived-but-still-awaiting session must not linger in the inbox. When
	// nothing qualifies the tier is empty and the sidebar hides it entirely.
	needsYou := make([]TreeNode, 0, len(live))
	for _, le := range live {
		if le.SessionID == "" {
			continue
		}
		st := stateFor(le.SessionID)
		// Membership mirrors attention.go's AttentionSummary exactly: one shared
		// call, not a second, independently-maintained copy of this rule. The
		// node keeps reporting st below — promotion changes membership, not the
		// node's own state.
		if lvl := promotedAttentionLevel(st, le.PendingEscalation); lvl != "needs_you" && lvl != "error" {
			continue
		}
		metaValue, hasMeta := metaMap[le.SessionID]
		var meta *schema.SessionMeta
		if hasMeta {
			meta = &metaValue
		}
		// Same tierEligible call attention.go's AttentionSummary uses for its
		// own inclusion check: only top-level (not a subagent, not a
		// fork-superseded parent nested under its active continuation) and not
		// manually archived sessions surface in triage — a nested session's
		// live continuation is the actionable unit.
		if !tierEligible(le.SessionID, meta, nestedMetaIDs, decisions) {
			continue
		}
		node := TreeNode{
			ID:            le.SessionID,
			State:         st,
			Kind:          "session",
			AskPending:    le.PendingAsk,
			Dormant:       dormantFor(le.SessionID),
			RunningJobs:   appwire.CloneEvenerJobs(le.RunningJobs),
			CompletedJobs: appwire.CloneEvenerJobs(le.CompletedJobs),
		}
		if meta != nil {
			node.Title = nodeTitle(*meta, nodeKind(*meta))
			node.Project = projectName(*meta)
			node.Branch = meta.EnvInfo.GitBranch
			node.CreatedAt = OrderCreatedAt(meta.CreatedAt, meta.UpdatedAt)
			node.UpdatedAt = OrderUpdatedAt(meta.UpdatedAt, meta.CreatedAt)
			node.Age = AgeString(node.UpdatedAt)
		} else {
			node.Title = ShortID(le.SessionID)
			node.CreatedAt = le.StartedAt
			node.UpdatedAt = le.StartedAt
			node.Age = AgeString(le.StartedAt)
		}
		needsYou = append(needsYou, node)
	}
	// Three bands, oldest-first inside each band (Track A §2 ask-tiering):
	// errored (broken beats blocked) > ask-pending (blocked beats your-move) >
	// your-move (a generic amber settle). AttentionRank isn't used here — it
	// would also separate plain awaiting from warning, which both belong in
	// the your-move band unless ask-pending.
	sort.SliceStable(needsYou, func(i, j int) bool {
		bi, bj := hubapi.NeedsYouBand(needsYou[i].State, needsYou[i].AskPending), hubapi.NeedsYouBand(needsYou[j].State, needsYou[j].AskPending)
		if bi != bj {
			return bi > bj
		}
		return needsYou[i].UpdatedAt.Before(needsYou[j].UpdatedAt)
	})

	// Live already excludes archived sessions (the filter right after the
	// liveNodes sort above), which is exactly the candidate rule the pinned
	// tier wants — one shared result, not a second copy of the rule.
	favoriteLive := liveNodes

	return Tree{
		NeedsYou:         needsYou,
		Live:             liveNodes,
		Projects:         activeProjects,
		ArchivedProjects: archivedProjects,
		favoriteLive:     favoriteLive,
	}
}

// BuildProjectTree builds the single TreeProject identified by canonical ID, for the lazy
// sidebar expand endpoint, using the current wall clock.
func BuildProjectTree(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool, projectID string) (TreeProject, bool) {
	return BuildProjectTreeAt(metas, live, decisions, time.Now(), projectID)
}

// BuildProjectTreeAt is BuildProjectTree with an injected clock. It filters the
// effective metadata to the requested canonical project ID, runs the normal
// tree build on that subset, and returns the resulting TreeProject (searching
// both the active and archived lists). Explicit parent lineage determines a
// subagent's project, even when the subagent runs from another effective
// directory. A project with only a live session is also eligible before its
// metadata reaches the index. ok is false when no project with that ID exists.
func BuildProjectTreeAt(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool, now time.Time, projectID string) (TreeProject, bool) {
	resolvedProjects := ResolveProjectMap(metas, live)
	effectiveMetas, sessionIsIndexed := treeMetasWithLive(metas, live, now, resolvedProjects)
	metaByID := make(map[string]schema.SessionMeta, len(effectiveMetas))
	for _, m := range effectiveMetas {
		if m.ID != "" {
			metaByID[m.ID] = m
		}
	}
	belongsToProject := func(m schema.SessionMeta) bool {
		seen := map[string]bool{m.ID: true}
		for m.IsSubagent && m.ParentSessionID != "" {
			parent, ok := metaByID[m.ParentSessionID]
			if !ok || seen[parent.ID] {
				break
			}
			seen[parent.ID] = true
			m = parent
		}
		return projectKeyForPath(EffectiveWorkingDir(m), resolvedProjects) == projectID
	}
	subset := make([]schema.SessionMeta, 0, len(effectiveMetas))
	for _, m := range effectiveMetas {
		if belongsToProject(m) {
			subset = append(subset, m)
		}
	}
	if len(subset) == 0 {
		return TreeProject{}, false
	}
	tree := buildTreeAtWithProjects(subset, live, decisions, now, resolvedProjects, sessionIsIndexed)
	for _, p := range tree.Projects {
		if p.Key == projectID {
			return p, true
		}
	}
	// A non-empty subset always produces at least one project with this ID.
	// If none is active, the first archived project has the same precedence the
	// original search loop provided.
	return tree.ArchivedProjects[0], true
}

// decisionFor returns the explicit archive decision for a session ID as a
// *bool (nil when there is no decision), the shape classifySession wants. An
// empty ID (e.g. a synthesized cluster row) never has a decision.
func decisionFor(decisions map[ArchiveKey]bool, id string) *bool {
	if id == "" {
		return nil
	}
	if v, ok := decisions[ArchiveKey{Kind: "session", ID: id}]; ok {
		return &v
	}
	return nil
}

// projectArchivedDecision resolves a project's manual archive decision by its
// canonical project ID. Returns false when no row is present.
func projectArchivedDecision(decisions map[ArchiveKey]bool, projectID string) bool {
	if v, ok := decisions[ArchiveKey{Kind: "project", ID: projectID}]; ok {
		return v
	}
	return false
}

// clusterRepeatedTitles folds same-titled idle/ended sessions into a single
// "cluster" node so a project that ran the same one-shot prompt N times
// ("describe this image ×5") collapses to one row. A cluster's members become
// its Children and ClusterCount is N. A title only folds when EVERY session
// bearing it is clusterable: if any same-titled session is live / needs-you /
// has children, none of them cluster, because hiding live signal behind a fold
// defeats the sidebar. A title needs at least clusterMin members to fold.
//
// The cluster row takes the slot of the title's first (most-recent) appearance;
// the input recency order is otherwise preserved.
func clusterRepeatedTitles(sessions []TreeNode) []TreeNode {
	const clusterMin = 3

	// Tally per-title clusterable counts and whether the title is foldable
	// (no non-clusterable member shares it).
	clusterableByTitle := make(map[string]int)
	foldable := make(map[string]bool)
	for _, s := range sessions {
		if clusterable(s) {
			if _, seen := foldable[s.Title]; !seen {
				foldable[s.Title] = true
			}
			clusterableByTitle[s.Title]++
		} else {
			foldable[s.Title] = false
		}
	}

	out := make([]TreeNode, 0, len(sessions))
	emitted := make(map[string]bool)
	for _, s := range sessions {
		title := s.Title
		if !clusterable(s) || !foldable[title] || clusterableByTitle[title] < clusterMin {
			out = append(out, s)
			continue
		}
		if emitted[title] {
			continue // members other than the first are folded into the cluster
		}
		emitted[title] = true
		members := make([]TreeNode, 0, clusterableByTitle[title])
		for _, m := range sessions {
			if m.Title == title && clusterable(m) {
				members = append(members, m)
			}
		}
		out = append(out, TreeNode{
			ID:           clusterID(s.Project, title),
			Title:        title,
			Project:      s.Project,
			State:        "ended",
			Kind:         "cluster",
			ClusterCount: len(members),
			UpdatedAt:    s.UpdatedAt, // first (most-recent) member carries recency
			Age:          s.Age,
			Children:     members,
		})
	}
	return out
}

// clusterID is the stable synthetic id for a repeated-title cluster, scoped by
// project so equal titles in different projects never collide, and never empty
// (an empty id renders as an empty ref and collides all clusters in a project
// at RowID "project:<key>:" — round-2 A7/B4).
func clusterID(project, title string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + title))
	return "cluster:" + hex.EncodeToString(sum[:4])
}

// clusterable reports whether a session row may be folded into a repeated-title
// cluster: it must be a plain idle/ended session with no children of its own.
func clusterable(n TreeNode) bool {
	if n.Kind != "session" {
		return false
	}
	if len(n.Children) > 0 || len(n.RunningJobs) > 0 || len(n.CompletedJobs) > 0 {
		return false
	}
	return n.State == "idle" || n.State == "ended"
}

func treeNodeLess(a, b TreeNode, metaMap map[string]schema.SessionMeta, liveMap map[string]LiveEntry) bool {
	ma, aHasMeta := metaMap[a.ID]
	mb, bHasMeta := metaMap[b.ID]
	if aHasMeta && bHasMeta {
		return sessionMetaLess(ma, mb)
	}
	if !aHasMeta && !bHasMeta {
		la, aLive := liveMap[a.ID]
		lb, bLive := liveMap[b.ID]
		if aLive && bLive {
			return liveEntryLess(la, lb)
		}
	}
	return sessionOrderLess(
		sessionOrderKey{updated: a.UpdatedAt, created: a.CreatedAt, title: a.Title, id: a.ID},
		sessionOrderKey{updated: b.UpdatedAt, created: b.CreatedAt, title: b.Title, id: b.ID},
	)
}
