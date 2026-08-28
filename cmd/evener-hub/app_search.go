package hub

import (
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

const searchPastLimit = 20

func hubSearch(cfg hubcore.WebConfig, params appwire.SearchParams) appwire.SearchResponse {
	resp := appwire.SearchResponse{
		Live: []appwire.SearchResult{},
		Past: []appwire.SearchResult{},
	}
	q := strings.ToLower(strings.TrimSpace(params.Query))
	if cfg.Roster != nil {
		live := cfg.Roster.List()
		sortLiveForSearch(live, cfg.Past)
		for _, le := range live {
			if le.SessionID == "" {
				continue
			}
			title := liveTitle(le.SessionID, le, cfg.Past)
			if q != "" && !strings.Contains(strings.ToLower(le.SessionID), q) && !strings.Contains(strings.ToLower(title), q) {
				continue
			}
			resp.Live = append(resp.Live, appwire.SearchResult{
				ID:      le.SessionID,
				Title:   title,
				State:   hubcore.NormalizeState(le.Status),
				Project: filepath.Base(le.WorkingDir),
				Age:     "now",
				Ref:     hubRefFromTreeNodeID(le.SessionID).String(),
			})
		}
	}
	if cfg.Past != nil {
		for _, e := range cfg.Past.Search(q, searchPastLimit, 0) {
			resp.Past = append(resp.Past, appwire.SearchResult{
				ID:      e.Meta.ID,
				Title:   searchPastTitle(e),
				State:   "ended",
				Project: filepath.Base(e.Meta.EnvInfo.WorkingDir),
				Age:     hubcore.AgeString(e.Meta.UpdatedAt),
				Ref:     hubRefFromTreeNodeID(e.Meta.ID).String(),
			})
		}
	}
	return resp
}

func sortLiveForSearch(live []hubcore.LiveEntry, past *hubcore.PastIndex) {
	sort.SliceStable(live, func(i, j int) bool {
		return hubcore.LiveEntryWithPastLess(live[i], live[j], past)
	})
}

func searchPastTitle(pe hubcore.PastEntry) string {
	if title := strings.TrimSpace(pe.Meta.Name); title != "" {
		return title
	}
	return hubcore.ShortID(pe.Meta.ID)
}
