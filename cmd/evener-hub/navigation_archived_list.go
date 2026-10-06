package hub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// navigationArchivedPage is one page of a project's archived rows. It is read
// from the core the navigation resources are served from, so the rows, their
// order and their decoration are exactly the project's archived tier.
type navigationArchivedPage struct {
	Sessions   hubapi.NavigationArray[hubapi.NavigationSessionSummary]
	NextCursor string
	Total      int
	// Catalog is the catalog read; the zero kind when none of the catalogs the
	// hint allows holds the key.
	Catalog navigationResourceKind
}

// navigationArchivedListRequest is a validated evener/archived/list request.
// Hint is the catalog the caller named, the zero kind when it named none.
// After is the key of the last row the caller holds; nil starts at the top.
type navigationArchivedListRequest struct {
	Hint       navigationResourceKind
	ProjectKey string
	After      *hubcore.SessionOrderKey
	Limit      int
}

// parseNavigationArchivedListParams validates the wire params the way a
// navigation read validates its own: a known catalog when one is given, a
// project key that is a navigation identity, a limit in
// 0..maxNavigationSectionRows (0 or absent means the maximum), and a cursor
// this hub minted.
func parseNavigationArchivedListParams(params appwire.ArchivedListParams) (navigationArchivedListRequest, error) {
	var hint navigationResourceKind
	if params.Catalog != "" {
		parsed, err := parseNavigationCatalog(params.Catalog)
		if err != nil {
			return navigationArchivedListRequest{}, err
		}
		hint = parsed
	}
	if err := validateNavigationIdentity("project key", params.ProjectKey, false); err != nil {
		return navigationArchivedListRequest{}, err
	}
	if params.Limit < 0 || params.Limit > maxNavigationSectionRows {
		return navigationArchivedListRequest{}, fmt.Errorf("limit must be between 0 and %d (0 or absent means %d)", maxNavigationSectionRows, maxNavigationSectionRows)
	}
	request := navigationArchivedListRequest{Hint: hint, ProjectKey: params.ProjectKey, Limit: params.Limit}
	if params.Cursor != "" {
		after, err := decodeArchivedCursor(params.Cursor, hint, params.ProjectKey)
		if err != nil {
			return navigationArchivedListRequest{}, err
		}
		request.After = &after
	}
	return request, nil
}

// archivedCursor is a hubcore.SessionOrderKey on the wire, bound to the list
// it continues (the request's catalog hint and project key), so a cursor from
// another list is rejected instead of misapplied. It holds the hint rather
// than the catalog read, so the next page still follows a project that moved
// between Projects and Archived projects. A zero time.Time round-trips through
// JSON as the zero time, so a row with no timestamps keeps its place in the
// order.
type archivedCursor struct {
	Hint    navigationResourceKind `json:"k"`
	Project string                 `json:"p"`
	Updated time.Time              `json:"u"`
	Created time.Time              `json:"c"`
	Title   string                 `json:"t"`
	ID      string                 `json:"i"`
}

func encodeArchivedCursor(hint navigationResourceKind, projectKey string, key hubcore.SessionOrderKey) string {
	// A struct of times and strings always encodes.
	raw, _ := json.Marshal(archivedCursor{Hint: hint, Project: projectKey, Updated: key.Updated, Created: key.Created, Title: strings.TrimSpace(key.Title), ID: key.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeArchivedCursor(cursor string, hint navigationResourceKind, projectKey string) (hubcore.SessionOrderKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return hubcore.SessionOrderKey{}, errors.New("invalid cursor")
	}
	var c archivedCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.ID == "" {
		return hubcore.SessionOrderKey{}, errors.New("invalid cursor")
	}
	if c.Hint != hint || c.Project != projectKey {
		return hubcore.SessionOrderKey{}, errors.New("cursor belongs to another archived list")
	}
	return hubcore.SessionOrderKey{Updated: c.Updated, Created: c.Created, Title: c.Title, ID: c.ID}, nil
}

// archivedListCatalog finds the catalog holding key now, and the project it
// holds. The same key can exist in more than one catalog, so a hinted catalog
// that holds it wins. A project moves between Projects and Archived projects
// as its sessions are archived and unarchived (hubcore's Tree), so a hint to
// one member of that pair falls back to the other; a test-runs hint has no
// fallback. With no hint, the first catalog in navigationCatalogOrder holding
// the key. The zero kind means none of those holds it.
func (p navigationProjection) archivedListCatalog(hint navigationResourceKind, key string) (navigationResourceKind, hubcore.TreeProject) {
	for _, catalog := range archivedListCandidates(hint) {
		for _, project := range p.catalogs[catalog] {
			if project.Key == key {
				return catalog, project
			}
		}
	}
	return "", hubcore.TreeProject{}
}

// archivedListCandidates is the order archivedListCatalog reads catalogs in.
func archivedListCandidates(hint navigationResourceKind) []navigationResourceKind {
	switch hint {
	case "":
		return navigationCatalogOrder
	case navigationResourceProjects:
		return []navigationResourceKind{navigationResourceProjects, navigationResourceArchivedProjects}
	case navigationResourceArchivedProjects:
		return []navigationResourceKind{navigationResourceArchivedProjects, navigationResourceProjects}
	default:
		return []navigationResourceKind{hint}
	}
}

// ArchivedList returns the page of archived rows of request's project that
// follows request.After in the rail's order, from the catalog holding the
// project now (archivedListCatalog). A project none of the catalogs it may be
// read from holds answers an empty page: it was deleted, or became or stopped
// being a test run, since the rail listed it.
func (p navigationProjection) ArchivedList(request navigationArchivedListRequest) (navigationArchivedPage, error) {
	catalog, project := p.archivedListCatalog(request.Hint, request.ProjectKey)
	rows, _ := project.TierRows("archived")
	start := 0
	if request.After != nil {
		start = sort.Search(len(rows), func(i int) bool {
			return hubcore.SessionOrderLess(*request.After, hubcore.TreeNodeOrderKey(rows[i]))
		})
	}
	projector := navigationProjector{projection: p}
	pageRows, remaining := navigationPage(rows, uint32(start), request.Limit, maxNavigationSectionRows)
	sessions := make(hubapi.NavigationArray[hubapi.NavigationSessionSummary], 0, len(pageRows))
	for _, row := range pageRows {
		summary, ok := projector.projectArchivedNode(row, 1)
		if !ok {
			break
		}
		sessions = append(sessions, summary)
	}
	remaining += len(pageRows) - len(sessions)
	page := hubapi.NavigationProjectPage{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Key: request.ProjectKey, Tier: "archived", Offset: uint32(start), Sessions: sessions, Remaining: remaining}
	fitNavigationProjectPage(&page)
	if len(page.Sessions) == 0 && page.Remaining > 0 {
		return navigationArchivedPage{}, navigationPageProgressInvariantError{kind: navigationResourceProjectPage}
	}
	out := navigationArchivedPage{Sessions: page.Sessions, Total: len(rows), Catalog: catalog}
	if page.Remaining > 0 {
		out.NextCursor = encodeArchivedCursor(request.Hint, request.ProjectKey, hubcore.TreeNodeOrderKey(rows[start+len(page.Sessions)-1]))
	}
	return out, nil
}

// Archived list rows retain fork originals as inline session children. These
// are separate conversations, not delegate activity or normalized graph nodes.
// Reuse the navigation traversal and envelope limits for this inline tree.
func (p *navigationProjector) projectArchivedNode(node hubcore.TreeNode, depth int) (hubapi.NavigationSessionSummary, bool) {
	summary, ok := p.projectNode(node, depth)
	if !ok {
		return summary, false
	}
	for index, child := range node.Children {
		if child.Kind != "fork" {
			continue
		}
		original, ok := p.projectArchivedNode(child, depth+1)
		if !ok {
			summary.OmittedDescendants += countArchivedForkNodes(node.Children[index:])
			break
		}
		summary.Children = append(summary.Children, original)
	}
	return summary, true
}

// Count only the already-built fork branches eligible for archived discovery.
// Delegate branches, including their own originals, are outside this scope.
// Iteration keeps counting independent of the rendered tree's depth bound.
func countArchivedForkNodes(rows []hubcore.TreeNode) int {
	count := 0
	pending := [][]hubcore.TreeNode{rows}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, node := range current {
			if node.Kind != "fork" {
				continue
			}
			count++
			if len(node.Children) > 0 {
				pending = append(pending, node.Children)
			}
		}
	}
	return count
}

// ArchivedList serves evener/archived/list from the current core. It waits
// for a current build the way a navigation read does, then reads the archived
// tier of the project from the catalog holding it now.
func (s *NavigationService) ArchivedList(ctx context.Context, request navigationArchivedListRequest) (appwire.ArchivedListResponse, error) {
	if _, err := s.ensureSnapshot(ctx, false, nil); err != nil {
		return appwire.ArchivedListResponse{}, err
	}
	s.mu.Lock()
	core := s.core
	s.mu.Unlock()
	if core == nil {
		return appwire.ArchivedListResponse{}, navigationUnavailable(errors.New("navigation core unavailable"))
	}
	page, err := core.projection.ArchivedList(request)
	if err != nil {
		return appwire.ArchivedListResponse{}, err
	}
	sessions, err := json.Marshal(page.Sessions)
	if err != nil {
		return appwire.ArchivedListResponse{}, err
	}
	return appwire.ArchivedListResponse{Sessions: sessions, NextCursor: page.NextCursor, Total: page.Total, Catalog: string(page.Catalog)}, nil
}

// archivedCount is the number of archived sessions in a catalog's project: the
// count the rail shows on its Archived fold and the total evener/archived/list
// pages through. TreeProject.MoreArchived stays the beyond-cap remainder for
// the tree's own consumers.
func archivedCount(project hubcore.TreeProject) int {
	rows, _ := project.TierRows("archived")
	return len(rows)
}
