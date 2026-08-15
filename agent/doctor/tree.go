package doctor

import (
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
)

const maxTreeDepth = 50

// TreeNode is one session in the session tree. Edge records how this node is
// linked to its parent ("delegate" or "observer"); it is empty for the root.
type TreeNode struct {
	SessionID     string         `json:"session_id"`
	TranscriptRef string         `json:"transcript_ref,omitempty"`
	AgentType     string         `json:"agent_type,omitempty"`
	Status        string         `json:"status,omitempty"`
	Edge          string         `json:"edge,omitempty"`
	Note          string         `json:"note,omitempty"`
	Failures      []StateFailure `json:"failures,omitempty"`
	Children      []TreeNode     `json:"children,omitempty"`
}

// TreeOpts narrows a session-tree walk.
type TreeOpts struct {
	Depth     int  // max depth; <=0 or >maxTreeDepth means maxTreeDepth
	Observers bool // include observer edges (worker -> observer), not just delegates
}

// Tree resolves the selector and walks the session tree: stable delegate edges,
// possibly crossing buckets via each child's transcript ref, and
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
	node.Failures = legacyDelegateFailures(events)
	_, stable, diagnostics, err := stableDoctorDelegates(paths)
	if err != nil {
		node.Note = "delegates unreadable: " + err.Error()
		return node
	}
	if len(diagnostics) != 0 {
		node.Note = strings.Join(diagnostics, "; ")
	}

	node.Children = append(node.Children, stableDelegateChildren(stateBase, paths.SessionID, stable, opts, depthRemaining, visited)...)
	if opts.Observers {
		node.Children = append(node.Children, observerChildren(stateBase, paths)...)
	}
	return node
}

func stableDelegateChildren(stateBase, ownerSessionID string, state delegatestore.State, opts TreeOpts, depthRemaining int, visited map[string]bool) []TreeNode {
	rows := projectDoctorDelegates(ownerSessionID, state)
	children := make([]TreeNode, 0, len(rows))
	for _, row := range rows {
		child := TreeNode{
			SessionID: row.ChildSessionID, TranscriptRef: row.TranscriptRef,
			AgentType: row.AgentType, Status: row.Phase, Edge: "delegate",
		}
		childSelector := row.TranscriptRef
		if childSelector == "" {
			childSelector = row.ChildSessionID
		}
		childPaths, err := Locate(stateBase, childSelector)
		if err != nil {
			child.Note = withDelegateReason("transcript not found", row.NotResumableReason)
			children = append(children, child)
			continue
		}
		if child.TranscriptRef == "" {
			child.TranscriptRef = childPaths.TranscriptRef
		}
		if depthRemaining > 1 {
			child = expandNode(stateBase, child, childPaths, opts, depthRemaining-1, visited)
		} else {
			child.Note = stableDepthLimitNote(childPaths)
		}
		child.Note = withDelegateReason(child.Note, row.NotResumableReason)
		children = append(children, child)
	}
	return children
}

func withDelegateReason(existing, reason string) string {
	if reason == "" {
		return existing
	}
	if existing == "" {
		return reason
	}
	return reason + "; " + existing
}

func stableDepthLimitNote(paths Paths) string {
	_, state, _, err := stableDoctorDelegates(paths)
	if err != nil {
		return ""
	}
	if len(projectDoctorDelegates(paths.SessionID, state)) != 0 {
		return "depth limit (children not expanded)"
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
