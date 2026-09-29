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
}

// navigationArchivedListRequest is a validated evener/archived/list request.
// After is the key of the last row the caller holds; nil starts at the top.
type navigationArchivedListRequest struct {
	Catalog    navigationResourceKind
	ProjectKey string
	After      *hubcore.SessionOrderKey
	Limit      int
}

// parseNavigationArchivedListParams validates the wire params the way a
// navigation read validates its own: a known catalog, a project key that is a
// navigation identity, a limit in 1..maxNavigationSectionRows (absent means
// the maximum), and a cursor this hub minted.
func parseNavigationArchivedListParams(params appwire.ArchivedListParams) (navigationArchivedListRequest, error) {
	catalog, err := parseNavigationCatalog(params.Catalog)
	if err != nil {
		return navigationArchivedListRequest{}, err
	}
	if err := validateNavigationIdentity("project key", params.ProjectKey, false); err != nil {
		return navigationArchivedListRequest{}, err
	}
	if params.Limit < 0 || params.Limit > maxNavigationSectionRows {
		return navigationArchivedListRequest{}, fmt.Errorf("limit must be between 1 and %d, or absent for %d", maxNavigationSectionRows, maxNavigationSectionRows)
	}
	request := navigationArchivedListRequest{Catalog: catalog, ProjectKey: params.ProjectKey, Limit: params.Limit}
	if params.Cursor != "" {
		after, err := decodeArchivedCursor(params.Cursor, catalog, params.ProjectKey)
		if err != nil {
			return navigationArchivedListRequest{}, err
		}
		request.After = &after
	}
	return request, nil
}

// archivedCursor is a hubcore.SessionOrderKey on the wire, bound to the list
// it continues (catalog and project key), so a cursor from another list is
// rejected instead of misapplied. A zero time.Time round-trips through JSON as
// the zero time, so a row with no timestamps keeps its place in the order.
type archivedCursor struct {
	Catalog navigationResourceKind `json:"k"`
	Project string                 `json:"p"`
	Updated time.Time              `json:"u"`
	Created time.Time              `json:"c"`
	Title   string                 `json:"t"`
	ID      string                 `json:"i"`
}

func encodeArchivedCursor(catalog navigationResourceKind, projectKey string, key hubcore.SessionOrderKey) string {
	// A struct of times and strings always encodes.
	raw, _ := json.Marshal(archivedCursor{Catalog: catalog, Project: projectKey, Updated: key.Updated, Created: key.Created, Title: strings.TrimSpace(key.Title), ID: key.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeArchivedCursor(cursor string, catalog navigationResourceKind, projectKey string) (hubcore.SessionOrderKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return hubcore.SessionOrderKey{}, errors.New("invalid cursor")
	}
	var c archivedCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.ID == "" {
		return hubcore.SessionOrderKey{}, errors.New("invalid cursor")
	}
	if c.Catalog != catalog || c.Project != projectKey {
		return hubcore.SessionOrderKey{}, errors.New("cursor belongs to another archived list")
	}
	return hubcore.SessionOrderKey{Updated: c.Updated, Created: c.Created, Title: c.Title, ID: c.ID}, nil
}

// ArchivedList returns the page of archived rows of request's catalog project
// that follows request.After in the rail's order. A project the catalog does
// not hold answers an empty page: it moved or was deleted since the rail listed
// it. The same key can exist in two catalogs, so the catalog names which
// project's rows (and which summary's count) the list is.
func (p navigationProjection) ArchivedList(request navigationArchivedListRequest) (navigationArchivedPage, error) {
	var project hubcore.TreeProject
	for _, candidate := range p.catalogs[request.Catalog] {
		if candidate.Key == request.ProjectKey {
			project = candidate
			break
		}
	}
	rows, _ := project.TierRows("archived")
	start := 0
	if request.After != nil {
		start = sort.Search(len(rows), func(i int) bool {
			return hubcore.SessionOrderLess(*request.After, hubcore.TreeNodeOrderKey(rows[i]))
		})
	}
	projector := navigationProjector{projection: p}
	sessions, remaining := projector.projectTier(project, "archived", uint32(start), request.Limit)
	page := hubapi.NavigationProjectPage{GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Key: request.ProjectKey, Tier: "archived", Offset: uint32(start), Sessions: sessions, Remaining: remaining}
	fitNavigationProjectPage(&page)
	if len(page.Sessions) == 0 && page.Remaining > 0 {
		return navigationArchivedPage{}, navigationPageProgressInvariantError{kind: navigationResourceProjectPage}
	}
	out := navigationArchivedPage{Sessions: page.Sessions, Total: len(rows)}
	if page.Remaining > 0 {
		out.NextCursor = encodeArchivedCursor(request.Catalog, request.ProjectKey, hubcore.TreeNodeOrderKey(rows[start+len(page.Sessions)-1]))
	}
	return out, nil
}

// ArchivedList serves evener/archived/list from the current core. It waits
// for a current build the way a navigation read does, then reads the archived
// tier of the named catalog's project.
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
	return appwire.ArchivedListResponse{Sessions: sessions, NextCursor: page.NextCursor, Total: page.Total}, nil
}

// archivedCount is the number of archived sessions in a catalog's project: the
// count the rail shows on its Archived fold and the total evener/archived/list
// pages through. TreeProject.MoreArchived stays the beyond-cap remainder for
// the tree's own consumers.
func archivedCount(project hubcore.TreeProject) int {
	rows, _ := project.TierRows("archived")
	return len(rows)
}
