package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/serf/agent"
)

// Tree is the sidebar data model: a flat live-triage section and a
// project-grouped section for all sessions.
type Tree struct {
	Live     []TreeNode
	Projects []TreeProject
}

// TreeProject groups sessions by working-directory basename.
type TreeProject struct {
	Name         string
	Sessions     []TreeNode
	RollupState  string // highest-attention state across this project's live sessions; "" if none
}

// TreeNode represents a row in the sidebar.
//
// Kind:
//   - "session"  – top-level session
//   - "subagent" – spawned via spawn_agent (purple dot, indented)
//   - "fork"     – branched session (⎇ glyph, same indent as session, dim)
type TreeNode struct {
	ID       string
	Title    string
	Project  string
	State    string // "awaiting" | "processing" | "warning" | "idle" | "ended"
	Kind     string // "session" | "subagent" | "fork"
	Age      string // pre-formatted "now", "2m", "3h", "5d"
	Children []TreeNode
}

// attentionRank maps a state string to a sort key.
// Higher rank = more attention needed. Sorted descending for live triage.
func attentionRank(state string) int {
	switch state {
	case "awaiting":
		return 4
	case "processing":
		return 3
	case "warning":
		return 2
	case "idle":
		return 1
	default: // "ended" and unknown
		return 0
	}
}

// ageString formats a duration since t as a human-readable string.
func ageString(t time.Time) string {
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
func projectName(m agent.SessionMeta) string {
	if m.EnvInfo.WorkingDir == "" {
		return "(no project)"
	}
	return filepath.Base(m.EnvInfo.WorkingDir)
}

// nodeTitle computes the display title for a tree node.
//
// Older sessions persisted before OriginalTask was captured fall back to a
// short, human-friendlier rendering of the session ID rather than the full
// 26-character ULID, which clutters the sidebar.
func nodeTitle(m agent.SessionMeta, kind string) string {
	if kind == "fork" {
		base := m.OriginalTask
		if base == "" {
			base = shortID(m.ID)
		}
		if m.ForkLabel != "" {
			return base + " · " + m.ForkLabel
		}
		return base
	}
	// "session" and "subagent"
	if m.OriginalTask != "" {
		return m.OriginalTask
	}
	return shortID(m.ID)
}

// shortID renders an unnamed session ID compactly.
func shortID(id string) string {
	if len(id) <= 14 {
		return id
	}
	return "session " + id[len(id)-6:]
}

// normalizeState maps the daemon's uppercase state vocabulary to the
// lowercase tokens used by the hub UI (CSS data-state selectors,
// attention-rank ordering).
func normalizeState(s string) string {
	switch s {
	case "":
		return "idle"
	case "AWAITING_REPLY", "AWAITING":
		return "awaiting"
	case "PROCESSING", "STREAMING", "TOOL", "COMPACTING":
		return "processing"
	case "ERRORED", "ERROR":
		return "awaiting" // errored sessions need user attention; group with awaiting
	case "WARNING":
		return "warning"
	case "IDLE":
		return "idle"
	case "ENDED", "CLOSED":
		return "ended"
	}
	return strings.ToLower(s)
}

// nodeKind returns "fork", "subagent", or "session" for a meta.
//
// A "fork" is the *snapshotted original* of an edit — the older branch that
// was preserved when the user edited a prior message. It carries the
// user-supplied ForkLabel.  The newer (active) branch carries
// ParentSessionID but no ForkLabel and renders as a normal session.
func nodeKind(m agent.SessionMeta) string {
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
func BuildTree(metas []agent.SessionMeta, live []LiveEntry) Tree {
	// Index live entries by SessionID.
	liveMap := make(map[string]LiveEntry, len(live))
	for _, le := range live {
		if le.SessionID != "" {
			liveMap[le.SessionID] = le
		}
	}

	// stateFor resolves the display state for a session ID. Daemon /status
	// reports uppercase ("IDLE", "PROCESSING", "AWAITING_REPLY", "ERRORED");
	// the UI vocabulary is lowercase to match CSS data-state selectors.
	stateFor := func(id string) string {
		if le, ok := liveMap[id]; ok {
			return normalizeState(le.Status)
		}
		return "ended"
	}

	// Group metas by project name.
	type projectAccum struct {
		topLevel []agent.SessionMeta
		children map[string][]agent.SessionMeta // parentID -> children
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
				children: make(map[string][]agent.SessionMeta),
			}
			projectOrder = append(projectOrder, pname)
		}
		acc := projects[pname]
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

		// Sort top-level sessions by UpdatedAt desc.
		sort.Slice(acc.topLevel, func(i, j int) bool {
			return acc.topLevel[i].UpdatedAt.After(acc.topLevel[j].UpdatedAt)
		})

		sessions := make([]TreeNode, 0, len(acc.topLevel))
		for _, m := range acc.topLevel {
			node := TreeNode{
				ID:      m.ID,
				Title:   nodeTitle(m, "session"),
				Project: pname,
				State:   stateFor(m.ID),
				Kind:    "session",
				Age:     ageString(m.UpdatedAt),
			}

			// Build children: subagents first, then forks, each sorted by UpdatedAt desc.
			childMetas := acc.children[m.ID]
			var subagents, forks []agent.SessionMeta
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
			sort.Slice(subagents, func(i, j int) bool {
				return subagents[i].UpdatedAt.After(subagents[j].UpdatedAt)
			})
			sort.Slice(forks, func(i, j int) bool {
				return forks[i].UpdatedAt.After(forks[j].UpdatedAt)
			})

			for _, c := range subagents {
				node.Children = append(node.Children, TreeNode{
					ID:      c.ID,
					Title:   nodeTitle(c, "subagent"),
					Project: pname,
					State:   stateFor(c.ID),
					Kind:    "subagent",
					Age:     ageString(c.UpdatedAt),
				})
			}
			for _, c := range forks {
				node.Children = append(node.Children, TreeNode{
					ID:      c.ID,
					Title:   nodeTitle(c, "fork"),
					Project: pname,
					State:   stateFor(c.ID),
					Kind:    "fork",
					Age:     ageString(c.UpdatedAt),
				})
			}

			sessions = append(sessions, node)
		}

		// RollupState: highest-attention state across live sessions in this project.
		rollup := ""
		for _, le := range live {
			// Check if this live entry belongs to this project.
			// We need to find the meta for this session.
			var meta *agent.SessionMeta
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
			if attentionRank(state) > attentionRank(rollup) {
				rollup = state
			}
		}

		treeProjects = append(treeProjects, TreeProject{
			Name:        pname,
			Sessions:    sessions,
			RollupState: rollup,
		})
	}

	// Build the Live slice: every live session, flat, Kind="session",
	// sorted by attention rank desc then UpdatedAt desc.
	liveNodes := make([]TreeNode, 0, len(live))
	for _, le := range live {
		// Find the meta for this live entry.
		var meta *agent.SessionMeta
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
			node.Age = ageString(meta.UpdatedAt)
		} else {
			node.Title = shortID(le.SessionID)
			node.Age = "now"
		}
		liveNodes = append(liveNodes, node)
	}
	sort.SliceStable(liveNodes, func(i, j int) bool {
		ri, rj := attentionRank(liveNodes[i].State), attentionRank(liveNodes[j].State)
		if ri != rj {
			return ri > rj
		}
		// Same rank: sort by UpdatedAt desc (find meta again).
		var ti, tj time.Time
		for _, m := range metas {
			if m.ID == liveNodes[i].ID {
				ti = m.UpdatedAt
			}
			if m.ID == liveNodes[j].ID {
				tj = m.UpdatedAt
			}
		}
		return ti.After(tj)
	})

	return Tree{
		Live:     liveNodes,
		Projects: treeProjects,
	}
}
