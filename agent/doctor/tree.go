package doctor

import (
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
)

const maxTreeDepth = 50

// TreeNode is one session in the session tree. Edge records how this node is
// linked to its parent ("delegate" or "observer"); it is empty for the root.
type TreeNode struct {
	SessionID     string     `json:"session_id"`
	TranscriptRef string     `json:"transcript_ref,omitempty"`
	AgentType     string     `json:"agent_type,omitempty"`
	Status        string     `json:"status,omitempty"`
	Edge          string     `json:"edge,omitempty"`
	Note          string     `json:"note,omitempty"`
	Children      []TreeNode `json:"children,omitempty"`
}

// TreeOpts narrows a session-tree walk.
type TreeOpts struct {
	Depth     int  // max depth; <=0 or >maxTreeDepth means maxTreeDepth
	Observers bool // include observer edges (worker -> observer), not just delegates
}

// Tree resolves the selector and walks the session tree: delegate edges (from
// DelegateRecord, possibly crossing buckets via each child's transcript ref) and
// — with Observers — observer edges (from the worker's SessionMeta.ObservedBy).
func Tree(stateBase, selector string, opts TreeOpts) (TreeNode, error) {
	paths, err := Locate(stateBase, selector)
	if err != nil {
		return TreeNode{}, err
	}
	depth := opts.Depth
	if depth <= 0 || depth > maxTreeDepth {
		depth = maxTreeDepth
	}
	visited := map[string]bool{}
	root := TreeNode{SessionID: paths.SessionID, TranscriptRef: paths.TranscriptRef}
	return expandNode(stateBase, root, paths, opts, depth, visited), nil
}

func expandNode(stateBase string, node TreeNode, paths Paths, opts TreeOpts, depthRemaining int, visited map[string]bool) TreeNode {
	if visited[paths.SessionID] {
		node.Note = "already shown (cycle)"
		return node
	}
	visited[paths.SessionID] = true

	events, err := jobstore.ReadEvents(paths.JobsPath)
	if err != nil {
		node.Note = "jobs unreadable: " + err.Error()
		return node
	}

	node.Children = append(node.Children, delegateChildren(stateBase, events, opts, depthRemaining, visited)...)
	if opts.Observers {
		node.Children = append(node.Children, observerChildren(stateBase, paths)...)
	}
	return node
}

func delegateChildren(stateBase string, events []jobstore.Event, opts TreeOpts, depthRemaining int, visited map[string]bool) []TreeNode {
	delegates := jobstore.FoldDelegates(events)
	ordered := make([]*jobstore.DelegateRecord, 0, len(delegates))
	for _, d := range delegates {
		if d.ChildSessionID == "" {
			continue
		}
		ordered = append(ordered, d)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ChildSessionID < ordered[j].ChildSessionID })

	children := make([]TreeNode, 0, len(ordered))
	for _, d := range ordered {
		child := TreeNode{
			SessionID:     d.ChildSessionID,
			TranscriptRef: d.TranscriptRef,
			AgentType:     d.AgentType,
			Status:        string(d.Status),
			Edge:          "delegate",
		}
		childSel := d.TranscriptRef
		if childSel == "" {
			childSel = d.ChildSessionID
		}
		childPaths, err := Locate(stateBase, childSel)
		if err != nil {
			child.Note = "transcript not found"
			children = append(children, child)
			continue
		}
		if child.TranscriptRef == "" {
			child.TranscriptRef = childPaths.TranscriptRef
		}
		if depthRemaining > 1 {
			child = expandNode(stateBase, child, childPaths, opts, depthRemaining-1, visited)
		} else {
			child.Note = depthLimitNote(childPaths)
		}
		children = append(children, child)
	}
	return children
}

// depthLimitNote reports a "children not expanded" note when a node sits at the
// depth limit but actually has delegate children, so the elision is visible.
func depthLimitNote(childPaths Paths) string {
	events, err := jobstore.ReadEvents(childPaths.JobsPath)
	if err != nil {
		return ""
	}
	for _, d := range jobstore.FoldDelegates(events) {
		if d.ChildSessionID != "" {
			return "depth limit (children not expanded)"
		}
	}
	return ""
}

func observerChildren(stateBase string, paths Paths) []TreeNode {
	meta, err := schema.LoadSessionMeta(paths.BucketDir, paths.SessionID)
	if err != nil || len(meta.ObservedBy) == 0 {
		return nil
	}
	observers := append([]string(nil), meta.ObservedBy...)
	sort.Strings(observers)
	nodes := make([]TreeNode, 0, len(observers))
	for _, obs := range observers {
		node := TreeNode{SessionID: obs, Edge: "observer"}
		if op, err := Locate(stateBase, obs); err == nil {
			node.TranscriptRef = op.TranscriptRef
		} else {
			node.Note = "transcript not found"
		}
		nodes = append(nodes, node)
	}
	return nodes
}

// RenderTree renders a session tree as an indented outline. Each node shows
// SID (agent_type) status -> transcript_ref so you can pivot into
// serf-doctor transcript <ref>.
func RenderTree(root TreeNode) string {
	var b strings.Builder
	renderTreeNode(&b, root, "", true)
	return b.String()
}

func renderTreeNode(b *strings.Builder, n TreeNode, prefix string, isRoot bool) {
	label := n.SessionID
	if n.AgentType != "" {
		label += " (" + n.AgentType + ")"
	}
	if n.Edge != "" {
		label = n.Edge + " " + label
	}
	if n.Status != "" {
		label += " " + n.Status
	}
	if n.TranscriptRef != "" {
		label += " → " + n.TranscriptRef
	}
	if n.Note != "" {
		label += "  [" + n.Note + "]"
	}
	if isRoot {
		fmt.Fprintf(b, "%s\n", label)
	} else {
		fmt.Fprintf(b, "%s%s\n", prefix, label)
	}
	childPrefix := prefix + "  "
	if isRoot {
		childPrefix = "  "
	}
	for _, c := range n.Children {
		renderTreeNode(b, c, childPrefix, false)
	}
}
