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

// defaultArchivedListLimit is the page size when a caller names none.
const defaultArchivedListLimit = 50

// navigationArchivedPage is one page of a project's archived rows. It is read
// from the core the navigation resources are served from, so the rows, their
// order and their decoration are exactly the project's archived tier.
type navigationArchivedPage struct {
	Sessions   hubapi.NavigationArray[hubapi.NavigationSessionSummary]
	NextCursor string
	Total      int
}

// errArchivedListInvalid marks a request the caller has to fix: an unknown
// catalog, or a cursor this hub did not mint.
var errArchivedListInvalid = errors.New("invalid archived list request")

// archivedCursor is a hubcore.SessionOrderKey on the wire. A zero time encodes
// as 0 and decodes back to the zero time.Time, not the Unix epoch, so a row
// with no timestamps keeps its place in the order.
type archivedCursor struct {
	Updated int64  `json:"u"`
	Created int64  `json:"c"`
	Title   string `json:"t"`
	ID      string `json:"i"`
}

func encodeArchivedCursor(key hubcore.SessionOrderKey) string {
	// A struct of ints and strings always encodes.
	raw, _ := json.Marshal(archivedCursor{Updated: cursorNanos(key.Updated), Created: cursorNanos(key.Created), Title: strings.TrimSpace(key.Title), ID: key.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeArchivedCursor(cursor string) (hubcore.SessionOrderKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return hubcore.SessionOrderKey{}, fmt.Errorf("%w: cursor", errArchivedListInvalid)
	}
	var c archivedCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.ID == "" {
		return hubcore.SessionOrderKey{}, fmt.Errorf("%w: cursor", errArchivedListInvalid)
	}
	return hubcore.SessionOrderKey{Updated: cursorTime(c.Updated), Created: cursorTime(c.Created), Title: c.Title, ID: c.ID}, nil
}

func cursorNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func cursorTime(nanos int64) time.Time {
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos).UTC()
}

// ArchivedList returns the page of the archived rows of catalog's project key
// that follows cursor in the rail's order. A project the catalog does not
// hold answers an empty page: it moved or was deleted since the rail listed
// it. The same key can exist in two catalogs, so the catalog names which
// project's rows (and which summary's count) the list is.
func (p navigationProjection) ArchivedList(catalog navigationResourceKind, projectKey, cursor string, limit int) (navigationArchivedPage, error) {
	empty := navigationArchivedPage{Sessions: hubapi.NavigationArray[hubapi.NavigationSessionSummary]{}}
	projects, ok := p.catalogs[catalog]
	if !ok {
		return empty, fmt.Errorf("%w: catalog %q", errArchivedListInvalid, catalog)
	}
	var rows []hubcore.TreeNode
	for _, project := range projects {
		if project.Key == projectKey {
			rows, _ = project.TierRows("archived")
			break
		}
	}
	start := 0
	if cursor != "" {
		after, err := decodeArchivedCursor(cursor)
		if err != nil {
			return empty, err
		}
		start = sort.Search(len(rows), func(i int) bool {
			return hubcore.SessionOrderLess(after, hubcore.TreeNodeOrderKey(rows[i]))
		})
	}
	if limit <= 0 {
		limit = defaultArchivedListLimit
	}
	limit = navigationLimit(limit, maxNavigationSectionRows)
	end := min(len(rows), start+limit)
	projector := navigationProjector{projection: p}
	page := hubapi.NavigationProjectPage{
		GenerationID: p.inputs.GenerationID, Revision: p.inputs.Revision, Key: projectKey, Tier: "archived",
		Sessions: projector.projectNodes(rows[start:end], limit),
	}
	page.Remaining = len(rows) - start - len(page.Sessions)
	fitNavigationProjectPage(&page)
	if len(page.Sessions) == 0 && page.Remaining > 0 {
		return empty, navigationPageProgressInvariantError{kind: navigationResourceProjectPage}
	}
	out := navigationArchivedPage{Sessions: page.Sessions, Total: len(rows)}
	if page.Remaining > 0 {
		out.NextCursor = encodeArchivedCursor(hubcore.TreeNodeOrderKey(rows[start+len(page.Sessions)-1]))
	}
	return out, nil
}

// ArchivedList serves evener/archived/list from the current core. It waits
// for a current build the way a navigation read does, then reads the archived
// tier of the named catalog's project.
func (s *NavigationService) ArchivedList(ctx context.Context, params appwire.ArchivedListParams) (appwire.ArchivedListResponse, error) {
	if _, err := s.ensureSnapshot(ctx, false, nil); err != nil {
		return appwire.ArchivedListResponse{}, err
	}
	s.mu.Lock()
	core := s.core
	s.mu.Unlock()
	if core == nil {
		return appwire.ArchivedListResponse{}, navigationUnavailable(errors.New("navigation core unavailable"))
	}
	page, err := core.projection.ArchivedList(navigationResourceKind(params.Catalog), params.ProjectKey, params.Cursor, params.Limit)
	if err != nil {
		return appwire.ArchivedListResponse{}, err
	}
	sessions, err := json.Marshal(page.Sessions)
	if err != nil {
		return appwire.ArchivedListResponse{}, err
	}
	return appwire.ArchivedListResponse{Sessions: sessions, NextCursor: page.NextCursor, Total: page.Total}, nil
}
