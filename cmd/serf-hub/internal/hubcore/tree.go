package hubcore

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
)

// Tree is the sidebar data model: a flat live-triage section and a
// project-grouped section for all sessions.
type Tree struct {
	Live     []TreeNode
	Projects []TreeProject
}

// TreeProject groups sessions by working-directory basename.
type TreeProject struct {
	Name        string
	WorkingDir  string // absolute path of the project's working directory; used to prefill the spawn form
	Sessions    []TreeNode
	RollupState string // highest-attention state across this project's live sessions; "" if none
	Tier        string // sidebar tier: "active" | "recent" | "older" | "test"
	// MostRecent is the project's newest top-level session activity, used to
	// rank projects within a tier (most-recent first).
	MostRecent time.Time
	Age        string // pre-formatted relative age of MostRecent ("now", "2m", "3h", "5d")
}

// Sidebar tiers. Projects are grouped into one of these bands so live work
// floats to the top and the disposable serf-e2e-* test sprawl is corralled
// into one collapsed bucket instead of drowning real projects.
const (
	TierActive = "active" // has a live/needs-you session; auto-expanded
	TierRecent = "recent" // touched within recentWindow; collapsed
	TierOlder  = "older"  // touched longer ago; collapsed
	TierTest   = "test"   // disposable serf-e2e-* single-session runs; collapsed
)

// recentWindow is the age boundary between the RECENT and OLDER tiers.
const recentWindow = 24 * time.Hour

// e2eProjectPrefix marks throwaway end-to-end test sessions whose project
// folders (serf-e2e-<rand>) otherwise bury real projects in the sidebar.
const e2eProjectPrefix = "serf-e2e-"

// isTestProject reports whether a project name is a disposable e2e test run.
func isTestProject(name string) bool {
	return strings.HasPrefix(name, e2eProjectPrefix)
}

// tierFor classifies a project into a sidebar tier. Test runs are bucketed
// regardless of recency; otherwise a non-empty rollup (a live/needs-you
// session) means ACTIVE, recency within recentWindow means RECENT, else OLDER.
func tierFor(name, rollupState string, mostRecent time.Time, now time.Time) string {
	if isTestProject(name) {
		return TierTest
	}
	if rollupState != "" && rollupState != "ended" {
		return TierActive
	}
	if !mostRecent.IsZero() && now.Sub(mostRecent) <= recentWindow {
		return TierRecent
	}
	return TierOlder
}

// tierRank orders tiers top-to-bottom in the sidebar.
func tierRank(tier string) int {
	switch tier {
	case TierActive:
		return 0
	case TierRecent:
		return 1
	case TierOlder:
		return 2
	default: // TierTest
		return 3
	}
}

// TreeNode represents a row in the sidebar.
//
// Kind:
//   - "session"  – top-level session
//   - "subagent" - created by delegate (purple dot, indented)
//   - "fork"     – branched session (⎇ glyph, same indent as session, dim)
type TreeNode struct {
	ID        string
	Title     string
	Project   string
	Branch    string // git branch at session start; empty when unknown
	State     string // "awaiting" | "active" | "warning" | "idle" | "ended"
	Kind      string // "session" | "subagent" | "fork"
	CreatedAt time.Time
	UpdatedAt time.Time
	Age       string // pre-formatted "now", "2m", "3h", "5d"
	Children  []TreeNode
}

// TierGroup is a labeled band of projects for the sidebar. Tiers render
// top-to-bottom in TierGroups order.
type TierGroup struct {
	Tier     string // "active" | "recent" | "older" | "test"
	Label    string // display label, e.g. "active", "test runs"
	Expanded bool   // tier auto-expands its projects (only the active tier)
	Projects []TreeProject
}

// tierLabel returns the sidebar header text for a tier.
func tierLabel(tier string) string {
	// Sentence-case sans labels (design-system §2 / mockup #2): the old UI
	// relied on CSS text-transform:uppercase to shout these; chrome labels are
	// now quiet sentence-case, so the display strings carry the capitalization.
	switch tier {
	case TierActive:
		return "Active"
	case TierRecent:
		return "Recent"
	case TierOlder:
		return "Older"
	default: // TierTest
		return "Test runs"
	}
}

// TierGroups buckets the tree's projects into ordered, labeled tier groups,
// preserving the within-tier ordering BuildTree already established. Empty
// tiers are omitted so the sidebar never shows a blank band.
func (t Tree) TierGroups() []TierGroup {
	order := []string{TierActive, TierRecent, TierOlder, TierTest}
	byTier := make(map[string][]TreeProject, len(order))
	for _, p := range t.Projects {
		tier := p.Tier
		if tier == "" {
			tier = TierOlder
		}
		byTier[tier] = append(byTier[tier], p)
	}
	groups := make([]TierGroup, 0, len(order))
	for _, tier := range order {
		projects := byTier[tier]
		if len(projects) == 0 {
			continue
		}
		groups = append(groups, TierGroup{
			Tier:     tier,
			Label:    tierLabel(tier),
			Expanded: tier == TierActive,
			Projects: projects,
		})
	}
	return groups
}

// AttentionRank maps a state string to a sort key.
// Higher rank = more attention needed. Sorted descending for live triage.
func AttentionRank(state string) int {
	switch state {
	case "awaiting":
		return 4
	case "active":
		return 3
	case "warning":
		return 2
	case "idle":
		return 1
	default: // "ended" and unknown
		return 0
	}
}

// rollupRank ranks states for a project's rollup dot. Per spec the dot
// reflects the most-attention-needing live child:
//
//	awaiting > warning > active > idle
//
// (warning beats processing here because a warning is something the user
// likely needs to look at, while active is the daemon making progress
// on its own.)
func rollupRank(state string) int {
	switch state {
	case "awaiting":
		return 4
	case "warning":
		return 3
	case "active":
		return 2
	case "idle":
		return 1
	default: // "ended" and unknown
		return 0
	}
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

// projectName returns the sidebar project label for a session meta.
func projectName(m schema.SessionMeta) string {
	if m.EnvInfo.WorkingDir == "" {
		return "(no project)"
	}
	return filepath.Base(m.EnvInfo.WorkingDir)
}

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
		return "awaiting" // errored sessions need user attention; group with awaiting
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

// BuildTree assembles the sidebar Tree from all session metas and the
// currently-live set.
func BuildTree(metas []schema.SessionMeta, live []LiveEntry) Tree {
	now := time.Now()
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
		topLevel   []schema.SessionMeta
		children   map[string][]schema.SessionMeta // parentID -> children
		workingDir string                          // first non-empty WorkingDir seen in this project
	}
	projects := make(map[string]*projectAccum)
	projectOrder := []string{} // insertion order for stable output

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
		pname := projectName(m)
		if _, ok := projects[pname]; !ok {
			projects[pname] = &projectAccum{
				children: make(map[string][]schema.SessionMeta),
			}
			projectOrder = append(projectOrder, pname)
		}
		acc := projects[pname]
		if acc.workingDir == "" && m.EnvInfo.WorkingDir != "" {
			acc.workingDir = m.EnvInfo.WorkingDir
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
	for _, pname := range projectOrder {
		acc := projects[pname]

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
				Project:   pname,
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
			sort.SliceStable(forks, func(i, j int) bool {
				return sessionMetaLess(forks[i], forks[j])
			})

			for _, c := range subagents {
				node.Children = append(node.Children, TreeNode{
					ID:        c.ID,
					Title:     nodeTitle(c, "subagent"),
					Project:   pname,
					State:     stateFor(c.ID),
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
					Project:   pname,
					State:     stateFor(c.ID),
					Kind:      "fork",
					CreatedAt: OrderCreatedAt(c.CreatedAt, c.UpdatedAt),
					UpdatedAt: OrderUpdatedAt(c.UpdatedAt, c.CreatedAt),
					Age:       AgeString(OrderUpdatedAt(c.UpdatedAt, c.CreatedAt)),
				})
			}

			sessions = append(sessions, node)
		}

		// RollupState: highest-attention state across live sessions in this project.
		rollup := ""
		for _, le := range live {
			// Check if this live entry belongs to this project.
			// We need to find the meta for this session.
			var meta *schema.SessionMeta
			for i := range metas {
				if metas[i].ID == le.SessionID && projectName(metas[i]) == pname {
					meta = &metas[i]
					break
				}
			}
			if meta == nil {
				continue
			}
			state := stateFor(le.SessionID)
			if rollupRank(state) > rollupRank(rollup) {
				rollup = state
			}
		}

		// MostRecent: newest top-level session activity in the project, used to
		// rank projects within a tier. Top-level sessions are already sorted
		// most-recent first, so the first one carries the project's recency.
		var mostRecent time.Time
		if len(sessions) > 0 {
			mostRecent = sessions[0].UpdatedAt
		}

		treeProjects = append(treeProjects, TreeProject{
			Name:        pname,
			WorkingDir:  projects[pname].workingDir,
			Sessions:    sessions,
			RollupState: rollup,
			Tier:        tierFor(pname, rollup, mostRecent, now),
			MostRecent:  mostRecent,
			Age:         AgeString(mostRecent),
		})
	}

	// Order projects by tier (active → recent → older → test), then
	// most-recent activity first within each tier. SliceStable keeps the
	// insertion order (already recency-sorted via sessionMetaLess) as the
	// tiebreak when timestamps are equal.
	sort.SliceStable(treeProjects, func(i, j int) bool {
		ti, tj := tierRank(treeProjects[i].Tier), tierRank(treeProjects[j].Tier)
		if ti != tj {
			return ti < tj
		}
		return treeProjects[i].MostRecent.After(treeProjects[j].MostRecent)
	})

	// Build the Live slice: every live session, flat, Kind="session",
	// sorted by attention rank desc, then the Hub session ordering contract.
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
		ri, rj := AttentionRank(liveNodes[i].State), AttentionRank(liveNodes[j].State)
		if ri != rj {
			return ri > rj
		}
		return treeNodeLess(liveNodes[i], liveNodes[j], metaMap, liveMap)
	})

	return Tree{
		Live:     liveNodes,
		Projects: treeProjects,
	}
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
