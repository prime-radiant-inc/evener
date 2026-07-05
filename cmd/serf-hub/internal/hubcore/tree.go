package hubcore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/hubapi"
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
//   - Live is the flat live list still consumed by the /api/tree JSON endpoint;
//     the sidebar no longer renders it.
type Tree struct {
	NeedsYou         []TreeNode
	Live             []TreeNode
	Projects         []TreeProject
	ArchivedProjects []TreeProject
}

// TreeProject groups sessions by working-directory basename. Its sessions are
// split into activity tiers (Current / Recent / Archived) computed from each
// session's last activity and archive decision.
type TreeProject struct {
	Name string
	// Key is the stable, path-derived project identifier: a slug of the full
	// working directory ("<basename>-<8-hex sha256(path)>", or "no-project" for
	// pathless sessions). Readable and collision-resistant, not collision-free —
	// every destructive use is path-validated. Name stays the basename for display.
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
	// session in it carries Origin=="test" (SERF_SESSION_ORIGIN=test): the
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
	Age          string // pre-formatted relative age of LastActivity ("now", "2m", "3h", "5d")
	// Worktrees is the count of distinct non-empty WorktreePath values across
	// the project's sessions, surfaced in the delete confirmation.
	Worktrees int
}

const (
	currentWindow = 24 * time.Hour
	archiveWindow = 14 * 24 * time.Hour
	// maxSidebarSessionsPerTier caps how many session rows each tier
	// (Current/Recent/Archived) emits in the sidebar. A project with hundreds
	// of one-shot runs would otherwise bloat the partial; the kept rows are the
	// most-recent N and the overflow is summarised as a quiet "+N older" note.
	maxSidebarSessionsPerTier = 50
)

// capTier keeps the first n rows (the input is already most-recent first) and
// returns the kept slice plus the overflow count.
func capTier(rows []TreeNode, n int) ([]TreeNode, int) {
	if len(rows) <= n {
		return rows, 0
	}
	return rows[:n], len(rows) - n
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
	ID           string
	Title        string
	Project      string
	Branch       string // git branch at session start; empty when unknown
	State        string // "errored" | "awaiting" | "active" | "warning" | "idle" | "ended"
	Kind         string // "session" | "subagent" | "fork" | "cluster"
	ClusterCount int    // for Kind=="cluster": number of folded same-titled runs
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Age          string // pre-formatted "now", "2m", "3h", "5d"
	Children     []TreeNode
}

// AttentionRank delegates to hubapi.AttentionRank. Kept as a thin exported
// wrapper (rather than removed outright) because cmd/serf-hub/web_api_tree.go
// still calls hubcore.AttentionRank; migrating that caller to hubapi directly
// is out of scope here.
func AttentionRank(state string) int { return hubapi.AttentionRank(state) }

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

// projectSlug is the stable path-derived project key: "<basename>-<8hex>" of
// the full working directory, or "no-project" when the path is empty. The
// basename is sanitized for use as a URL/query key.
func projectSlug(path string) string {
	if path == "" {
		return "no-project"
	}
	sum := sha256.Sum256([]byte(path))
	base := strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(filepath.Base(path))
	return base + "-" + hex.EncodeToString(sum[:4])
}

// ProjectSlug is the exported path-derived project key used by the hub's
// orphan-live grouping and project-key resolution. See projectSlug.
func ProjectSlug(path string) string { return projectSlug(path) }

// nodeTitle computes the display title for a tree node.
//
// Older sessions persisted before OriginalPrompt was captured fall back to a
// short, human-friendlier rendering of the session ID rather than the full
// 26-character ULID, which clutters the sidebar.
func nodeTitle(m schema.SessionMeta, kind string) string {
	base := schema.SessionDisplayName(m)
	if base == "" {
		base = ShortID(m.ID)
	}
	if kind == "fork" && m.ForkLabel != "" {
		return base + " · " + m.ForkLabel
	}
	return base
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
	case appwire.ThreadStatusWarning:
		return "warning"
	case appwire.ThreadStatusIdle:
		return "idle"
	case appwire.ThreadStatusClosed, appwire.ThreadStatusNotLoaded, "ended":
		return "ended"
	default:
		return "idle"
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

// BuildTree assembles the sidebar Tree from all session metas, the
// currently-live set, and the user's explicit archive decisions, using the
// current wall clock for tier classification.
func BuildTree(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool) Tree {
	return BuildTreeAt(metas, live, decisions, time.Now())
}

// BuildTreeAt is BuildTree with an injected clock so tier classification
// (Current/Recent/Archived, by last-activity age) is deterministic in tests.
func BuildTreeAt(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool, now time.Time) Tree {
	metas = append([]schema.SessionMeta(nil), metas...)
	sort.SliceStable(metas, func(i, j int) bool {
		return sessionMetaLess(metas[i], metas[j])
	})

	// Index live entries by SessionID.
	liveMap := make(map[string]LiveEntry, len(live))
	for _, le := range live {
		if le.SessionID != "" {
			liveMap[le.SessionID] = le
		}
	}
	metaMap := make(map[string]schema.SessionMeta, len(metas))
	for _, m := range metas {
		if m.ID != "" {
			metaMap[m.ID] = m
		}
	}

	// stateFor resolves the display state for a session ID.
	stateFor := func(id string) string {
		if le, ok := liveMap[id]; ok {
			return NormalizeState(le.Status)
		}
		return "ended"
	}

	// Group metas by project name.
	type projectAccum struct {
		name       string // basename for display
		topLevel   []schema.SessionMeta
		children   map[string][]schema.SessionMeta // parentID -> children
		workingDir string                          // the full grouping path ("" for no-project)
		worktrees  map[string]bool                 // distinct WorktreePath set
		count      int                             // number of sessions seen for this project
		anyNonTest bool                            // true once any session's Origin != "test"
	}
	projects := make(map[string]*projectAccum) // keyed by EffectiveWorkingDir path
	projectOrder := []string{}                 // insertion order (paths) for stable output

	// Build a reverse-lookup index: which session forked from each origin?
	// Used to attach a "snapshotted original" (the parent meta with ForkLabel
	// set) under the new active branch (the child whose ParentSessionID
	// matches). Per spec: the new active branch is top-level; the original
	// is the dim sibling.
	forkChildren := make(map[string]string) // origin_id -> latest_child_id
	for _, m := range metas {
		if m.ParentSessionID != "" && !m.IsSubagent {
			forkChildren[m.ParentSessionID] = m.ID
		}
	}

	for _, m := range metas {
		path := EffectiveWorkingDir(m)
		acc := projects[path]
		if acc == nil {
			acc = &projectAccum{name: projectName(m), children: map[string][]schema.SessionMeta{}, worktrees: map[string]bool{}}
			projects[path] = acc
			projectOrder = append(projectOrder, path)
		}
		if acc.workingDir == "" && path != "" {
			acc.workingDir = path
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
			// Subagents nest under their origin.
			acc.children[m.ParentSessionID] = append(acc.children[m.ParentSessionID], m)
		case m.ForkLabel != "":
			// This meta is the snapshotted original of a fork. The active
			// branch (the meta whose ParentSessionID == m.ID) is top-level;
			// this one becomes the dim child of that active branch.
			if newID, ok := forkChildren[m.ID]; ok {
				acc.children[newID] = append(acc.children[newID], m)
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
			node := TreeNode{
				ID:        m.ID,
				Title:     nodeTitle(m, kind),
				Project:   acc.name,
				Branch:    m.EnvInfo.GitBranch,
				State:     stateFor(m.ID),
				Kind:      kind,
				CreatedAt: OrderCreatedAt(m.CreatedAt, m.UpdatedAt),
				UpdatedAt: OrderUpdatedAt(m.UpdatedAt, m.CreatedAt),
				Age:       AgeString(OrderUpdatedAt(m.UpdatedAt, m.CreatedAt)),
			}

			// Build children: subagents first, then forks, each sorted by UpdatedAt desc.
			childMetas := acc.children[m.ID]
			var subagents, forks []schema.SessionMeta
			for _, c := range childMetas {
				if c.IsSubagent {
					subagents = append(subagents, c)
				} else if c.ForkLabel != "" {
					// Snapshotted original of a fork.
					forks = append(forks, c)
				}
				// Other children of m (e.g., active branches of forks where
				// m IS the original) are not displayed under m — they're
				// top-level. Defensive: ignore any other category.
			}
			sort.SliceStable(subagents, func(i, j int) bool {
				return sessionMetaLess(subagents[i], subagents[j])
			})
			if len(subagents) > maxSidebarSessionsPerTier {
				subagents = subagents[:maxSidebarSessionsPerTier]
			}
			sort.SliceStable(forks, func(i, j int) bool {
				return sessionMetaLess(forks[i], forks[j])
			})

			// A subagent cannot legitimately be live once its parent session
			// has ended: the worker process died with the parent, but a stale
			// "active"/"awaiting" entry can linger in the live map and would
			// otherwise spin ⟳ forever. Clamp the child to "ended" when the
			// parent is not live.
			parentDead := node.State == "ended"
			for _, c := range subagents {
				childState := stateFor(c.ID)
				if parentDead {
					childState = "ended"
				}
				node.Children = append(node.Children, TreeNode{
					ID:        c.ID,
					Title:     nodeTitle(c, "subagent"),
					Project:   acc.name,
					State:     childState,
					Kind:      "subagent",
					CreatedAt: OrderCreatedAt(c.CreatedAt, c.UpdatedAt),
					UpdatedAt: OrderUpdatedAt(c.UpdatedAt, c.CreatedAt),
					Age:       AgeString(OrderUpdatedAt(c.UpdatedAt, c.CreatedAt)),
				})
			}
			for _, c := range forks {
				node.Children = append(node.Children, TreeNode{
					ID:        c.ID,
					Title:     nodeTitle(c, "fork"),
					Project:   acc.name,
					State:     stateFor(c.ID),
					Kind:      "fork",
					CreatedAt: OrderCreatedAt(c.CreatedAt, c.UpdatedAt),
					UpdatedAt: OrderUpdatedAt(c.UpdatedAt, c.CreatedAt),
					Age:       AgeString(OrderUpdatedAt(c.UpdatedAt, c.CreatedAt)),
				})
			}

			sessions = append(sessions, node)
		}

		// Rollup: highest-attention state (for the dot fallback) plus the
		// magnitude counts the header renders. Counts run over the project's own
		// top-level sessions so subagents don't inflate the "how many need me"
		// answer.
		rollup := ""
		rollupLive, rollupAttn := 0, 0
		for _, s := range sessions {
			if hubapi.RollupRank(s.State) > hubapi.RollupRank(rollup) {
				rollup = s.State
			}
			switch s.State {
			case "awaiting", "warning", "errored":
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
		isArchived := projectArchivedDecision(decisions, path, acc.name) ||
			(len(current) == 0 && len(recent) == 0)

		// Cap each tier so a project with hundreds of runs can't bloat the
		// sidebar payload; the kept rows are the most-recent N (already ordered).
		current, moreCurrent := capTier(current, maxSidebarSessionsPerTier)
		recent, moreRecent := capTier(recent, maxSidebarSessionsPerTier)
		archived, moreArchived := capTier(archived, maxSidebarSessionsPerTier)

		treeProjects = append(treeProjects, TreeProject{
			Name:         acc.name,
			Key:          projectSlug(path),
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

	// Build the Live slice: every live session, flat, sorted by attention rank
	// desc, then the Hub session ordering contract. The sidebar no longer
	// renders this rail (it duplicated the active tier), but the /api/tree JSON
	// endpoint still consumes it.
	liveNodes := make([]TreeNode, 0, len(live))
	for _, le := range live {
		if le.SessionID == "" {
			continue
		}
		// Find the meta for this live entry.
		var meta *schema.SessionMeta
		for i := range metas {
			if metas[i].ID == le.SessionID {
				meta = &metas[i]
				break
			}
		}
		state := stateFor(le.SessionID)
		node := TreeNode{
			ID:    le.SessionID,
			State: state,
			Kind:  "session",
		}
		if meta != nil {
			kind := nodeKind(*meta)
			node.Kind = kind
			node.Title = nodeTitle(*meta, kind)
			node.Project = projectName(*meta)
			node.CreatedAt = OrderCreatedAt(meta.CreatedAt, meta.UpdatedAt)
			node.UpdatedAt = OrderUpdatedAt(meta.UpdatedAt, meta.CreatedAt)
			node.Age = AgeString(node.UpdatedAt)
		} else {
			node.Title = ShortID(le.SessionID)
			node.CreatedAt = le.StartedAt
			node.UpdatedAt = le.StartedAt
			node.Age = AgeString(le.StartedAt)
		}
		liveNodes = append(liveNodes, node)
	}
	sort.SliceStable(liveNodes, func(i, j int) bool {
		ri, rj := hubapi.AttentionRank(liveNodes[i].State), hubapi.AttentionRank(liveNodes[j].State)
		if ri != rj {
			return ri > rj
		}
		return treeNodeLess(liveNodes[i], liveNodes[j], metaMap, liveMap)
	})

	// Build the NeedsYou triage tier: every top-level live session in the
	// awaiting|warning|errored family, flat across all projects, errors first
	// and then oldest-blocked first so the queue works top-down. Subagents and
	// working/idle sessions are excluded — this tier is strictly "what needs me
	// right now." An archived session is suppressed even while live: archive is
	// a clearing verb (spec v5, round-4 A4/B7), so an archived-but-still-awaiting
	// session must not linger in the inbox. When nothing qualifies the tier is
	// empty and the sidebar hides it entirely.
	needsYou := make([]TreeNode, 0, len(live))
	for _, le := range live {
		if le.SessionID == "" {
			continue
		}
		st := stateFor(le.SessionID)
		if st != "awaiting" && st != "warning" && st != "errored" {
			continue
		}
		// Archive suppression: an archived session is out of the inbox even
		// while live — archive is a clearing verb (spec v5, round-4 A4/B7).
		if d := decisionFor(decisions, le.SessionID); d != nil && *d {
			continue
		}
		var meta *schema.SessionMeta
		for i := range metas {
			if metas[i].ID == le.SessionID {
				meta = &metas[i]
				break
			}
		}
		// Only top-level sessions surface in triage — a subagent's parent is
		// the actionable unit.
		if meta != nil && meta.IsSubagent {
			continue
		}
		node := TreeNode{
			ID:    le.SessionID,
			State: st,
			Kind:  "session",
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
	// Errors first, then oldest-waiting first within the amber (awaiting/warning)
	// family (spec v5). NeedsYou only ever admits errored/awaiting/warning, and
	// among those two amber states urgency is purely how long the user has been
	// blocked — awaiting and warning are equally "look at this," so only a red
	// error jumps the queue; full AttentionRank isn't used here because it would
	// also rank awaiting strictly above warning, which would wrongly separate
	// the amber family by state instead of by age.
	sort.SliceStable(needsYou, func(i, j int) bool {
		ie, je := needsYou[i].State == "errored", needsYou[j].State == "errored"
		if ie != je {
			return ie
		}
		return needsYou[i].UpdatedAt.Before(needsYou[j].UpdatedAt)
	})

	return Tree{
		NeedsYou:         needsYou,
		Live:             liveNodes,
		Projects:         activeProjects,
		ArchivedProjects: archivedProjects,
	}
}

// BuildProjectTree builds the single TreeProject named by name, for the lazy
// sidebar expand endpoint, using the current wall clock.
func BuildProjectTree(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool, name string) (TreeProject, bool) {
	return BuildProjectTreeAt(metas, live, decisions, time.Now(), name)
}

// BuildProjectTreeAt is BuildProjectTree with an injected clock. It filters the
// metas to the requested project, runs the normal tree build on that subset, and
// returns the resulting TreeProject (searching both the active and archived
// lists). ok is false when no project of that name exists.
func BuildProjectTreeAt(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool, now time.Time, name string) (TreeProject, bool) {
	subset := make([]schema.SessionMeta, 0, len(metas))
	for _, m := range metas {
		if projectName(m) == name {
			subset = append(subset, m)
		}
	}
	if len(subset) == 0 {
		return TreeProject{}, false
	}
	tree := BuildTreeAt(subset, live, decisions, now)
	for _, p := range tree.Projects {
		if p.Name == name {
			return p, true
		}
	}
	for _, p := range tree.ArchivedProjects {
		if p.Name == name {
			return p, true
		}
	}
	return TreeProject{}, false
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

// projectArchivedDecision resolves a project's manual archive decision. A
// path-keyed row always wins; a legacy basename-keyed row is honored only when
// no path-keyed row exists (round-2 B7 / round-3 G3 precedence). Returns false
// when neither row is present.
func projectArchivedDecision(decisions map[ArchiveKey]bool, path, basename string) bool {
	if v, ok := decisions[ArchiveKey{Kind: "project", ID: path}]; ok {
		return v
	}
	if v, ok := decisions[ArchiveKey{Kind: "project", ID: basename}]; ok {
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
	if len(n.Children) > 0 {
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
